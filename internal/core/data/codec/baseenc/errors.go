// Package baseenc — the sentinel *errs.Error values, one per code. Each
// Reason derives from its var name or from its Code constant (ADR 0020);
// the Public and Private texts are the ones the service package always
// emitted, so moving the declaration changed no rendering.
package baseenc

import "github.com/kitsunium/sdk/internal/kernel/errs"

var (
	// BaseEncMarshalFailed wraps a JSON-side failure before base-N encoding.
	BaseEncMarshalFailed = errs.Define(CodeBaseEncMarshalFailed, "BASE_ENC_MARSHAL_FAILED",
		"base-N encoding failed",
		"service/data/codec/baseenc: encoding/json.Marshal returned an error before the base-N step")

	// BaseEncUnmarshalFailed wraps a JSON-side failure after base-N decoding.
	BaseEncUnmarshalFailed = errs.Define(CodeBaseEncUnmarshalFailed, "BASE_ENC_UNMARSHAL_FAILED",
		"base-N decoding failed",
		"service/data/codec/baseenc: encoding/json.Unmarshal returned an error after the base-N step")

	// BaseEncDecodeFailed wraps a malformed base-N input rejected by the
	// stdlib decoder.
	BaseEncDecodeFailed = errs.Define(CodeBaseEncDecodeFailed, "BASE_ENC_DECODE_FAILED",
		"base-N decoding failed",
		"service/data/codec/baseenc: stdlib base-N decoder rejected the input bytes")

	// BaseEncSizeExceeded fires when an Unmarshal input exceeds the 10 MiB cap.
	BaseEncSizeExceeded = errs.Define(CodeBaseEncSizeExceeded, "BASE_ENC_SIZE_EXCEEDED",
		"base-N input exceeds size limit",
		"service/data/codec/baseenc: len(data) exceeds maxBaseEncBytes (CWE-400 defence)")
)
