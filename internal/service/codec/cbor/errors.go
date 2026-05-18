// Package cbor — declares the sentinel *errs.Error values for CBOR.
package cbor

import "github.com/kitsunium/sdk/internal/kernel/errs"

var (
	// MarshalFailed wraps a failure from fxamacker/cbor/v2.Marshal.
	MarshalFailed = errs.Define(CodeCBORMarshalFailed, "MARSHAL_FAILED",
		"CBOR encoding failed",
		"service/codec/cbor: github.com/fxamacker/cbor/v2.Marshal returned an error")

	// UnmarshalFailed wraps a failure from fxamacker/cbor/v2.Unmarshal.
	UnmarshalFailed = errs.Define(CodeCBORUnmarshalFailed, "UNMARSHAL_FAILED",
		"CBOR decoding failed",
		"service/codec/cbor: github.com/fxamacker/cbor/v2.Unmarshal returned an error")
)
