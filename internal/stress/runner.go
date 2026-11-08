package stress

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
	"time"

	"github.com/telesma-app/ctap/authenticator"
	"github.com/telesma-app/ctap/protocol"
	ctaptransport "github.com/telesma-app/ctap/transport"
	"github.com/telesma-app/ctap/transport/ctaphid"
)

// Device is the non-destructive subset used by the stress runner.
type Device interface {
	io.Closer
	Ping(context.Context, []byte) error
	Lock(context.Context, uint) error
	GetInfo(context.Context) (protocol.AuthenticatorGetInfoResponse, error)
	GetPINRetries(context.Context) (uint, *bool, error)
	GetUVRetries(context.Context) (uint, error)
}

// OpenFunc opens one independently owned authenticator channel.
type OpenFunc func(context.Context, string) (Device, error)

// ProgressFunc receives periodic aggregate snapshots. It must return quickly.
type ProgressFunc func(Snapshot)

// Runner executes a bounded non-destructive stress run.
type Runner struct {
	Config       Config
	Open         OpenFunc
	BaselineInfo InfoSnapshot
	Progress     ProgressFunc

	nextRequest atomic.Uint64
	nextPing    atomic.Uint64
}

// Run opens one independent CTAPHID channel per worker and executes commands
// until the duration or global request limit is reached.
// Config must be validated before Run, and Open must be set.
// The Runner and its configuration must not be modified until Run returns.
func (r *Runner) Run(parent context.Context) (Report, error) {
	r.nextRequest.Store(0)
	r.nextPing.Store(0)

	baselineSignature, err := r.BaselineInfo.Signature()
	if err != nil {
		return Report{}, err
	}

	runCtx, cancel := r.runContext(parent)
	defer cancel()

	startedAt := time.Now()
	metrics := newMetricSet(startedAt, r.Config.FailureSamples)

	var ticks <-chan time.Time

	if r.Config.Rate > 0 {
		ticker := time.NewTicker(time.Duration(float64(time.Second) / r.Config.Rate))
		defer ticker.Stop()

		ticks = ticker.C
	}

	var progress sync.WaitGroup
	if r.Progress != nil && r.Config.ProgressEvery > 0 {
		progress.Go(func() { r.reportProgress(runCtx, metrics) })
	}

	var workers sync.WaitGroup
	for workerID := range r.Config.Workers {
		workers.Go(func() {
			r.runWorker(runCtx, workerID+1, baselineSignature, ticks, metrics)
		})
	}

	workers.Wait()
	cancel()
	progress.Wait()

	report := metrics.report(time.Now())
	report.DevicePath = r.Config.DevicePath
	report.Workers = r.Config.Workers
	report.ConfiguredOps = append([]Operation(nil), r.Config.Operations...)
	report.PingSizes = append([]int(nil), r.Config.PingSizes...)
	report.LockSeconds = r.Config.LockSeconds
	report.CommandTimeout = r.Config.CommandTimeout
	report.BaselineInfo = r.BaselineInfo
	report.InfoSignature = baselineSignature

	return report, parent.Err()
}

func (r *Runner) runContext(parent context.Context) (context.Context, context.CancelFunc) {
	if r.Config.Duration > 0 {
		return context.WithTimeout(parent, r.Config.Duration)
	}

	return context.WithCancel(parent)
}

func (r *Runner) runWorker(
	ctx context.Context,
	workerID int,
	baselineSignature string,
	ticks <-chan time.Time,
	metrics *metricSet,
) {
	var device Device
	defer func() {
		if device != nil {
			if err := device.Close(); err != nil {
				metrics.recordCloseFailure(workerID, err)
			}
		}
	}()

	for ctx.Err() == nil {
		requestNumber, ok := r.claimRequest()
		if !ok {
			return
		}

		if ticks != nil {
			select {
			case <-ctx.Done():
				return
			case <-ticks:
			}
		}

		operation := r.Config.Operations[(requestNumber-1)%uint64(len(r.Config.Operations))]

		if device == nil {
			startedAt := time.Now()
			openCtx, cancel := context.WithTimeout(ctx, r.Config.OpenTimeout)

			var err error

			device, err = r.Open(openCtx, r.Config.DevicePath)

			cancel()

			openDuration := time.Since(startedAt)

			if ctx.Err() != nil {
				return
			}

			metrics.recordOpen(err == nil)

			if err != nil {
				metrics.record(attempt{
					Operation: operation,
					Worker:    workerID,
					Duration:  openDuration,
					Err:       fmt.Errorf("open device: %w", err),
					ErrorKind: "open/" + classifyError(err),
				})

				if !waitReopen(ctx, r.Config.ReopenDelay) {
					return
				}

				continue
			}
		}

		result := r.execute(ctx, device, workerID, operation, baselineSignature)
		if ctx.Err() != nil && errors.Is(result.Err, ctx.Err()) {
			return
		}

		metrics.record(result)

		if result.Err == nil || !r.Config.ReopenOnError {
			continue
		}

		if err := device.Close(); err != nil {
			metrics.recordCloseFailure(workerID, err)
		}

		device = nil

		if !waitReopen(ctx, r.Config.ReopenDelay) {
			return
		}
	}
}

func (r *Runner) claimRequest() (uint64, bool) {
	number := r.nextRequest.Add(1)
	if r.Config.Requests != 0 && number > r.Config.Requests {
		return 0, false
	}

	return number, true
}

func (r *Runner) execute(
	ctx context.Context,
	device Device,
	workerID int,
	operation Operation,
	baselineSignature string,
) attempt {
	commandCtx, cancel := context.WithTimeout(ctx, r.Config.CommandTimeout)
	defer cancel()

	startedAt := time.Now()

	result := attempt{Operation: operation, Worker: workerID}
	switch operation {
	case OperationPing:
		pingNumber := r.nextPing.Add(1)
		size := r.Config.PingSizes[(pingNumber-1)%uint64(len(r.Config.PingSizes))]
		payload := pingPayload(workerID, pingNumber, size)
		result.PingBytes = size
		result.PingSet = true
		result.Err = device.Ping(commandCtx, payload)
	case OperationLock:
		result.Err = device.Lock(commandCtx, r.Config.LockSeconds)
	case OperationGetInfo:
		var info protocol.AuthenticatorGetInfoResponse

		info, result.Err = device.GetInfo(commandCtx)
		if result.Err == nil && r.Config.CheckInfoStable {
			var signature string

			signature, result.Err = SnapshotInfo(info).Signature()
			if result.Err == nil && signature != baselineSignature {
				result.Err = &infoDriftError{Want: baselineSignature, Got: signature}
			}
		}
	case OperationPINRetries:
		_, _, result.Err = device.GetPINRetries(commandCtx)
	case OperationUVRetries:
		_, result.Err = device.GetUVRetries(commandCtx)
	default:
		panic(fmt.Sprintf("unknown operation %q", operation))
	}

	result.Duration = time.Since(startedAt)
	if result.Err != nil {
		result.ErrorKind = classifyError(result.Err)
	}

	return result
}

func (r *Runner) reportProgress(ctx context.Context, metrics *metricSet) {
	ticker := time.NewTicker(r.Config.ProgressEvery)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			r.Progress(metrics.snapshot(now))
		}
	}
}

func pingPayload(workerID int, sequence uint64, size int) []byte {
	payload := make([]byte, size)

	state := sequence ^ uint64(workerID)*0x9e3779b97f4a7c15
	for index := range payload {
		state ^= state << 13
		state ^= state >> 7
		state ^= state << 17
		payload[index] = byte(state)
	}

	return payload
}

type infoDriftError struct {
	Want string
	Got  string
}

func (e *infoDriftError) Error() string {
	return fmt.Sprintf("stable GetInfo fields changed: baseline %s, got %s", e.Want, e.Got)
}

func classifyError(err error) string {
	if errors.Is(err, context.DeadlineExceeded) {
		return "timeout"
	}

	if errors.Is(err, context.Canceled) {
		return "canceled"
	}

	var drift *infoDriftError
	if errors.As(err, &drift) {
		return "get-info-drift"
	}

	if errors.Is(err, authenticator.ErrPingPongMismatch) {
		return "ping-mismatch"
	}

	var invalidated *ctaptransport.DeviceInvalidatedError
	if errors.As(err, &invalidated) {
		return "device-invalidated"
	}

	var ioErr *ctaptransport.IOError
	if errors.As(err, &ioErr) {
		return "io-" + string(ioErr.Operation)
	}

	var ctapErr *ctaptransport.CTAPError
	if errors.As(err, &ctapErr) {
		return "ctap-" + ctapErr.StatusCode.String()
	}

	var ctaphidErr *ctaphid.ErrorResponse
	if errors.As(err, &ctaphidErr) {
		return "ctaphid-" + ctaphidErr.ErrorCode.String()
	}

	return "other"
}

func waitReopen(ctx context.Context, delay time.Duration) bool {
	if delay == 0 {
		return ctx.Err() == nil
	}

	timer := time.NewTimer(delay)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
