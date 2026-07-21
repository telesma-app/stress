package stress

import (
	"testing"

	"github.com/go-ctap/ctap/protocol"
)

func TestParseOperations(t *testing.T) {
	t.Parallel()

	operations, err := ParseOperations("ping, get-info,pin-retries")
	if err != nil {
		t.Fatalf("ParseOperations: %v", err)
	}
	want := []Operation{OperationPing, OperationGetInfo, OperationPINRetries}
	if len(operations) != len(want) {
		t.Fatalf("operations = %v, want %v", operations, want)
	}
	for index := range want {
		if operations[index] != want[index] {
			t.Fatalf("operations = %v, want %v", operations, want)
		}
	}

	if _, err := ParseOperations("ping,ping"); err == nil {
		t.Fatal("duplicate operation was accepted")
	}
	if _, err := ParseOperations("reset"); err == nil {
		t.Fatal("mutating/unknown operation was accepted")
	}
}

func TestParsePingSizes(t *testing.T) {
	t.Parallel()

	sizes, err := ParsePingSizes("0, 57,58,7609")
	if err != nil {
		t.Fatalf("ParsePingSizes: %v", err)
	}
	want := []int{0, 57, 58, 7609}
	for index := range want {
		if sizes[index] != want[index] {
			t.Fatalf("sizes = %v, want %v", sizes, want)
		}
	}
	if _, err := ParsePingSizes("7610"); err == nil {
		t.Fatal("oversized ping was accepted")
	}
}

func TestCapPingSizesUsesAuthenticatorMaximum(t *testing.T) {
	t.Parallel()

	sizes := []int{0, 1, 57, 58, 1024, 4096, 7609}
	got := CapPingSizes(sizes, 4096)
	want := []int{0, 1, 57, 58, 1024, 4096}
	if len(got) != len(want) {
		t.Fatalf("CapPingSizes() = %v, want %v", got, want)
	}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("CapPingSizes() = %v, want %v", got, want)
		}
	}
	if sizes[len(sizes)-1] != 7609 {
		t.Fatalf("CapPingSizes mutated input: %v", sizes)
	}
}

func TestCapPingSizesDeduplicatesCollapsedBoundaries(t *testing.T) {
	t.Parallel()

	got := CapPingSizes([]int{0, 1, 57, 58, 64, 116, 117, 1024, 7609}, 57)
	want := []int{0, 1, 57}
	if len(got) != len(want) {
		t.Fatalf("CapPingSizes() = %v, want %v", got, want)
	}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("CapPingSizes() = %v, want %v", got, want)
		}
	}
}

func TestValidateCapabilities(t *testing.T) {
	t.Parallel()

	config := DefaultConfig()
	config.DevicePath = "/dev/fake"
	config.Operations = []Operation{OperationPINRetries, OperationUVRetries}
	info := protocol.AuthenticatorGetInfoResponse{
		Versions:           protocol.Versions{protocol.FIDO_2_1},
		PinUvAuthProtocols: []protocol.PinUvAuthProtocol{protocol.PinUvAuthProtocolTwo},
		Options: map[protocol.Option]bool{
			protocol.OptionClientPIN:        false,
			protocol.OptionUserVerification: true,
		},
	}
	if err := config.ValidateCapabilities(info); err != nil {
		t.Fatalf("ValidateCapabilities: %v", err)
	}

	delete(info.Options, protocol.OptionUserVerification)
	if err := config.ValidateCapabilities(info); err == nil {
		t.Fatal("missing uv capability was accepted")
	}
}
