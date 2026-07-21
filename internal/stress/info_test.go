package stress

import (
	"testing"

	"github.com/go-ctap/ctap/protocol"
	"github.com/google/uuid"
)

func TestInfoSignatureIgnoresVolatileFields(t *testing.T) {
	t.Parallel()

	remaining := uint(10)
	uvCount := uint(2)
	info := protocol.AuthenticatorGetInfoResponse{
		Versions:                         protocol.Versions{protocol.FIDO_2_1},
		AAGUID:                           uuid.MustParse("00112233-4455-6677-8899-aabbccddeeff"),
		Options:                          map[protocol.Option]bool{protocol.OptionClientPIN: true},
		RemainingDiscoverableCredentials: &remaining,
		UvCountSinceLastPinEntry:         &uvCount,
		EncIdentifier:                    []byte{1, 2, 3},
		EncCredStoreState:                []byte{4, 5, 6},
	}
	want, err := SnapshotInfo(info).Signature()
	if err != nil {
		t.Fatalf("Signature: %v", err)
	}

	remaining = 9
	uvCount = 3
	info.EncIdentifier = []byte{7}
	info.EncCredStoreState = []byte{8}
	got, err := SnapshotInfo(info).Signature()
	if err != nil {
		t.Fatalf("Signature: %v", err)
	}
	if got != want {
		t.Fatalf("volatile fields changed signature: got %s, want %s", got, want)
	}
}

func TestInfoSignatureDetectsCapabilityDrift(t *testing.T) {
	t.Parallel()

	info := protocol.AuthenticatorGetInfoResponse{
		Versions: protocol.Versions{protocol.FIDO_2_1},
		Options:  map[protocol.Option]bool{protocol.OptionClientPIN: true},
	}
	want, err := SnapshotInfo(info).Signature()
	if err != nil {
		t.Fatalf("Signature: %v", err)
	}
	info.Options[protocol.OptionClientPIN] = false
	got, err := SnapshotInfo(info).Signature()
	if err != nil {
		t.Fatalf("Signature: %v", err)
	}
	if got == want {
		t.Fatal("capability drift did not change signature")
	}
}
