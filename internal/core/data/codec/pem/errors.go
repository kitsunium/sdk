// Package pem — the sentinel *errs.Error values, one per code. Each
// Reason derives from its var name or from its Code constant (ADR 0020);
// the Public and Private texts are the ones the service package always
// emitted, so moving the declaration changed no rendering.
package pem

import "github.com/kitsunium/sdk/internal/kernel/errs"

var (
	// MarshalFailed wraps a failure from encoding/pem.Encode.
	MarshalFailed = errs.Define(CodePEMMarshalFailed, "MARSHAL_FAILED",
		"PEM encoding failed",
		"service/data/codec/pem: encoding/pem.Encode returned an error")

	// UnmarshalFailed fires when no PEM block could be decoded from the input.
	UnmarshalFailed = errs.Define(CodePEMUnmarshalFailed, "UNMARSHAL_FAILED",
		"PEM decoding failed",
		"service/data/codec/pem: encoding/pem.Decode returned no block")

	// ValueInvalid fires when the caller does not pass a *pem.Block / **pem.Block.
	ValueInvalid = errs.Define(CodePEMValueInvalid, "VALUE_INVALID",
		"PEM codec requires a *pem.Block value",
		"service/data/codec/pem: Marshal/Unmarshal argument is not *pem.Block")
)
