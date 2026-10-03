// Package msgpack — how a call fails. Every failure is one of the two codes
// this package has always owned: MARSHAL_FAILED (0.3.7.1) for an encode and
// UNMARSHAL_FAILED (0.3.7.2) for a decode, so a caller routing on the reason
// keeps working whatever the cause. What went wrong is said in Private, which
// is log-only, and in Fields — offsets, Go type names, limits — and never by
// quoting the input: a decode failure names where and what, not the bytes.
package msgpack

import (
	"reflect"

	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// Reasons and public messages, shared by every failure of one direction.
const (
	// reasonMarshal is the reason every encode failure carries.
	reasonMarshal string = "MARSHAL_FAILED"
	// reasonUnmarshal is the reason every decode failure carries.
	reasonUnmarshal string = "UNMARSHAL_FAILED"
	// publicMarshal is the wire-safe message of an encode failure.
	publicMarshal string = "MessagePack encoding failed"
	// publicUnmarshal is the wire-safe message of a decode failure.
	publicUnmarshal string = "MessagePack decoding failed"
	// privatePrefix opens every log-only message.
	privatePrefix string = "service/data/codec/msgpack: "
)

// Field keys attached to failures.
const (
	// fieldOffset is the byte offset in the input where a decode failed.
	fieldOffset string = "offset"
	// fieldType is the Go type the codec was encoding or decoding into.
	fieldType string = "type"
	// fieldWire is the MessagePack family the input carried.
	fieldWire string = "wire"
	// fieldLimit is the bound a value exceeded.
	fieldLimit string = "limit"
	// fieldLen is a length the input declared or the value had.
	fieldLen string = "len"
	// fieldName is a struct field name.
	fieldName string = "field"
)

// familyNames is what a failure calls each family.
var familyNames = [...]string{
	famInvalid: "never-used byte 0xc1",
	famNil:     "nil",
	famBool:    "boolean",
	famInt:     "integer",
	famUint:    "unsigned integer",
	famFloat32: "float 32",
	famFloat64: "float 64",
	famStr:     "string",
	famBin:     "binary",
	famArray:   "array",
	famMap:     "map",
	famExt:     "extension",
}

// String names the family for a failure's fields.
func (f family) String() string {
	//: the table covers every family the header table can produce.
	return familyNames[f]
}

// marshalFault is the MARSHAL_FAILED error for an encode failure the codec
// detected itself.
func marshalFault(detail string, fields ...errs.FieldValue) error {
	//: a nil cause makes this code the origin.
	return errs.Wrap(nil, errs.WrapParams{
		Code:    CodeMsgPackMarshalFailed,
		Reason:  reasonMarshal,
		Public:  publicMarshal,
		Private: privatePrefix + detail,
	}, fields...)
}

// unmarshalFault is the UNMARSHAL_FAILED error for a decode failure the codec
// detected itself.
func unmarshalFault(detail string, fields ...errs.FieldValue) error {
	//: a nil cause makes this code the origin.
	return errs.Wrap(nil, errs.WrapParams{
		Code:    CodeMsgPackUnmarshalFailed,
		Reason:  reasonUnmarshal,
		Public:  publicUnmarshal,
		Private: privatePrefix + detail,
	}, fields...)
}

// wrapMarshal attaches MARSHAL_FAILED to a cause the codec did not produce —
// a writer's failure, or a marshaler method's. An SDK error as the cause keeps
// its own code (origin wins).
func wrapMarshal(cause error, detail string, fields ...errs.FieldValue) error {
	//: origin-wins wrap; a stdlib cause gets this code.
	return errs.Wrap(cause, errs.WrapParams{
		Code:    CodeMsgPackMarshalFailed,
		Reason:  reasonMarshal,
		Public:  publicMarshal,
		Private: privatePrefix + detail,
	}, fields...)
}

// wrapUnmarshal attaches UNMARSHAL_FAILED to a cause the codec did not
// produce — a reader's failure, or an unmarshaler method's.
func wrapUnmarshal(cause error, detail string, fields ...errs.FieldValue) error {
	//: origin-wins wrap; a stdlib cause gets this code.
	return errs.Wrap(cause, errs.WrapParams{
		Code:    CodeMsgPackUnmarshalFailed,
		Reason:  reasonUnmarshal,
		Public:  publicUnmarshal,
		Private: privatePrefix + detail,
	}, fields...)
}

// typeField names a Go type in a failure's fields.
func typeField(t reflect.Type) errs.FieldValue {
	//: reflect's own spelling, e.g. "map[string]int".
	return errs.String(fieldType, t.String())
}

// unsupportedType is the encode failure for a kind MessagePack cannot carry:
// a channel, a function, a complex number or an unsafe pointer.
func unsupportedType(t reflect.Type) error {
	//: name the Go type; the value itself is never quoted.
	return marshalFault("cannot encode a value of this Go type", typeField(t))
}

// depthExceeded is the failure for a value nested past maxDepth, on either
// side: on encode it is usually a cycle, on decode a hostile input.
func depthExceeded(encode bool) error {
	//: the encode side names the likely cause.
	if encode {
		//: a pointer cycle recurses until it reaches the bound.
		return marshalFault("value nests deeper than the depth limit (a cyclic value?)", errs.Int(fieldLimit, maxDepth))
	}
	//: the decode side refuses before the stack grows further.
	return unmarshalFault("input nests deeper than the depth limit", errs.Int(fieldLimit, maxDepth))
}
