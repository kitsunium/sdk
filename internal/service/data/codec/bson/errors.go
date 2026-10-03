// Package bson — declares the sentinel *errs.Error values for BSON, and the
// constructors that give each failure its own log-only detail.
package bson

import (
	"strconv"

	"github.com/kitsunium/sdk/internal/kernel/errs"
)

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

// marshalError builds the encode failure, detail naming what was refused.
// detail never carries a value: it names Go types, struct fields and BSON
// types only, so a log line cannot leak the data being encoded.
func marshalError(cause error, detail string) error {
	//: a cause that is already an SDK error keeps its own code (origin wins).
	return errs.Wrap(cause, errs.WrapParams{
		Code:    CodeBSONMarshalFailed,
		Reason:  "BSON_MARSHAL_FAILED",
		Public:  "BSON encoding failed",
		Private: "service/data/codec/bson.Marshal: " + detail,
	})
}

// unmarshalError builds the decode failure. detail names offsets, BSON types,
// Go types and declared field names — never a key or a value read from the
// input, which is a stranger's text and does not belong in a log line.
func unmarshalError(cause error, detail string) error {
	//: a cause that is already an SDK error keeps its own code (origin wins).
	return errs.Wrap(cause, errs.WrapParams{
		Code:    CodeBSONUnmarshalFailed,
		Reason:  "BSON_UNMARSHAL_FAILED",
		Public:  "BSON decoding failed",
		Private: "service/data/codec/bson.Unmarshal: " + detail,
	})
}

// depthError builds the nesting refusal for op ("Marshal" or "Unmarshal").
func depthError(op string) error {
	//: one message per direction, the bound named.
	return errs.Wrap(nil, errs.WrapParams{
		Code:    CodeBSONDepthExceeded,
		Reason:  "BSON_DEPTH_EXCEEDED",
		Public:  "BSON nesting depth exceeds limit",
		Private: "service/data/codec/bson." + op + ": nesting exceeds " + strconv.Itoa(maxBSONNestedLevels) + " levels",
	})
}

// valueError builds the refusal of a value type's constructor or parser.
func valueError(cause error, detail string) error {
	//: detail names the type and the shape expected, never the input.
	return errs.Wrap(cause, errs.WrapParams{
		Code:    CodeBSONValueInvalid,
		Reason:  "BSON_VALUE_INVALID",
		Public:  "BSON value is invalid",
		Private: "service/data/codec/bson: " + detail,
	})
}
