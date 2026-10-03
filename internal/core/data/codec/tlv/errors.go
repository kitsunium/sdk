// Package tlv — the sentinel *errs.Error values, one per code. Each
// Reason derives from its var name or from its Code constant (ADR 0020);
// the Public and Private texts are the ones the service package always
// emitted, so moving the declaration changed no rendering.
package tlv

import "github.com/kitsunium/sdk/internal/kernel/errs"

var (
	// MarshalFailed wraps an encode-side failure in the TLV codec.
	MarshalFailed = errs.Define(CodeTLVMarshalFailed, "MARSHAL_FAILED",
		"TLV encoding failed",
		"service/data/codec/tlv: Marshal/Append/Encode failed during reflection-driven encode")

	// UnmarshalFailed wraps a decode-side failure in the TLV codec.
	UnmarshalFailed = errs.Define(CodeTLVUnmarshalFailed, "UNMARSHAL_FAILED",
		"TLV decoding failed",
		"service/data/codec/tlv: Unmarshal/Decode failed (malformed record, type mismatch, reader error)")

	// UnsupportedType fires for values the TLV format cannot represent
	// (chan, func, complex*, unsafe.Pointer).
	UnsupportedType = errs.Define(CodeTLVUnsupportedType, "UNSUPPORTED_TYPE",
		"TLV codec cannot encode this type",
		"service/data/codec/tlv: reflect.Kind is not representable in the TLV wire format")

	// DepthExceeded fires when a nested value exceeds the 32-level cap.
	DepthExceeded = errs.Define(CodeTLVDepthExceeded, "DEPTH_EXCEEDED",
		"TLV nesting depth exceeds limit",
		"service/data/codec/tlv: nesting depth > maxTLVDepth (CWE-674 defence)")

	// SizeExceeded fires when an Unmarshal input exceeds the 10 MiB cap.
	SizeExceeded = errs.Define(CodeTLVSizeExceeded, "SIZE_EXCEEDED",
		"TLV input exceeds size limit",
		"service/data/codec/tlv: len(data) exceeds maxTLVBytes (CWE-400 defence)")

	// Truncated fires when a buffer ends mid-record.
	Truncated = errs.Define(CodeTLVTruncated, "TRUNCATED",
		"TLV buffer truncated",
		"service/data/codec/tlv: buffer ended before the declared record length was satisfied")
)
