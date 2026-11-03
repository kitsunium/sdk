package bson

import (
	"reflect"
	"strconv"

	"github.com/kitsunium/sdk/internal/core/data/codec"
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

// checkTarget is core/data/codec.CheckTarget, except that a non-nil map is a
// target too: the root document fills it in place (#1101).
func checkTarget(v any) error {
	//: a map the decoder fills in place.
	if rv := reflect.ValueOf(v); rv.Kind() == reflect.Map && !rv.IsNil() {
		return nil
	}
	//: anything else must be a non-nil pointer.
	return codec.CheckTarget(v)
}

// malformedAt builds the decode failure of a document malformed at byte off
// of the input: the rule it breaks in the Private text, the offset as the
// family's field codec.PositionOf reads (#1125) — never in the text alone.
func malformedAt(off int, rule string) error {
	//: the offset is a field, the rule a fixed sentence.
	return errs.Wrap(nil, errs.WrapParams{
		Code:    corebson.CodeBSONUnmarshalFailed,
		Reason:  "BSON_UNMARSHAL_FAILED",
		Public:  "BSON decoding failed",
		Private: "service/data/codec/bson.Unmarshal: malformed document: " + rule,
	}, codec.PositionFields(codec.PositionValue{Offset: off})...)
}

// depthError builds the nesting refusal for op ("Marshal" or "Unmarshal").
func depthError(op string) error {
	//: one message per direction, the bound named.
	return errs.Wrap(nil, errs.WrapParams{
		Code:    corebson.CodeBSONDepthExceeded,
		Reason:  "DEPTH_EXCEEDED",
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
