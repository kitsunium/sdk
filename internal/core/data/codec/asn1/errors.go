// Package asn1 — the sentinel *errs.Error values, one per code. Each
// Reason derives from its var name or from its Code constant (ADR 0020);
// the Public and Private texts are the ones the service package always
// emitted, so moving the declaration changed no rendering.
package asn1

import "github.com/kitsunium/sdk/internal/kernel/errs"

var (
	// MarshalFailed wraps a failure from encoding/asn1.Marshal.
	MarshalFailed = errs.Define(CodeASN1MarshalFailed, "MARSHAL_FAILED",
		"ASN.1 DER encoding failed",
		"service/data/codec/asn1: encoding/asn1.Marshal returned an error")

	// UnmarshalFailed wraps a failure from encoding/asn1.Unmarshal.
	UnmarshalFailed = errs.Define(CodeASN1UnmarshalFailed, "UNMARSHAL_FAILED",
		"ASN.1 DER decoding failed",
		"service/data/codec/asn1: encoding/asn1.Unmarshal returned an error")
)
