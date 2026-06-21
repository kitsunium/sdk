// Package hcl — declares the sentinel *errs.Error values for the HCL codec.
package hcl

import "github.com/kitsunium/sdk/internal/kernel/errs"

var (
	// MarshalFailed wraps an HCL encode failure (gohcl.EncodeIntoBody).
	MarshalFailed = errs.Define(CodeHCLMarshalFailed, "HCL_MARSHAL_FAILED",
		"HCL encoding failed",
		"third-party/codec/hcl: gohcl.EncodeIntoBody failed or the top-level value is not a struct")

	// UnmarshalFailed wraps an HCL parse/decode failure (hclsimple.Decode).
	UnmarshalFailed = errs.Define(CodeHCLUnmarshalFailed, "HCL_UNMARSHAL_FAILED",
		"HCL decoding failed",
		"third-party/codec/hcl: hclsimple.Decode returned diagnostics")

	// SizeExceeded marks an Unmarshal input over the 10 MiB hard cap.
	SizeExceeded = errs.Define(CodeHCLSizeExceeded, "HCL_SIZE_EXCEEDED",
		"HCL input exceeds size limit",
		"third-party/codec/hcl: len(data) exceeds maxHCLBytes")
)
