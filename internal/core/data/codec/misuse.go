package codec

import (
	"reflect"

	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// The fields the misuse refusals carry (#1101).
const (
	// fieldTarget names the type of a refused Unmarshal or Decode target.
	fieldTarget string = "target"
	// fieldType names the type of a refused Marshal value.
	fieldType string = "type"
	// nilTarget is what fieldTarget says for an untyped nil.
	nilTarget string = "nil"
)

// CheckTarget refuses v as the target of an Unmarshal or a Decode unless it
// is a non-nil pointer: TargetInvalid, its "target" field naming v's type,
// "nil" for an untyped nil. Every codec calls it before reading a byte
// (#1101) — bson also takes a non-nil map, flatbuffers a BytesAcceptor that
// is not a pointer —, so a programmer's error is never reported as corrupt
// input. It allocates only when it refuses.
func CheckTarget(v any) error {
	//: an untyped nil has no type to name.
	if v == nil {
		return errs.Wrap(TargetInvalid, errs.WrapParams{}, errs.String(fieldTarget, nilTarget))
	}
	rv := reflect.ValueOf(v)
	//: the one target a decoder can store through.
	if rv.Kind() == reflect.Pointer && !rv.IsNil() {
		return nil
	}
	//: a typed nil pointer, or a value the decoder could not change.
	return errs.Wrap(TargetInvalid, errs.WrapParams{}, errs.String(fieldTarget, rv.Type().String()))
}

// CheckValue refuses v as the value of a Marshal when its kind is one no
// codec encodes — a channel, a function, an unsafe pointer —: ValueInvalid,
// its "type" field naming v's type. The Marshal of the codecs that encode any
// Go value (json, xml, yaml, toml, cbor, msgpack, bson) calls it first
// (#1101); Append, the hot path, leaves the refusal to the encoder. It
// allocates only when it refuses.
func CheckValue(v any) error {
	t := reflect.TypeOf(v)
	//: nil encodes as each format's null.
	if t == nil {
		return nil
	}
	//: the kinds no wire format holds.
	switch t.Kind() {
	//: a channel, a function, an unsafe pointer.
	case reflect.Chan, reflect.Func, reflect.UnsafePointer:
		//: refused before encoding, the type named.
		return errs.Wrap(ValueInvalid, errs.WrapParams{}, errs.String(fieldType, t.String()))
	//: every other kind is the codec's to judge.
	default:
		return nil
	}
}

// IsMisuse reports whether err, or an error it wraps, is a codec refusing how
// it was called rather than a byte it read: EMPTY_INPUT, TARGET_INVALID or
// VALUE_INVALID, the family's codes 0.2.2.2 to 0.2.2.4 (#1101). A
// TARGET_INVALID or a VALUE_INVALID is the program's own bug, an HTTP server
// answers it with 500; an EMPTY_INPUT is a body that never came, 400.
func IsMisuse(err error) bool {
	//: by the family's codes, wherever they are in the chain.
	return errs.HasCode(err, CodeEmptyInput) ||
		errs.HasCode(err, CodeTargetInvalid) ||
		errs.HasCode(err, CodeValueInvalid)
}
