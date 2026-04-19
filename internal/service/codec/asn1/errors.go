// Package asn1: errors.go declares the sentinel *errs.Error values for DER.
package asn1

import "github.com/kitsunium/sdk/internal/kernel/errs"

var (
	// MarshalFailed wraps a failure from encoding/asn1.Marshal.
	MarshalFailed = errs.Define(CodeMarshalFailed, "MARSHAL_FAILED",
		"ASN.1 DER encoding failed",
		"service/codec/asn1: encoding/asn1.Marshal returned an error")

	// UnmarshalFailed wraps a failure from encoding/asn1.Unmarshal.
	UnmarshalFailed = errs.Define(CodeUnmarshalFailed, "UNMARSHAL_FAILED",
		"ASN.1 DER decoding failed",
		"service/codec/asn1: encoding/asn1.Unmarshal returned an error")
)
