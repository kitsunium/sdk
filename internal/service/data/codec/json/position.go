package json

import (
	stdjson "encoding/json"
	"encoding/json/jsontext"
	jsonv2 "encoding/json/v2"
	"errors"
	"math"

	"github.com/kitsunium/sdk/internal/core/data/codec"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// position returns the byte offset encoding/json's error gives err, as the
// family's field codec.PositionOf reads (#1125): the bytes read when the
// decoder stopped, which encoding/json reports for a syntax error and for a
// value its target cannot hold, and json/v2 for both of its own. Any other
// failure — a reader's, a nil target — is placed nowhere.
func position(err error) []errs.FieldValue {
	//: the v1 errors, which json/v2 also returns under the v1 options.
	if se, ok := errors.AsType[*stdjson.SyntaxError](err); ok {
		return offsetField(se.Offset)
	}
	//: a value its target cannot hold.
	if te, ok := errors.AsType[*stdjson.UnmarshalTypeError](err); ok {
		return offsetField(te.Offset)
	}
	//: json/v2's syntax error, under its own options.
	if sy, ok := errors.AsType[*jsontext.SyntacticError](err); ok {
		return offsetField(sy.ByteOffset)
	}
	//: json/v2's semantic error, when it knows where.
	if sem, ok := errors.AsType[*jsonv2.SemanticError](err); ok && sem.ByteOffset > 0 {
		return offsetField(sem.ByteOffset)
	}
	//: nowhere to place it.
	return nil
}

// offsetField is the family's offset field for an offset the standard
// library counts in int64. A stream's offset can pass what an int holds on a
// 32-bit platform: that refusal is placed nowhere rather than wrongly.
func offsetField(off int64) []errs.FieldValue {
	//: past an int: unknown, not wrapped around.
	if off > math.MaxInt {
		return nil
	}
	//: the 0-based byte.
	return codec.PositionFields(codec.PositionValue{Offset: int(off)})
}
