// Package xml — declares the sentinel *errs.Error values for XML.
package xml

import "github.com/kitsunium/sdk/internal/kernel/errs"

var (
	// MarshalFailed wraps a failure from encoding/xml.Marshal.
	MarshalFailed = errs.Define(CodeXMLMarshalFailed, "MARSHAL_FAILED",
		"XML encoding failed",
		"service/codec/xml: encoding/xml.Marshal returned an error")

	// UnmarshalFailed wraps a failure from encoding/xml.Unmarshal.
	UnmarshalFailed = errs.Define(CodeXMLUnmarshalFailed, "UNMARSHAL_FAILED",
		"XML decoding failed",
		"service/codec/xml: encoding/xml.Unmarshal returned an error")
)
