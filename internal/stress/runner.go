package stress

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
	"time"

	"github.com/go-ctap/ctap/authenticator"
	"github.com/go-ctap/ctap/protocol"
	ctaptransport "github.com/go-ctap/ctap/transport"
	"github.com/go-ctap/ctap/transport/ctaphid"
)

// Device is the non-mutating subset used by the stress runner.
type Device interface {
	io.Closer
	Ping(context.Context, []byte) error
	GetInfo(context.Context) (protocol.AuthenticatorGetInfoResponse, error)
	GetInfoCached() (protocol.AuthenticatorGetInfoResponse, bool)
	GetPINRetries(context.Context) (uint, *bool, error)
	GetUVRetries(context.Context) (uint, error)
}

// OpenFunc opens one independently owned authenticator channel.
type OpenFunc func(context.Context, string) (Device, error)

// ProgressFunc receives periodic aggregate snapshots. It must return quickly.
type ProgressFunc func(Snapshot)

// Runner executes a bounded read-only stress run.
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
func (r *Runner) Run(parent context.Context) (Report, error) {
	if r.Open == nil {
		return Report{}, fmt.Errorf("open function is nil")
	}
	if err := r.Config.Validate(); err != nil {
		return Report{}, err
	}
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
	limiter, err := newRateLimiter(r.Config.Rate)
	if err != nil {
		return Report{}, err
	}
	defer limiter.Close()

	progressDone := make(chan struct{})
	go r.reportProgress(runCtx, metrics, progressDone)

	var workers sync.WaitGroup
	workers.Add(r.Config.Workers)
	for workerID := range r.Config.Workers {
		go func() {
			defer workers.Done()
			r.runWorker(runCtx, workerID+1, baselineSignature, limiter, metrics)
		}()
	}
	workers.Wait()
	cancel()
	<-progressDone

	report := metrics.report(time.Now())
	report.DevicePath = r.Config.DevicePath
	report.Workers = r.Config.Workers
	report.ConfiguredOps = append([]Operation(nil), r.Config.Operations...)
	report.PingSizes = append([]int(nil), r.Config.PingSizes...)
	report.CommandTimeout = r.Config.CommandTimeout
	report.BaselineInfo = r.BaselineInfo
	report.InfoSignature = baselineSignature

	if parent.Err() != nil {
		return report, parent.Err()
	}

	return report, nil
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
	limiter *rateLimiter,
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

	for {
		requestNumber, ok := r.claimRequest()
		if !ok {
			return
		}
		if err := limiter.Wait(ctx); err != nil {
			return
		}

		operation := r.Config.Operations[(requestNumber-1)%uint64(len(r.Config.Operations))]
		if device == nil {
			startedAt := time.Now()
			opened, err := r.openDevice(ctx)
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
			device = opened
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

func (r *Runner) openDevice(ctx context.Context) (Device, error) {
	openCtx, cancel := context.WithTimeout(ctx, r.Config.OpenTimeout)
	defer cancel()

	return r.Open(openCtx, r.Config.DevicePath)
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
		result.Err = fmt.Errorf("unknown operation %q", operation)
	}
	result.Duration = time.Since(startedAt)
	if result.Err != nil {
		result.ErrorKind = classifyError(result.Err)
	}

	return result
}

func (r *Runner) reportProgress(ctx context.Context, metrics *metricSet, done chan<- struct{}) {
	defer close(done)
	if r.Progress == nil || r.Config.ProgressEvery == 0 {
		<-ctx.Done()
		return
	}

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
	if err == nil {
		return ""
	}
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

type rateLimiter struct {
	ticker *time.Ticker
}

func newRateLimiter(rate float64) (*rateLimiter, error) {
	if rate == 0 {
		return &rateLimiter{}, nil
	}
	interval := time.Duration(float64(time.Second) / rate)
	if interval < time.Nanosecond {
		return nil, fmt.Errorf("rate %.3f is too high", rate)
	}

	return &rateLimiter{ticker: time.NewTicker(interval)}, nil
}

func (l *rateLimiter) Wait(ctx context.Context) error {
	if l.ticker == nil {
		return ctx.Err()
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-l.ticker.C:
		return nil
	}
}

func (l *rateLimiter) Close() {
	if l.ticker != nil {
		l.ticker.Stop()
	}
}
