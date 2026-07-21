package stress

import (
	"cmp"
	"slices"
	"sync"
	"time"
)

var latencyBounds = []time.Duration{
	10 * time.Microsecond,
	25 * time.Microsecond,
	50 * time.Microsecond,
	100 * time.Microsecond,
	250 * time.Microsecond,
	500 * time.Microsecond,
	time.Millisecond,
	2500 * time.Microsecond,
	5 * time.Millisecond,
	10 * time.Millisecond,
	25 * time.Millisecond,
	50 * time.Millisecond,
	100 * time.Millisecond,
	250 * time.Millisecond,
	500 * time.Millisecond,
	time.Second,
	2500 * time.Millisecond,
	5 * time.Second,
	10 * time.Second,
	30 * time.Second,
	time.Minute,
}

type attempt struct {
	Operation Operation
	Worker    int
	Duration  time.Duration
	PingBytes int
	PingSet   bool
	Err       error
	ErrorKind string
}

type metricSet struct {
	mu             sync.Mutex
	startedAt      time.Time
	operations     map[Operation]*operationMetric
	failureKinds   map[string]uint64
	failureSamples []FailureSample
	sampleLimit    int
	opens          uint64
	openFailures   uint64
	closeFailures  uint64
}

type operationMetric struct {
	attempts  uint64
	succeeded uint64
	failed    uint64
	pingBytes uint64
	total     time.Duration
	min       time.Duration
	max       time.Duration
	buckets   []uint64
}

// FailureSample preserves a bounded amount of diagnostic context.
type FailureSample struct {
	At        time.Time `json:"at"`
	Worker    int       `json:"worker"`
	Operation Operation `json:"operation"`
	PingBytes *int      `json:"pingBytes,omitempty"`
	Kind      string    `json:"kind"`
	Error     string    `json:"error"`
}

func newMetricSet(startedAt time.Time, sampleLimit int) *metricSet {
	return &metricSet{
		startedAt:    startedAt,
		operations:   make(map[Operation]*operationMetric),
		failureKinds: make(map[string]uint64),
		sampleLimit:  sampleLimit,
	}
}

func (m *metricSet) record(result attempt) {
	m.mu.Lock()
	defer m.mu.Unlock()

	metric := m.operations[result.Operation]
	if metric == nil {
		metric = &operationMetric{buckets: make([]uint64, len(latencyBounds)+1)}
		m.operations[result.Operation] = metric
	}
	metric.attempts++
	metric.total += result.Duration
	metric.pingBytes += uint64(result.PingBytes)
	if metric.min == 0 || result.Duration < metric.min {
		metric.min = result.Duration
	}
	if result.Duration > metric.max {
		metric.max = result.Duration
	}
	bucket := len(latencyBounds)
	for index, bound := range latencyBounds {
		if result.Duration <= bound {
			bucket = index
			break
		}
	}
	metric.buckets[bucket]++

	if result.Err == nil {
		metric.succeeded++
		return
	}

	metric.failed++
	m.failureKinds[result.ErrorKind]++
	if len(m.failureSamples) < m.sampleLimit {
		sample := FailureSample{
			At:        time.Now().UTC(),
			Worker:    result.Worker,
			Operation: result.Operation,
			Kind:      result.ErrorKind,
			Error:     result.Err.Error(),
		}
		if result.PingSet {
			pingBytes := result.PingBytes
			sample.PingBytes = &pingBytes
		}
		m.failureSamples = append(m.failureSamples, sample)
	}
}

func (m *metricSet) recordOpen(success bool) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.opens++
	if !success {
		m.openFailures++
	}
}

func (m *metricSet) recordCloseFailure(worker int, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.closeFailures++
	kind := "close/" + classifyError(err)
	m.failureKinds[kind]++
	if len(m.failureSamples) < m.sampleLimit {
		m.failureSamples = append(m.failureSamples, FailureSample{
			At:        time.Now().UTC(),
			Worker:    worker,
			Operation: Operation("close"),
			Kind:      kind,
			Error:     err.Error(),
		})
	}
}

func (m *metricSet) snapshot(now time.Time) Snapshot {
	m.mu.Lock()
	defer m.mu.Unlock()

	snapshot := Snapshot{
		Elapsed:      now.Sub(m.startedAt),
		FailureKinds: make(map[string]uint64, len(m.failureKinds)),
	}
	for kind, count := range m.failureKinds {
		snapshot.FailureKinds[kind] = count
	}
	for _, metric := range m.operations {
		snapshot.Attempts += metric.attempts
		snapshot.Succeeded += metric.succeeded
		snapshot.Failed += metric.failed
	}
	if snapshot.Elapsed > 0 {
		snapshot.RequestsPerSecond = float64(snapshot.Attempts) / snapshot.Elapsed.Seconds()
	}

	return snapshot
}

func (m *metricSet) report(endedAt time.Time) Report {
	m.mu.Lock()
	defer m.mu.Unlock()

	report := Report{
		StartedAt:      m.startedAt.UTC(),
		EndedAt:        endedAt.UTC(),
		Elapsed:        endedAt.Sub(m.startedAt),
		Operations:     make(map[Operation]OperationStats, len(m.operations)),
		FailureKinds:   make(map[string]uint64, len(m.failureKinds)),
		FailureSamples: slices.Clone(m.failureSamples),
		Opens:          m.opens,
		OpenFailures:   m.openFailures,
		CloseFailures:  m.closeFailures,
	}
	for operation, metric := range m.operations {
		stats := OperationStats{
			Attempts:  metric.attempts,
			Succeeded: metric.succeeded,
			Failed:    metric.failed,
			PingBytes: metric.pingBytes,
			Min:       metric.min,
			Max:       metric.max,
			P50:       percentile(metric.buckets, metric.attempts, 0.50),
			P95:       percentile(metric.buckets, metric.attempts, 0.95),
			P99:       percentile(metric.buckets, metric.attempts, 0.99),
		}
		if metric.attempts != 0 {
			stats.Average = metric.total / time.Duration(metric.attempts)
		}
		report.Operations[operation] = stats
		report.Attempts += metric.attempts
		report.Succeeded += metric.succeeded
		report.Failed += metric.failed
	}
	for kind, count := range m.failureKinds {
		report.FailureKinds[kind] = count
	}
	if report.Elapsed > 0 {
		report.RequestsPerSecond = float64(report.Attempts) / report.Elapsed.Seconds()
	}

	return report
}

func percentile(buckets []uint64, total uint64, quantile float64) time.Duration {
	if total == 0 {
		return 0
	}
	rank := uint64(float64(total)*quantile + 0.999999999)
	var seen uint64
	for index, count := range buckets {
		seen += count
		if seen >= rank {
			if index < len(latencyBounds) {
				return latencyBounds[index]
			}
			return latencyBounds[len(latencyBounds)-1]
		}
	}

	return latencyBounds[len(latencyBounds)-1]
}

// SortedOperations returns deterministic operation names for text rendering.
func (r Report) SortedOperations() []Operation {
	operations := make([]Operation, 0, len(r.Operations))
	for operation := range r.Operations {
		operations = append(operations, operation)
	}
	slices.SortFunc(operations, func(a, b Operation) int {
		return cmp.Compare(string(a), string(b))
	})

	return operations
}

// Snapshot is a lightweight progress view.
type Snapshot struct {
	Elapsed           time.Duration     `json:"elapsed"`
	Attempts          uint64            `json:"attempts"`
	Succeeded         uint64            `json:"succeeded"`
	Failed            uint64            `json:"failed"`
	RequestsPerSecond float64           `json:"requestsPerSecond"`
	FailureKinds      map[string]uint64 `json:"failureKinds,omitempty"`
}

// OperationStats contains latency and outcome metrics for one command.
type OperationStats struct {
	Attempts  uint64        `json:"attempts"`
	Succeeded uint64        `json:"succeeded"`
	Failed    uint64        `json:"failed"`
	PingBytes uint64        `json:"pingBytes,omitempty"`
	Average   time.Duration `json:"average"`
	Min       time.Duration `json:"min"`
	P50       time.Duration `json:"p50UpperBound"`
	P95       time.Duration `json:"p95UpperBound"`
	P99       time.Duration `json:"p99UpperBound"`
	Max       time.Duration `json:"max"`
}

// Report is the final result of a stress run.
type Report struct {
	StartedAt         time.Time                    `json:"startedAt"`
	EndedAt           time.Time                    `json:"endedAt"`
	Elapsed           time.Duration                `json:"elapsed"`
	DevicePath        string                       `json:"devicePath"`
	Workers           int                          `json:"workers"`
	ConfiguredOps     []Operation                  `json:"configuredOperations"`
	PingSizes         []int                        `json:"pingSizes,omitempty"`
	CommandTimeout    time.Duration                `json:"commandTimeout"`
	BaselineInfo      InfoSnapshot                 `json:"baselineInfo"`
	Attempts          uint64                       `json:"attempts"`
	Succeeded         uint64                       `json:"succeeded"`
	Failed            uint64                       `json:"failed"`
	RequestsPerSecond float64                      `json:"requestsPerSecond"`
	Opens             uint64                       `json:"opens"`
	OpenFailures      uint64                       `json:"openFailures"`
	CloseFailures     uint64                       `json:"closeFailures"`
	InfoSignature     string                       `json:"infoSignature"`
	Operations        map[Operation]OperationStats `json:"operations"`
	FailureKinds      map[string]uint64            `json:"failureKinds,omitempty"`
	FailureSamples    []FailureSample              `json:"failureSamples,omitempty"`
}
