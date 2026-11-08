package stress

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/telesma-app/ctap/protocol"
)

// Operation identifies one non-mutating authenticator command.
type Operation string

const (
	OperationPing       Operation = "ping"
	OperationGetInfo    Operation = "get-info"
	OperationPINRetries Operation = "pin-retries"
	OperationUVRetries  Operation = "uv-retries"
)

var knownOperations = map[Operation]struct{}{
	OperationPing:       {},
	OperationGetInfo:    {},
	OperationPINRetries: {},
	OperationUVRetries:  {},
}

// Config controls a stress run against one authenticator path.
type Config struct {
	DevicePath      string
	Duration        time.Duration
	Requests        uint64
	Workers         int
	CommandTimeout  time.Duration
	OpenTimeout     time.Duration
	Rate            float64
	Operations      []Operation
	PingSizes       []int
	CheckInfoStable bool
	ReopenOnError   bool
	ReopenDelay     time.Duration
	ProgressEvery   time.Duration
	FailureSamples  int
}

// DefaultConfig returns conservative defaults. Increasing Workers opens one
// independent CTAPHID channel per worker.
func DefaultConfig() Config {
	return Config{
		Duration:        30 * time.Second,
		Workers:         1,
		CommandTimeout:  3 * time.Second,
		OpenTimeout:     5 * time.Second,
		Operations:      []Operation{OperationPing, OperationGetInfo},
		PingSizes:       []int{0, 1, 57, 58, 64, 116, 117, 1024, 7609},
		CheckInfoStable: true,
		ReopenOnError:   true,
		ReopenDelay:     100 * time.Millisecond,
		ProgressEvery:   time.Second,
		FailureSamples:  20,
	}
}

// Validate checks limits which are independent of authenticator capabilities.
func (c Config) Validate() error {
	if c.DevicePath == "" {
		return fmt.Errorf("device path is empty")
	}

	if c.Duration < 0 {
		return fmt.Errorf("duration cannot be negative")
	}

	if c.Duration == 0 && c.Requests == 0 {
		return fmt.Errorf("duration and requests cannot both be zero")
	}

	if c.Workers < 1 {
		return fmt.Errorf("workers must be at least 1")
	}

	if c.CommandTimeout <= 0 {
		return fmt.Errorf("command timeout must be positive")
	}

	if c.OpenTimeout <= 0 {
		return fmt.Errorf("open timeout must be positive")
	}

	if c.Rate < 0 || c.Rate > 1e9 {
		return fmt.Errorf("rate must be between 0 and 1e9 requests/second")
	}

	if len(c.Operations) == 0 {
		return fmt.Errorf("at least one operation is required")
	}

	for _, operation := range c.Operations {
		if _, ok := knownOperations[operation]; !ok {
			return fmt.Errorf("unknown operation %q", operation)
		}
	}

	if containsOperation(c.Operations, OperationPing) && len(c.PingSizes) == 0 {
		return fmt.Errorf("at least one ping size is required")
	}

	for _, size := range c.PingSizes {
		if size < 0 || size > 7609 {
			return fmt.Errorf("ping size %d is outside CTAPHID range 0..7609", size)
		}
	}

	if c.ReopenDelay < 0 {
		return fmt.Errorf("reopen delay cannot be negative")
	}

	if c.ProgressEvery < 0 {
		return fmt.Errorf("progress interval cannot be negative")
	}

	if c.FailureSamples < 0 {
		return fmt.Errorf("failure sample limit cannot be negative")
	}

	return nil
}

// ValidateCapabilities rejects explicitly requested commands which the
// authenticator says it cannot service.
func (c Config) ValidateCapabilities(info protocol.AuthenticatorGetInfoResponse) error {
	if containsOperation(c.Operations, OperationPINRetries) {
		if _, ok := info.Options[protocol.OptionClientPIN]; !ok {
			return fmt.Errorf("operation %q requires the clientPin option", OperationPINRetries)
		}

		if !supportsPinUvAuthProtocol(info.PinUvAuthProtocols) {
			return fmt.Errorf("operation %q requires a reported pinUvAuthProtocol", OperationPINRetries)
		}
	}

	if containsOperation(c.Operations, OperationUVRetries) {
		if _, ok := info.Options[protocol.OptionUserVerification]; !ok {
			return fmt.Errorf("operation %q requires the uv option", OperationUVRetries)
		}

		if !supportsCTAP21(info.Versions) {
			return fmt.Errorf("operation %q requires CTAP 2.1 or newer", OperationUVRetries)
		}
	}

	return nil
}

func supportsPinUvAuthProtocol(protocols []protocol.PinUvAuthProtocol) bool {
	for _, candidate := range protocols {
		if candidate == protocol.PinUvAuthProtocolOne || candidate == protocol.PinUvAuthProtocolTwo {
			return true
		}
	}

	return false
}

func supportsCTAP21(versions protocol.Versions) bool {
	for _, version := range versions {
		switch version {
		case protocol.FIDO_2_1_PRE, protocol.FIDO_2_1, protocol.FIDO_2_3:
			return true
		}
	}

	return false
}

func containsOperation(operations []Operation, want Operation) bool {
	for _, operation := range operations {
		if operation == want {
			return true
		}
	}

	return false
}

// ParseOperations parses a comma-separated operation list and rejects
// duplicates because duplicate weighting is easy to request accidentally.
func ParseOperations(value string) ([]Operation, error) {
	parts := strings.Split(value, ",")
	operations := make([]Operation, 0, len(parts))

	seen := make(map[Operation]struct{}, len(parts))
	for _, part := range parts {
		operation := Operation(strings.TrimSpace(part))
		if _, ok := knownOperations[operation]; !ok {
			return nil, fmt.Errorf("unknown operation %q", operation)
		}

		if _, ok := seen[operation]; ok {
			return nil, fmt.Errorf("duplicate operation %q", operation)
		}

		seen[operation] = struct{}{}
		operations = append(operations, operation)
	}

	return operations, nil
}

// ParsePingSizes parses comma-separated CTAPHID payload sizes.
func ParsePingSizes(value string) ([]int, error) {
	parts := strings.Split(value, ",")

	sizes := make([]int, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			return nil, fmt.Errorf("empty ping size")
		}

		size, err := strconv.Atoi(part)
		if err != nil {
			return nil, fmt.Errorf("parse ping size %q: %w", part, err)
		}

		if size < 0 || size > 7609 {
			return nil, fmt.Errorf("ping size %d is outside CTAPHID range 0..7609", size)
		}

		sizes = append(sizes, size)
	}

	return sizes, nil
}

// CapPingSizes limits CTAPHID_PING payloads to the authenticator's effective
// maxMsgSize. Values that collapse onto the same capped size are de-duplicated
// while preserving the caller's order.
func CapPingSizes(sizes []int, maxMessageSize uint) []int {
	limit := int(min(maxMessageSize, uint(7609)))
	capped := make([]int, 0, len(sizes))

	seen := make(map[int]struct{}, len(sizes))
	for _, size := range sizes {
		size = min(size, limit)
		if _, ok := seen[size]; ok {
			continue
		}

		seen[size] = struct{}{}
		capped = append(capped, size)
	}

	return capped
}
