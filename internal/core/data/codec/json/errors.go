// Package json — the sentinel *errs.Error values, one per code. Each
// Reason derives from its var name or from its Code constant (ADR 0020);
// the Public and Private texts are the ones the service package always
// emitted, so moving the declaration changed no rendering.
package json

import "github.com/kitsunium/sdk/internal/kernel/errs"

var (
	// MarshalFailed wraps a failure from encoding/json.Marshal.
	MarshalFailed = errs.Define(CodeJSONMarshalFailed, "MARSHAL_FAILED",
		"JSON encoding failed",
		"service/data/codec/json: encoding/json.Marshal returned an error")

	// UnmarshalFailed wraps a failure from encoding/json.Unmarshal.
	UnmarshalFailed = errs.Define(CodeJSONUnmarshalFailed, "UNMARSHAL_FAILED",
		"JSON decoding failed",
		"service/data/codec/json: encoding/json.Unmarshal returned an error")
)
