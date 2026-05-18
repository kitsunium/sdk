// Package baseenc — declares the sentinel *errs.Error values for the
// base-N codec family.
package baseenc

import "github.com/kitsunium/sdk/internal/kernel/errs"

var (
	// BaseEncMarshalFailed wraps a JSON-side failure before base-N encoding.
	BaseEncMarshalFailed = errs.Define(CodeBaseEncMarshalFailed, "BASE_ENC_MARSHAL_FAILED",
		"base-N encoding failed",
		"service/codec/baseenc: encoding/json.Marshal returned an error before the base-N step")

	// BaseEncUnmarshalFailed wraps a JSON-side failure after base-N decoding.
	BaseEncUnmarshalFailed = errs.Define(CodeBaseEncUnmarshalFailed, "BASE_ENC_UNMARSHAL_FAILED",
		"base-N decoding failed",
		"service/codec/baseenc: encoding/json.Unmarshal returned an error after the base-N step")

	// BaseEncDecodeFailed wraps a malformed base-N input rejected by the
	// stdlib decoder.
	BaseEncDecodeFailed = errs.Define(CodeBaseEncDecodeFailed, "BASE_ENC_DECODE_FAILED",
		"base-N decoding failed",
		"service/codec/baseenc: stdlib base-N decoder rejected the input bytes")

	// BaseEncSizeExceeded fires when an Unmarshal input exceeds the 10 MiB cap.
	BaseEncSizeExceeded = errs.Define(CodeBaseEncSizeExceeded, "BASE_ENC_SIZE_EXCEEDED",
		"base-N input exceeds size limit",
		"service/codec/baseenc: len(data) exceeds maxBaseEncBytes (CWE-400 defence)")
)
