// Package flatbuffers — the sentinel *errs.Error values, one per code. Each
// Reason derives from its var name or from its Code constant (ADR 0020);
// the Public and Private texts are the ones the service package always
// emitted, so moving the declaration changed no rendering.
package flatbuffers

import "github.com/kitsunium/sdk/internal/kernel/errs"

var (
	// FlatbuffersBadType fires when Marshal cannot extract a byte payload
	// from the argument (not []byte, not a BytesProvider).
	FlatbuffersBadType = errs.Define(CodeFlatbuffersBadType, "FLATBUFFERS_BAD_TYPE",
		"FlatBuffers codec requires []byte or a BytesProvider value",
		"service/data/codec/flatbuffers: Marshal argument is not []byte and does not implement BytesProvider")

	// FlatbuffersBadTarget fires when Unmarshal cannot publish the payload
	// through the target (not *[]byte, not a BytesAcceptor).
	FlatbuffersBadTarget = errs.Define(CodeFlatbuffersBadTarget, "FLATBUFFERS_BAD_TARGET",
		"FlatBuffers codec requires a *[]byte or BytesAcceptor target",
		"service/data/codec/flatbuffers: Unmarshal target is not *[]byte and does not implement BytesAcceptor")

	// FlatbuffersTruncated fires when the buffer is shorter than the
	// 4-byte root-offset header mandated by the FlatBuffers wire format
	// or larger than the package-level CWE-400 ceiling.
	FlatbuffersTruncated = errs.Define(CodeFlatbuffersTruncated, "FLATBUFFERS_TRUNCATED",
		"FlatBuffers buffer is truncated",
		"service/data/codec/flatbuffers: buffer length is invalid for the FlatBuffers wire format")
)
