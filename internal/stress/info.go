package stress

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"

	"github.com/telesma-app/ctap/attestation"
	"github.com/telesma-app/ctap/credential"
	"github.com/telesma-app/ctap/extension"
	"github.com/telesma-app/ctap/protocol"
)

// InfoSnapshot is the stable capability portion of authenticatorGetInfo.
// Volatile counters and encrypted state fields are deliberately excluded.
type InfoSnapshot struct {
	Versions                      protocol.Versions                                  `json:"versions"`
	Extensions                    []extension.ExtensionIdentifier                    `json:"extensions,omitempty"`
	AAGUID                        string                                             `json:"aaguid"`
	Options                       map[string]bool                                    `json:"options,omitempty"`
	MaxMsgSize                    uint                                               `json:"maxMsgSize,omitempty"`
	PinUvAuthProtocols            []protocol.PinUvAuthProtocol                       `json:"pinUvAuthProtocols,omitempty"`
	MaxCredentialCountInList      uint                                               `json:"maxCredentialCountInList,omitempty"`
	MaxCredentialIDLength         uint                                               `json:"maxCredentialIdLength,omitempty"`
	Transports                    []credential.AuthenticatorTransport                `json:"transports,omitempty"`
	Algorithms                    []credential.PublicKeyCredentialParameters         `json:"algorithms,omitempty"`
	MaxSerializedLargeBlobArray   uint                                               `json:"maxSerializedLargeBlobArray,omitempty"`
	ForcePINChange                bool                                               `json:"forcePINChange,omitempty"`
	MinPINLength                  uint                                               `json:"minPINLength,omitempty"`
	FirmwareVersion               *uint                                              `json:"firmwareVersion,omitempty"`
	MaxCredBlobLength             uint                                               `json:"maxCredBlobLength,omitempty"`
	MaxRPIDsForSetMinPINLength    *uint                                              `json:"maxRPIDsForSetMinPINLength,omitempty"`
	PreferredPlatformUvAttempts   uint                                               `json:"preferredPlatformUvAttempts,omitempty"`
	UvModality                    *protocol.UserVerify                               `json:"uvModality,omitempty"`
	Certifications                map[string]uint64                                  `json:"certifications,omitempty"`
	VendorPrototypeConfigCommands []protocol.VendorCommandID                         `json:"vendorPrototypeConfigCommands,omitempty"`
	AttestationFormats            []attestation.AttestationStatementFormatIdentifier `json:"attestationFormats,omitempty"`
	LongTouchForReset             *bool                                              `json:"longTouchForReset,omitempty"`
	TransportsForReset            []credential.AuthenticatorTransport                `json:"transportsForReset,omitempty"`
	PinComplexityPolicy           *bool                                              `json:"pinComplexityPolicy,omitempty"`
	PinComplexityPolicyURL        string                                             `json:"pinComplexityPolicyURL,omitempty"`
	MaxPINLength                  uint                                               `json:"maxPINLength,omitempty"`
	AuthenticatorConfigCommands   []protocol.ConfigSubCommand                        `json:"authenticatorConfigCommands,omitempty"`
}

// SnapshotInfo extracts fields expected to remain stable during a read-only run.
func SnapshotInfo(info protocol.AuthenticatorGetInfoResponse) InfoSnapshot {
	options := make(map[string]bool, len(info.Options))
	for key, value := range info.Options {
		options[string(key)] = value
	}

	return InfoSnapshot{
		Versions:                      info.Versions,
		Extensions:                    info.Extensions,
		AAGUID:                        info.AAGUID.String(),
		Options:                       options,
		MaxMsgSize:                    info.MaxMsgSize,
		PinUvAuthProtocols:            info.PinUvAuthProtocols,
		MaxCredentialCountInList:      info.MaxCredentialCountInList,
		MaxCredentialIDLength:         info.MaxCredentialIdLength,
		Transports:                    info.Transports,
		Algorithms:                    info.Algorithms,
		MaxSerializedLargeBlobArray:   info.MaxSerializedLargeBlobArray,
		ForcePINChange:                info.ForcePINChange,
		MinPINLength:                  info.MinPINLength,
		FirmwareVersion:               info.FirmwareVersion,
		MaxCredBlobLength:             info.MaxCredBlobLength,
		MaxRPIDsForSetMinPINLength:    info.MaxRPIDsForSetMinPINLength,
		PreferredPlatformUvAttempts:   info.PreferredPlatformUvAttempts,
		UvModality:                    info.UvModality,
		Certifications:                info.Certifications,
		VendorPrototypeConfigCommands: info.VendorPrototypeConfigCommands,
		AttestationFormats:            info.AttestationFormats,
		LongTouchForReset:             info.LongTouchForReset,
		TransportsForReset:            info.TransportsForReset,
		PinComplexityPolicy:           info.PinComplexityPolicy,
		PinComplexityPolicyURL:        info.PinComplexityPolicyURLString(),
		MaxPINLength:                  info.MaxPINLength,
		AuthenticatorConfigCommands:   info.AuthenticatorConfigCommands,
	}
}

// Signature returns a deterministic digest of the stable GetInfo fields.
func (s InfoSnapshot) Signature() (string, error) {
	encoded, err := json.Marshal(s)
	if err != nil {
		return "", fmt.Errorf("marshal GetInfo snapshot: %w", err)
	}

	digest := sha256.Sum256(encoded)

	return hex.EncodeToString(digest[:]), nil
}
