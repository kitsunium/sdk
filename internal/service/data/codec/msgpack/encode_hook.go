// Package msgpack — the values that encode themselves, and the untyped entry
// point. appendAny is where Marshal, Append and every interface value start:
// the dynamic types a document is made of are written by a type switch, and
// everything else by the type's cached plan, so both paths write the same
// bytes for the same value.
package msgpack

import (
	"encoding"
	"reflect"
)

// appendAny appends the encoding of x, whatever its dynamic type.
func appendAny(b []byte, x any, depth int) ([]byte, error) {
	//: nil, strings, booleans, the common numbers, []byte.
	if encoded, handled, err := appendScalarAny(b, x); handled {
		//: written without a plan.
		return encoded, err
	}
	//: the two untyped containers a document is made of.
	switch v := x.(type) {
	//: a JSON-shaped object.
	case map[string]any:
		return appendStringMap(b, v, appendAnyElem, depth)
	//: a JSON-shaped array.
	case []any:
		return appendAnySlice(b, v, depth)
	//: anything else, by its cached plan.
	default:
		rv := reflect.ValueOf(x)
		return encoderFor(rv.Type())(b, rv, depth)
	}
}

// appendScalarAny writes the scalar dynamic types without a plan and reports
// whether it handled x.
func appendScalarAny(b []byte, x any) (encoded []byte, handled bool, err error) {
	//: numbers first: the bulk of a document's leaves.
	if encoded, handled = appendNumberAny(b, x); handled {
		//: written.
		return encoded, true, nil
	}
	//: the exact types; named types take the plan, which writes the same.
	switch v := x.(type) {
	//: nil.
	case nil:
		return appendNil(b), true, nil
	//: str.
	case string:
		encoded, err = appendString(b, v)
		return encoded, true, err
	//: true or false.
	case bool:
		return appendBool(b, v), true, nil
	//: bin, or nil for a nil slice.
	case []byte:
		encoded, err = appendBytes(b, v)
		return encoded, true, err
	//: not a scalar fast path.
	default:
		return b, false, nil
	}
}

// appendNumberAny writes the common number types without a plan. A float32
// is written from the value itself, bit for bit — a signalling NaN included —
// as the vendor-backed codec did for an untyped float32.
func appendNumberAny(b []byte, x any) ([]byte, bool) {
	//: the exact types.
	switch v := x.(type) {
	//: shortest integer form.
	case int:
		return appendInt(b, int64(v)), true
	//: shortest integer form.
	case int64:
		return appendInt(b, v), true
	//: shortest integer form.
	case uint64:
		return appendUint(b, v), true
	//: float 64.
	case float64:
		return appendFloat64(b, v), true
	//: float 32, never widened.
	case float32:
		return appendFloat32(b, v), true
	//: not a number fast path.
	default:
		return b, false
	}
}

// appendAnySlice writes a []any: nil for a nil slice, else an array.
func appendAnySlice(b []byte, s []any, depth int) ([]byte, error) {
	//: nil and empty differ on the wire.
	if s == nil {
		//: nil.
		return appendNil(b), nil
	}
	//: one level deeper.
	if depth >= maxDepth {
		//: absurd nesting.
		return b, depthExceeded(true)
	}
	b, err := appendArrayHeader(b, len(s))
	//: refuse a count the wire cannot carry.
	if err != nil {
		//: b unchanged.
		return b, err
	}
	//: each element by its dynamic type.
	for _, x := range s {
		b, err = appendAny(b, x, depth+1)
		//: the first failure wins.
		if err != nil {
			//: stop.
			return b, err
		}
	}
	//: the whole array.
	return b, nil
}

// addressEncoder calls a pointer-receiver method: on the value's address when
// it has one, on an addressable copy otherwise — so a value passed directly
// to Marshal encodes exactly as the same value inside a struct.
func addressEncoder(method encodeFunc) encodeFunc {
	//: the closure is the plan.
	return func(b []byte, v reflect.Value, depth int) ([]byte, error) {
		//: a field, a slice element: call through its address.
		if v.CanAddr() {
			//: no copy.
			return method(b, v.Addr(), depth)
		}
		//: a read-only value cannot be copied out either.
		if !v.CanInterface() {
			//: refuse rather than panic.
			return b, readOnly(v.Type())
		}
		p := reflect.New(v.Type())
		p.Elem().Set(v)
		//: through the copy's address.
		return method(b, p, depth)
	}
}

// encodeSelfMsgpack writes what MarshalMsgpack returns, once it is known to
// be exactly one well-formed value: bytes written as-is cannot be allowed to
// desynchronise everything after them.
func encodeSelfMsgpack(b []byte, v reflect.Value, _ int) ([]byte, error) {
	//: a nil map or slice with the method is nil.
	if isNilable(v.Kind()) && v.IsNil() {
		//: nil.
		return appendNil(b), nil
	}
	m, ok := interfaceOf[marshalMsgpacker](v)
	//: reached through an unexported embedding.
	if !ok {
		//: refuse rather than panic.
		return b, readOnly(v.Type())
	}
	//: call it, check what it wrote.
	return appendMarshalled(b, m, v.Type())
}

// appendMarshalled appends what m.MarshalMsgpack returns, refusing anything
// but exactly one well-formed value.
func appendMarshalled(b []byte, m marshalMsgpacker, t reflect.Type) ([]byte, error) {
	raw, err := m.MarshalMsgpack()
	//: the method's own failure, wrapped.
	if err != nil {
		//: origin wins for an SDK error.
		return b, wrapMarshal(err, "MarshalMsgpack failed", typeField(t))
	}
	//: exactly one value, nothing after it.
	if !isSingleValue(raw) {
		//: refuse the malformed bytes.
		return b, marshalFault("MarshalMsgpack did not return exactly one MessagePack value", typeField(t))
	}
	//: the method's bytes.
	return append(b, raw...), nil
}

// encodeSelfBinary writes what MarshalBinary returns, as a bin.
func encodeSelfBinary(b []byte, v reflect.Value, _ int) ([]byte, error) {
	//: a nil map or slice with the method is nil.
	if isNilable(v.Kind()) && v.IsNil() {
		//: nil.
		return appendNil(b), nil
	}
	m, ok := interfaceOf[encoding.BinaryMarshaler](v)
	//: reached through an unexported embedding.
	if !ok {
		//: refuse rather than panic.
		return b, readOnly(v.Type())
	}
	raw, err := m.MarshalBinary()
	//: the method's own failure, wrapped.
	if err != nil {
		//: origin wins for an SDK error.
		return b, wrapMarshal(err, "MarshalBinary failed", typeField(v.Type()))
	}
	//: bin, or nil when the method returned nil.
	return appendBytes(b, raw)
}

// encodeSelfText writes what MarshalText returns, as a bin — not a str: the
// vendor-backed codec wrote text as binary, and a reader in another language
// would see the type change if this did not.
func encodeSelfText(b []byte, v reflect.Value, _ int) ([]byte, error) {
	//: a nil map or slice with the method is nil.
	if isNilable(v.Kind()) && v.IsNil() {
		//: nil.
		return appendNil(b), nil
	}
	m, ok := interfaceOf[encoding.TextMarshaler](v)
	//: reached through an unexported embedding.
	if !ok {
		//: refuse rather than panic.
		return b, readOnly(v.Type())
	}
	raw, err := m.MarshalText()
	//: the method's own failure, wrapped.
	if err != nil {
		//: origin wins for an SDK error.
		return b, wrapMarshal(err, "MarshalText failed", typeField(v.Type()))
	}
	//: bin, or nil when the method returned nil.
	return appendBytes(b, raw)
}

// isSingleValue reports whether raw is exactly one well-formed value.
func isSingleValue(raw []byte) bool {
	d := decodeState{data: raw}
	//: one value, ending at the last byte.
	return d.skip() == nil && d.off == len(raw)
}
