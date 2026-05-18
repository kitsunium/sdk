// Package baseenc — declares the sentinel *errs.Error values.
package baseenc

import "github.com/kitsunium/sdk/internal/kernel/errs"

var (
	// InvalidEncoding fires when the caller passes an unknown Encoding.
	InvalidEncoding = errs.Define(CodeInvalidEncoding, "INVALID_ENCODING",
		"baseenc encoding is not supported",
		"pkg/v1/codec/baseenc: Encoding value did not match any stdlib encoding")

	// DecodeFailed wraps a failure from the underlying stdlib decoder.
	DecodeFailed = errs.Define(CodeDecodeFailed, "DECODE_FAILED",
		"baseenc decoding failed",
		"pkg/v1/codec/baseenc: stdlib decoder returned an error")

	// EncodeFailed wraps a failure from the underlying stdlib encoder.
	EncodeFailed = errs.Define(CodeEncodeFailed, "ENCODE_FAILED",
		"baseenc encoding failed",
		"pkg/v1/codec/baseenc: stdlib encoder returned an error")
)
