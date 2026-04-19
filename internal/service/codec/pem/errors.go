// Package pem: errors.go declares the sentinel *errs.Error values for PEM.
package pem

import "github.com/kitsunium/sdk/internal/kernel/errs"

var (
	// MarshalFailed wraps a failure from encoding/pem.Encode.
	MarshalFailed = errs.Define(CodeMarshalFailed, "MARSHAL_FAILED",
		"PEM encoding failed",
		"service/codec/pem: encoding/pem.Encode returned an error")

	// UnmarshalFailed fires when no PEM block could be decoded from the input.
	UnmarshalFailed = errs.Define(CodeUnmarshalFailed, "UNMARSHAL_FAILED",
		"PEM decoding failed",
		"service/codec/pem: encoding/pem.Decode returned no block")

	// ValueInvalid fires when the caller does not pass a *pem.Block / **pem.Block.
	ValueInvalid = errs.Define(CodeValueInvalid, "VALUE_INVALID",
		"PEM codec requires a *pem.Block value",
		"service/codec/pem: Marshal/Unmarshal argument is not *pem.Block")
)
