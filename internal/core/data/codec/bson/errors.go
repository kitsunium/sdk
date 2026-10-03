// Package bson — the sentinel *errs.Error values, one per code. Each
// Reason derives from its var name or from its Code constant (ADR 0020);
// the Public and Private texts are the ones the service package always
// emitted, so moving the declaration changed no rendering.
package bson

import "github.com/kitsunium/sdk/internal/kernel/errs"

var (
	// MarshalFailed marks a value Marshal or Append cannot encode.
	MarshalFailed = errs.Define(CodeBSONMarshalFailed, "BSON_MARSHAL_FAILED",
		"BSON encoding failed",
		"service/data/codec/bson: the value has no BSON encoding")

	// UnmarshalFailed marks input Unmarshal refuses.
	UnmarshalFailed = errs.Define(CodeBSONUnmarshalFailed, "BSON_UNMARSHAL_FAILED",
		"BSON decoding failed",
		"service/data/codec/bson: the input is not a well-formed BSON document the target can hold")

	// SizeExceeded marks an Unmarshal input over the 10 MiB hard cap.
	SizeExceeded = errs.Define(CodeBSONSizeExceeded, "BSON_SIZE_EXCEEDED",
		"BSON input exceeds size limit",
		"service/data/codec/bson: len(data) exceeds maxBSONBytes")

	// DepthExceeded marks a document or value nested deeper than the codec
	// reads or writes.
	DepthExceeded = errs.Define(CodeBSONDepthExceeded, "BSON_DEPTH_EXCEEDED",
		"BSON nesting depth exceeds limit",
		"service/data/codec/bson: nesting exceeds maxBSONNestedLevels")

	// ValueInvalid marks a BSON value type that cannot be built from its input.
	ValueInvalid = errs.Define(CodeBSONValueInvalid, "BSON_VALUE_INVALID",
		"BSON value is invalid",
		"service/data/codec/bson: the input does not describe a value of this BSON type")
)
