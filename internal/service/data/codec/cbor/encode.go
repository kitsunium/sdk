package cbor

import (
	"encoding/binary"
	"math"
	"reflect"
	"time"
	"unicode/utf8"
)

// maxIndirections bounds the pointers and interfaces followed between two
// containers. A chain that long is a cycle — an interface holding a pointer
// to itself — and not data.
const maxIndirections int = 32

// walkDepth is where the encoder stands in the value it walks.
type walkDepth struct {
	// nest counts the arrays, maps and tags open around the current item.
	nest int
	// hops counts the pointers and interfaces followed since the last one.
	hops int
}

// enter is the depth inside one more array, map or tag, refused past the
// nesting the decoder accepts.
func (w walkDepth) enter() (walkDepth, error) {
	//: one level more than the decoder reads back is a level too many.
	if w.nest >= maxCBORNestedLevels {
		//: refused, and the reason a cycle of structs or slices ends here.
		return w, encodeFailure("a value nested more than 32 arrays, maps and tags deep")
	}
	//: one level in, no indirection yet.
	return walkDepth{nest: w.nest + 1}, nil
}

// follow is the depth past one more pointer or interface.
func (w walkDepth) follow() (walkDepth, error) {
	//: an interface holding a pointer to itself would never end.
	if w.hops >= maxIndirections {
		//: refused.
		return w, encodeFailure("more than 32 pointers and interfaces between two containers")
	}
	//: same level, one more indirection.
	return walkDepth{nest: w.nest, hops: w.hops + 1}, nil
}

// appendValue appends the encoding of x to b. The dynamic types an untyped
// document is made of are encoded without reflection.
func appendValue(b []byte, x any, at walkDepth) ([]byte, error) {
	//: the scalars first, then the two untyped containers.
	if encoded, handled, err := appendScalar(b, x); handled {
		//: a scalar, or a string refused for its encoding.
		return encoded, err
	}
	//: the containers of an untyped document.
	switch typed := x.(type) {
	case map[string]any:
		//: sorted keys, untyped values.
		return appendStringAnyMap(b, typed, at)
	case []any:
		//: an array of untyped values.
		return appendAnySlice(b, typed, at)
	default:
		//: every other type has a plan.
		rv := reflect.ValueOf(x)
		plan := encodePlanFor(rv.Type())
		//: through the plan.
		return plan.kind.encode(b, rv, plan, at)
	}
}

// appendScalar encodes x when it is nil or one of the common scalar types.
// handled is false for any other type.
func appendScalar(b []byte, x any) (encoded []byte, handled bool, err error) {
	//: the types an untyped document and most payloads hold.
	switch typed := x.(type) {
	case nil:
		//: null.
		return append(b, nullByte), true, nil
	case string:
		encoded, err = appendText(b, typed)
		//: a text string, or its refusal.
		return encoded, true, err
	case []byte:
		//: a byte string, or null for nil.
		return appendBytes(b, typed), true, nil
	case bool:
		//: true or false.
		return appendBool(b, typed), true, nil
	default:
		encoded, handled = appendNumber(b, x)
		//: a number, or not a scalar this switch knows.
		return encoded, handled, nil
	}
}

// appendNumber encodes x when it is one of the common number types, or a
// time.Time.
func appendNumber(b []byte, x any) (encoded []byte, handled bool) {
	//: the widths a document most often carries.
	switch typed := x.(type) {
	case float64:
		//: a double, or a half for NaN and the infinities.
		return appendFloat64(b, typed), true
	case int:
		//: signed.
		return appendInt(b, int64(typed)), true
	case int64:
		//: signed.
		return appendInt(b, typed), true
	case uint64:
		//: unsigned.
		return appendHead(b, majorUnsigned, typed), true
	case time.Time:
		//: Unix seconds.
		return appendTime(b, typed), true
	default:
		//: through a plan.
		return b, false
	}
}

// appendAnySlice appends an array of untyped values.
func appendAnySlice(b []byte, s []any, at walkDepth) ([]byte, error) {
	//: a nil slice is null, as every nil slice is.
	if s == nil {
		//: null.
		return append(b, nullByte), nil
	}
	inner, err := at.enter()
	//: an array is one level of nesting.
	if err != nil {
		//: too deep.
		return b, err
	}
	b = appendHead(b, majorArray, uint64(len(s)))
	//: each element in order.
	for _, elem := range s {
		b, err = appendValue(b, elem, inner)
		//: the first failure ends the encoding.
		if err != nil {
			//: refused.
			return b, err
		}
	}
	//: the whole array.
	return b, nil
}

// appendBool appends true or false.
func appendBool(b []byte, v bool) []byte {
	//: one byte either way.
	if v {
		//: true.
		return append(b, trueByte)
	}
	//: false.
	return append(b, falseByte)
}

// appendInt appends a signed integer: major type 0 for n ≥ 0, and major type
// 1 carrying −1−n otherwise, which reaches math.MinInt64 without overflow.
func appendInt(b []byte, n int64) []byte {
	//: non-negative integers are unsigned.
	if n >= 0 {
		//: as is.
		return appendHead(b, majorUnsigned, uint64(n))
	}
	//: −1−n, computed as the bitwise complement.
	return appendHead(b, majorNegative, uint64(^n))
}

// appendFloat64 appends a double-precision float; NaN and the infinities
// take their half-precision form, as fxamacker/cbor wrote them.
func appendFloat64(b []byte, f float64) []byte {
	//: the special values have one short encoding each.
	if special, ok := specialFloat(f); ok {
		//: three bytes.
		return appendHalf(b, special)
	}
	//: eight bytes, network byte order.
	return binary.BigEndian.AppendUint64(append(b, float64Head), math.Float64bits(f))
}

// appendFloat32 appends a single-precision float; NaN and the infinities
// take their half-precision form.
func appendFloat32(b []byte, f float32) []byte {
	//: the special values have one short encoding each.
	if special, ok := specialFloat(float64(f)); ok {
		//: three bytes.
		return appendHalf(b, special)
	}
	//: four bytes, network byte order.
	return binary.BigEndian.AppendUint32(append(b, float32Head), math.Float32bits(f))
}

// specialFloat returns the half-precision encoding of NaN or an infinity.
func specialFloat(f float64) (half uint16, ok bool) {
	//: every NaN is written as the one canonical quiet NaN.
	if math.IsNaN(f) {
		//: 0x7e00.
		return halfNaN, true
	}
	//: an infinity keeps its sign.
	if math.IsInf(f, 0) {
		//: ±0x7c00.
		return infinityHalf(f), true
	}
	//: an ordinary number.
	return 0, false
}

// infinityHalf is the half-precision infinity of the sign of f.
func infinityHalf(f float64) uint16 {
	//: positive or negative.
	if f > 0 {
		//: +Inf.
		return halfPosInf
	}
	//: −Inf.
	return halfNegInf
}

// appendHalf appends a half-precision float given by its bits.
func appendHalf(b []byte, half uint16) []byte {
	//: two bytes, network byte order.
	return binary.BigEndian.AppendUint16(append(b, float16Head), half)
}

// appendText appends a text string, refusing one that is not UTF-8: RFC 8949
// §5.3.1 makes such an item invalid, and the decoder refuses it.
func appendText(b []byte, s string) ([]byte, error) {
	//: valid UTF-8 or nothing.
	if !utf8.ValidString(s) {
		//: refused.
		return b, encodeFailure("a string that is not valid UTF-8")
	}
	b = appendHead(b, majorText, uint64(len(s)))
	//: the bytes as they are.
	return append(b, s...), nil
}

// appendBytes appends a byte string; a nil slice is null.
func appendBytes(b, data []byte) []byte {
	//: nil is null, as fxamacker/cbor wrote it.
	if data == nil {
		//: null.
		return append(b, nullByte)
	}
	b = appendHead(b, majorBytes, uint64(len(data)))
	//: the bytes as they are.
	return append(b, data...)
}

// appendTime appends a time.Time as its integer Unix seconds, or null for
// the zero time. The fraction of a second is not written — the encoding the
// codec has always produced, kept so a reader of its output sees no change.
func appendTime(b []byte, t time.Time) []byte {
	//: the zero time is "no time".
	if t.IsZero() {
		//: null.
		return append(b, nullByte)
	}
	//: whole seconds, untagged.
	return appendInt(b, t.Unix())
}
