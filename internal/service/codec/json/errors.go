// Package json — declares the sentinel *errs.Error values used
// to wrap failures from encoding/json.
package json

import "github.com/kitsunium/sdk/internal/kernel/errs"

var (
	// MarshalFailed wraps a failure from encoding/json.Marshal.
	MarshalFailed = errs.Define(CodeJSONMarshalFailed, "MARSHAL_FAILED",
		"JSON encoding failed",
		"service/codec/json: encoding/json.Marshal returned an error")

	// UnmarshalFailed wraps a failure from encoding/json.Unmarshal.
	UnmarshalFailed = errs.Define(CodeJSONUnmarshalFailed, "UNMARSHAL_FAILED",
		"JSON decoding failed",
		"service/codec/json: encoding/json.Unmarshal returned an error")
)
