// Package xml — the sentinel *errs.Error values, one per code. Each
// Reason derives from its var name or from its Code constant (ADR 0020);
// the Public and Private texts are the ones the service package always
// emitted, so moving the declaration changed no rendering.
package xml

import "github.com/kitsunium/sdk/internal/kernel/errs"

var (
	// MarshalFailed wraps a failure from encoding/xml.Marshal.
	MarshalFailed = errs.Define(CodeXMLMarshalFailed, "MARSHAL_FAILED",
		"XML encoding failed",
		"service/data/codec/xml: encoding/xml.Marshal returned an error")

	// UnmarshalFailed wraps a failure from encoding/xml.Unmarshal.
	UnmarshalFailed = errs.Define(CodeXMLUnmarshalFailed, "UNMARSHAL_FAILED",
		"XML decoding failed",
		"service/data/codec/xml: encoding/xml.Unmarshal returned an error")
)
