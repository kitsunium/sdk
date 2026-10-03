// Package bson — the constructors that give each failure its own log-only
// detail. The codes and the sentinels they wrap are declared in
// internal/core/data/codec/bson (ADR 0160).
package bson

import (
	"strconv"

	corebson "github.com/kitsunium/sdk/internal/core/data/codec/bson"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// marshalError builds the encode failure, detail naming what was refused.
// detail never carries a value: it names Go types, struct fields and BSON
// types only, so a log line cannot leak the data being encoded.
func marshalError(cause error, detail string) error {
	//: a cause that is already an SDK error keeps its own code (origin wins).
	return errs.Wrap(cause, errs.WrapParams{
		Code:    corebson.CodeBSONMarshalFailed,
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
		Code:    corebson.CodeBSONUnmarshalFailed,
		Reason:  "BSON_UNMARSHAL_FAILED",
		Public:  "BSON decoding failed",
		Private: "service/data/codec/bson.Unmarshal: " + detail,
	})
}

// depthError builds the nesting refusal for op ("Marshal" or "Unmarshal").
func depthError(op string) error {
	//: one message per direction, the bound named.
	return errs.Wrap(nil, errs.WrapParams{
		Code:    corebson.CodeBSONDepthExceeded,
		Reason:  "BSON_DEPTH_EXCEEDED",
		Public:  "BSON nesting depth exceeds limit",
		Private: "service/data/codec/bson." + op + ": nesting exceeds " + strconv.Itoa(maxBSONNestedLevels) + " levels",
	})
}

// valueError builds the refusal of a value type's constructor or parser.
func valueError(cause error, detail string) error {
	//: detail names the type and the shape expected, never the input.
	return errs.Wrap(cause, errs.WrapParams{
		Code:    corebson.CodeBSONValueInvalid,
		Reason:  "BSON_VALUE_INVALID",
		Public:  "BSON value is invalid",
		Private: "service/data/codec/bson: " + detail,
	})
}
