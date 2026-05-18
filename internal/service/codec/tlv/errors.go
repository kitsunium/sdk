// Package tlv — declares the sentinel *errs.Error values for TLV.
package tlv

import "github.com/kitsunium/sdk/internal/kernel/errs"

var (
	// MarshalFailed wraps an encode-side failure in the TLV codec.
	MarshalFailed = errs.Define(CodeTLVMarshalFailed, "MARSHAL_FAILED",
		"TLV encoding failed",
		"service/codec/tlv: Marshal/Append/Encode failed during reflection-driven encode")

	// UnmarshalFailed wraps a decode-side failure in the TLV codec.
	UnmarshalFailed = errs.Define(CodeTLVUnmarshalFailed, "UNMARSHAL_FAILED",
		"TLV decoding failed",
		"service/codec/tlv: Unmarshal/Decode failed (malformed record, type mismatch, reader error)")

	// UnsupportedType fires for values the TLV format cannot represent
	// (chan, func, complex*, unsafe.Pointer).
	UnsupportedType = errs.Define(CodeTLVUnsupportedType, "UNSUPPORTED_TYPE",
		"TLV codec cannot encode this type",
		"service/codec/tlv: reflect.Kind is not representable in the TLV wire format")

	// DepthExceeded fires when a nested value exceeds the 32-level cap.
	DepthExceeded = errs.Define(CodeTLVDepthExceeded, "DEPTH_EXCEEDED",
		"TLV nesting depth exceeds limit",
		"service/codec/tlv: nesting depth > maxTLVDepth (CWE-674 defence)")

	// SizeExceeded fires when an Unmarshal input exceeds the 10 MiB cap.
	SizeExceeded = errs.Define(CodeTLVSizeExceeded, "SIZE_EXCEEDED",
		"TLV input exceeds size limit",
		"service/codec/tlv: len(data) exceeds maxTLVBytes (CWE-400 defence)")

	// Truncated fires when a buffer ends mid-record.
	Truncated = errs.Define(CodeTLVTruncated, "TRUNCATED",
		"TLV buffer truncated",
		"service/codec/tlv: buffer ended before the declared record length was satisfied")
)
