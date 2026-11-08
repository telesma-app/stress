package stress

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/telesma-app/ctap/protocol"
	"github.com/telesma-app/ctap/transport/ctaphid"
)

func TestRunnerHonorsGlobalRequestLimit(t *testing.T) {
	t.Parallel()

	info := supportedTestInfo()
	config := DefaultConfig()
	config.DevicePath = "/dev/fake"
	config.Duration = 5 * time.Second
	config.Requests = 100
	config.Workers = 4
	config.Operations = []Operation{
		OperationPing,
		OperationGetInfo,
		OperationPINRetries,
		OperationUVRetries,
	}
	config.ProgressEvery = 0
	config.ReopenDelay = 0

	runner := Runner{
		Config:       config,
		BaselineInfo: SnapshotInfo(info),
		Open: func(context.Context, string) (Device, error) {
			return &fakeDevice{info: info}, nil
		},
	}

	report, err := runner.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if report.Attempts != config.Requests || report.Succeeded != config.Requests || report.Failed != 0 {
		t.Fatalf("attempts/ok/failed = %d/%d/%d, want %d/%d/0", report.Attempts, report.Succeeded, report.Failed, config.Requests, config.Requests)
	}

	for _, operation := range config.Operations {
		if report.Operations[operation].Attempts != 25 {
			t.Fatalf("%s attempts = %d, want 25", operation, report.Operations[operation].Attempts)
		}
	}
}

func TestRunnerReopensAfterFailure(t *testing.T) {
	t.Parallel()

	info := supportedTestInfo()
	config := DefaultConfig()
	config.DevicePath = "/dev/fake"
	config.Duration = 5 * time.Second
	config.Requests = 3
	config.Operations = []Operation{OperationPing}
	config.PingSizes = []int{32}
	config.ProgressEvery = 0
	config.ReopenDelay = 0

	var openCount atomic.Uint64

	runner := Runner{
		Config:       config,
		BaselineInfo: SnapshotInfo(info),
		Open: func(context.Context, string) (Device, error) {
			count := openCount.Add(1)
			return &fakeDevice{info: info, pingErr: errorOnFirstOpen(count)}, nil
		},
	}

	report, err := runner.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if report.Attempts != 3 || report.Succeeded != 2 || report.Failed != 1 {
		t.Fatalf("attempts/ok/failed = %d/%d/%d, want 3/2/1", report.Attempts, report.Succeeded, report.Failed)
	}

	if report.Opens != 2 || report.OpenFailures != 0 {
		t.Fatalf("opens/open failures = %d/%d, want 2/0", report.Opens, report.OpenFailures)
	}

	if report.FailureKinds["other"] != 1 {
		t.Fatalf("failure kinds = %v, want other=1", report.FailureKinds)
	}
}

func TestRunnerDetectsGetInfoDrift(t *testing.T) {
	t.Parallel()

	baseline := supportedTestInfo()
	drifted := supportedTestInfo()
	drifted.MaxMsgSize++
	config := DefaultConfig()
	config.DevicePath = "/dev/fake"
	config.Duration = 5 * time.Second
	config.Requests = 1
	config.Operations = []Operation{OperationGetInfo}
	config.ProgressEvery = 0
	config.ReopenDelay = 0

	runner := Runner{
		Config:       config,
		BaselineInfo: SnapshotInfo(baseline),
		Open: func(context.Context, string) (Device, error) {
			return &fakeDevice{info: drifted}, nil
		},
	}

	report, err := runner.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if report.Failed != 1 || report.FailureKinds["get-info-drift"] != 1 {
		t.Fatalf("failed/kinds = %d/%v, want drift failure", report.Failed, report.FailureKinds)
	}
}

func TestRunnerExecutesLock(t *testing.T) {
	t.Parallel()

	info := supportedTestInfo()
	config := DefaultConfig()
	config.DevicePath = "/dev/fake"
	config.Duration = 5 * time.Second
	config.Requests = 1
	config.Operations = []Operation{OperationLock}
	config.LockSeconds = 7
	config.ProgressEvery = 0

	device := &fakeDevice{info: info}
	runner := Runner{
		Config:       config,
		BaselineInfo: SnapshotInfo(info),
		Open: func(context.Context, string) (Device, error) {
			return device, nil
		},
	}

	report, err := runner.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if got := device.lockSeconds.Load(); got != uint64(config.LockSeconds) {
		t.Fatalf("Lock seconds = %d, want %d", got, config.LockSeconds)
	}

	stats := report.Operations[OperationLock]
	if stats.Attempts != 1 || stats.Succeeded != 1 || stats.Failed != 0 {
		t.Fatalf("lock attempts/ok/failed = %d/%d/%d, want 1/1/0", stats.Attempts, stats.Succeeded, stats.Failed)
	}

	if report.LockSeconds != config.LockSeconds {
		t.Fatalf("report lock seconds = %d, want %d", report.LockSeconds, config.LockSeconds)
	}
}

func TestRunnerClassifiesCTAPHIDLockError(t *testing.T) {
	t.Parallel()

	info := supportedTestInfo()
	config := DefaultConfig()
	config.DevicePath = "/dev/fake"
	config.Duration = 5 * time.Second
	config.Requests = 1
	config.Operations = []Operation{OperationLock}
	config.ProgressEvery = 0
	config.ReopenDelay = 0

	runner := Runner{
		Config:       config,
		BaselineInfo: SnapshotInfo(info),
		Open: func(context.Context, string) (Device, error) {
			return &fakeDevice{
				info:    info,
				lockErr: &ctaphid.ErrorResponse{ErrorCode: ctaphid.ERR_INVALID_CMD},
			}, nil
		},
	}

	report, err := runner.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if report.Failed != 1 || report.FailureKinds["ctaphid-ERR_INVALID_CMD"] != 1 {
		t.Fatalf("failed/kinds = %d/%v, want CTAPHID invalid command", report.Failed, report.FailureKinds)
	}
}

func TestRunnerDoesNotCountRunDeadlineAsDeviceFailure(t *testing.T) {
	t.Parallel()

	info := supportedTestInfo()
	config := DefaultConfig()
	config.DevicePath = "/dev/fake"
	config.Duration = 10 * time.Millisecond
	config.CommandTimeout = time.Second
	config.Operations = []Operation{OperationPing}
	config.PingSizes = []int{1}
	config.ProgressEvery = 0
	config.ReopenDelay = 0

	runner := Runner{
		Config:       config,
		BaselineInfo: SnapshotInfo(info),
		Open: func(context.Context, string) (Device, error) {
			return &blockingFakeDevice{fakeDevice: fakeDevice{info: info}}, nil
		},
	}

	report, err := runner.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if report.Failed != 0 {
		t.Fatalf("deadline produced %d device failures", report.Failed)
	}
}

func TestRunnerCountsCommandTimeout(t *testing.T) {
	t.Parallel()

	info := supportedTestInfo()
	config := DefaultConfig()
	config.DevicePath = "/dev/fake"
	config.Duration = time.Second
	config.Requests = 1
	config.CommandTimeout = 5 * time.Millisecond
	config.Operations = []Operation{OperationPing}
	config.PingSizes = []int{1}
	config.ProgressEvery = 0
	config.ReopenDelay = 0

	runner := Runner{
		Config:       config,
		BaselineInfo: SnapshotInfo(info),
		Open: func(context.Context, string) (Device, error) {
			return &blockingFakeDevice{fakeDevice: fakeDevice{info: info}}, nil
		},
	}

	report, err := runner.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if report.Failed != 1 || report.FailureKinds["timeout"] != 1 {
		t.Fatalf("failed/kinds = %d/%v, want timeout failure", report.Failed, report.FailureKinds)
	}

	if len(report.FailureSamples) != 1 || report.FailureSamples[0].PingBytes == nil || *report.FailureSamples[0].PingBytes != 1 {
		t.Fatalf("failure samples = %#v, want pingBytes=1", report.FailureSamples)
	}
}

func TestRunnerClosesDeviceWhenOpenFinishesAfterCancellation(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	device := &fakeDevice{}
	config := DefaultConfig()
	config.DevicePath = "/dev/fake"
	config.Requests = 1
	config.ProgressEvery = 0
	runner := Runner{
		Config: config,
		Open: func(context.Context, string) (Device, error) {
			cancel()
			return device, nil
		},
	}

	report, err := runner.Run(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Run error = %v, want cancellation", err)
	}

	if !device.closed.Load() {
		t.Error("device returned after cancellation was not closed")
	}

	if report.Attempts != 0 || report.Failed != 0 {
		t.Errorf("cancellation produced attempts/failures = %d/%d", report.Attempts, report.Failed)
	}
}

func TestRunnerRateLimit(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name     string
		rate     float64
		duration time.Duration
		attempts uint64
	}{
		{name: "wait is canceled by run deadline", rate: 1e-9, duration: 10 * time.Millisecond},
		{name: "ticks allow requests", rate: 1e4, duration: time.Second, attempts: 3},
	} {
		t.Run(test.name, func(t *testing.T) {
			config := DefaultConfig()
			config.DevicePath = "/dev/fake"
			config.Duration = test.duration
			config.Rate = test.rate
			config.Requests = 3
			config.Workers = 2
			config.ProgressEvery = 0
			config.Operations = []Operation{OperationPing}
			config.PingSizes = []int{0}
			runner := Runner{
				Config: config,
				Open: func(context.Context, string) (Device, error) {
					return &fakeDevice{}, nil
				},
			}

			report, err := runner.Run(t.Context())
			if err != nil {
				t.Fatalf("Run: %v", err)
			}

			if report.Attempts != test.attempts || report.Succeeded != test.attempts || report.Failed != 0 {
				t.Errorf("attempts/ok/failed = %d/%d/%d, want %d/%d/0", report.Attempts, report.Succeeded, report.Failed, test.attempts, test.attempts)
			}

			if test.attempts == 0 && report.Opens != 0 {
				t.Errorf("opened %d devices while waiting for a rate limit tick", report.Opens)
			}
		})
	}
}

func TestRunnerWaitsForProgressToFinish(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	config := DefaultConfig()
	config.DevicePath = "/dev/fake"
	config.Duration = time.Second
	config.ProgressEvery = time.Millisecond
	config.Operations = []Operation{OperationPing}
	config.PingSizes = []int{0}

	var snapshot Snapshot

	runner := Runner{
		Config: config,
		Open: func(context.Context, string) (Device, error) {
			return &fakeDevice{}, nil
		},
		Progress: func(current Snapshot) {
			if current.Attempts != 0 {
				cancel()

				snapshot = current
			}
		},
	}

	report, err := runner.Run(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Run error = %v, want cancellation from progress callback", err)
	}

	if snapshot.Attempts == 0 || report.Attempts < snapshot.Attempts {
		t.Errorf("progress/final attempts = %d/%d", snapshot.Attempts, report.Attempts)
	}
}

func TestPingPayloadIsDeterministicAndVaries(t *testing.T) {
	t.Parallel()

	first := pingPayload(1, 2, 128)
	second := pingPayload(1, 2, 128)
	other := pingPayload(2, 2, 128)

	if string(first) != string(second) {
		t.Fatal("same inputs produced different payloads")
	}

	if string(first) == string(other) {
		t.Fatal("different worker produced identical payload")
	}
}

func errorOnFirstOpen(openCount uint64) error {
	if openCount == 1 {
		return errors.New("injected ping failure")
	}

	return nil
}

func supportedTestInfo() protocol.AuthenticatorGetInfoResponse {
	return protocol.AuthenticatorGetInfoResponse{
		Versions:           protocol.Versions{protocol.FIDO_2_1},
		MaxMsgSize:         1024,
		PinUvAuthProtocols: []protocol.PinUvAuthProtocol{protocol.PinUvAuthProtocolTwo},
		Options: map[protocol.Option]bool{
			protocol.OptionClientPIN:        true,
			protocol.OptionUserVerification: true,
		},
	}
}

type fakeDevice struct {
	info        protocol.AuthenticatorGetInfoResponse
	pingErr     error
	lockErr     error
	lockSeconds atomic.Uint64
	closed      atomic.Bool
}

type blockingFakeDevice struct {
	fakeDevice
}

func (d *blockingFakeDevice) Ping(ctx context.Context, _ []byte) error {
	<-ctx.Done()
	return ctx.Err()
}

func (d *fakeDevice) Ping(context.Context, []byte) error {
	return d.pingErr
}

func (d *fakeDevice) Lock(_ context.Context, seconds uint) error {
	d.lockSeconds.Store(uint64(seconds))
	return d.lockErr
}

func (d *fakeDevice) GetInfo(context.Context) (protocol.AuthenticatorGetInfoResponse, error) {
	return d.info, nil
}

func (d *fakeDevice) GetPINRetries(context.Context) (uint, *bool, error) {
	return 8, nil, nil
}

func (d *fakeDevice) GetUVRetries(context.Context) (uint, error) {
	return 5, nil
}

func (d *fakeDevice) Close() error {
	d.closed.Store(true)
	return nil
}
