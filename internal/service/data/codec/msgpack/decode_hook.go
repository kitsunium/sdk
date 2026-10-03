// Package msgpack — pointers, interfaces, values that decode themselves, and
// the entry point every decode starts from. unmarshalInto decodes exactly one
// value and refuses trailing bytes: Unmarshal is handed one document, and
// bytes after it are a framing error, not something to ignore.
package msgpack

import (
	"encoding"
	"reflect"

	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// pointerToAnyType is *any, the target of an untyped Unmarshal.
var pointerToAnyType = reflect.TypeFor[*any]()

// unmarshalInto decodes data — exactly one value — into the value v points
// at.
func unmarshalInto(data []byte, v any) error {
	d := &decodeState{data: data}
	//: decode the one value.
	if err := d.decodeTarget(v); err != nil {
		//: the first failure wins.
		return err
	}
	//: nothing may follow it.
	if d.off != len(d.data) {
		//: name where the value ended and how long the input is.
		return unmarshalFault("trailing bytes after the value",
			errs.Int(fieldOffset, d.off), errs.Int(fieldLen, len(d.data)))
	}
	//: decoded.
	return nil
}

// decodeTarget decodes the next value into what the pointer v points at.
func (d *decodeState) decodeTarget(v any) error {
	rv := reflect.ValueOf(v)
	//: only a non-nil pointer can receive a value.
	if rv.Kind() != reflect.Pointer || rv.IsNil() {
		//: a programming error at the call site, reported as a decode failure.
		return unmarshalFault("decode target is not a non-nil pointer")
	}
	e := rv.Elem()
	//: the untyped target skips the plan lookup.
	if rv.Type() == pointerToAnyType {
		//: the empty-interface rules, at the top level too.
		return decodeEmptyInterface(d, e)
	}
	//: the element's plan.
	return decoderFor(e.Type())(d, e)
}

// pointerDecoder builds the decoder of pointer type t: nil sets the pointer
// to nil, anything else is decoded into what it points at, allocated when
// nil.
func pointerDecoder(t reflect.Type) decodeFunc {
	elem := decoderFor(t.Elem())
	elemType := t.Elem()
	//: the closure is the plan.
	return func(d *decodeState, v reflect.Value) error {
		//: nil is a nil pointer.
		if d.peekNil() {
			d.off++
			//: the pointer, not the pointee, becomes nil.
			return zeroValue(v)
		}
		//: a nil pointer is allocated.
		if v.IsNil() {
			//: a pointer reached through an unexported embedding.
			if !v.CanSet() {
				//: refuse rather than panic.
				return unmarshalFault("cannot set a value reached through an unexported field", typeField(t))
			}
			v.Set(reflect.New(elemType))
		}
		//: what it points at.
		return elem(d, v.Elem())
	}
}

// interfaceDecoder builds the decoder of interface type t.
func interfaceDecoder(t reflect.Type) decodeFunc {
	//: the target choice is the interface's.
	switch {
	//: anything goes.
	case t.NumMethod() == 0:
		return decodeEmptyInterface
	//: a string becomes an error with that text.
	case t == errorType:
		return decodeErrorValue
	//: another interface: only into a pointer it already holds.
	default:
		return decodeNonEmptyInterface
	}
}

// heldPointer returns the non-nil pointer interface v holds, when the next
// value is not nil — the one case where a decode goes INTO what an interface
// holds instead of replacing it.
func heldPointer(d *decodeState, v reflect.Value) (reflect.Value, bool) {
	//: nil replaces whatever is held.
	if v.IsNil() || d.peekNil() {
		//: nothing to decode into.
		return reflect.Value{}, false
	}
	e := v.Elem()
	//: only a non-nil pointer is decoded into.
	return e, e.Kind() == reflect.Pointer && !e.IsNil()
}

// decodeEmptyInterface decodes into an empty interface: into the pointer it
// holds, else the untyped value replaces what it holds.
func decodeEmptyInterface(d *decodeState, v reflect.Value) error {
	//: an interface holding a pointer is a target already chosen.
	if p, ok := heldPointer(d, v); ok {
		//: decode into the pointee.
		return decoderFor(p.Type().Elem())(d, p.Elem())
	}
	x, err := d.decodeAny()
	if err != nil {
		//: malformed, truncated or unsupported.
		return err
	}
	//: a read-only interface cannot be replaced.
	if !v.CanSet() {
		//: refuse rather than panic.
		return unmarshalFault("cannot set a value reached through an unexported field", typeField(v.Type()))
	}
	//: nil clears the interface.
	if x == nil {
		v.SetZero()
		//: cleared.
		return nil
	}
	v.Set(reflect.ValueOf(x))
	//: replaced.
	return nil
}

// decodeErrorValue decodes into the error interface: a string becomes an
// error with that text, nil a nil error.
func decodeErrorValue(d *decodeState, v reflect.Value) error {
	p, err := d.readBytesOrNil(v.Type())
	if err != nil {
		//: not a string.
		return err
	}
	//: nil, or an empty payload from nil.
	if p == nil {
		//: a nil error.
		return zeroValue(v)
	}
	//: the message, as an error value.
	v.Set(reflect.ValueOf(error(&decodedError{msg: string(p)})))
	return nil
}

// decodeNonEmptyInterface decodes into an interface with methods: into the
// pointer it holds, or nil; no concrete type can be chosen for anything else.
func decodeNonEmptyInterface(d *decodeState, v reflect.Value) error {
	//: an interface holding a pointer is a target already chosen.
	if p, ok := heldPointer(d, v); ok {
		//: decode into the pointee.
		return decoderFor(p.Type().Elem())(d, p.Elem())
	}
	//: nil clears the interface.
	if d.peekNil() {
		d.off++
		//: cleared.
		return zeroValue(v)
	}
	//: nothing says which type implements it.
	return unmarshalFault("cannot decode into a non-empty interface that holds no pointer", typeField(v.Type()))
}

// selfTarget returns the address of v as T, the interface a self-decoding
// type implements through its pointer.
func selfTarget[T any](v reflect.Value) (T, error) {
	target, ok := interfaceOf[T](v.Addr())
	//: reached through an unexported embedding.
	if !ok {
		//: refuse rather than panic.
		return target, unmarshalFault("cannot call the decoding method of a value reached through an unexported field", typeField(v.Type()))
	}
	//: the method's receiver.
	return target, nil
}

// decodeSelfMsgpack hands UnmarshalMsgpack a copy of the next value's bytes;
// nil zeroes the target without calling it.
func decodeSelfMsgpack(d *decodeState, v reflect.Value) error {
	//: nil is the zero value, as the vendor decoded it.
	if d.peekNil() {
		d.off++
		//: zero, method not called.
		return zeroValue(v)
	}
	start := d.off
	//: find where the value ends.
	if err := d.skip(); err != nil {
		//: malformed or truncated.
		return err
	}
	u, err := selfTarget[unmarshalMsgpacker](v)
	if err != nil {
		//: read-only.
		return err
	}
	//: the method receives a copy of exactly the value's bytes.
	return callUnmarshalMsgpack(u, cloneBytes(d.data[start:d.off]), v.Type())
}

// callUnmarshalMsgpack hands raw to the method, wrapping its failure.
func callUnmarshalMsgpack(u unmarshalMsgpacker, raw []byte, t reflect.Type) error {
	//: the method's own failure, wrapped.
	if err := u.UnmarshalMsgpack(raw); err != nil {
		//: origin wins for an SDK error.
		return wrapUnmarshal(err, "UnmarshalMsgpack failed", typeField(t))
	}
	//: decoded by the type.
	return nil
}

// decodeSelfBinary hands UnmarshalBinary a copy of a bin or str payload; nil
// zeroes the target without calling it.
func decodeSelfBinary(d *decodeState, v reflect.Value) error {
	p, isNil, err := d.selfPayload(v)
	//: wrong family, malformed or truncated — or nil, which zeroes.
	if err != nil || isNil {
		//: done either way.
		return err
	}
	u, err := selfTarget[encoding.BinaryUnmarshaler](v)
	if err != nil {
		//: read-only.
		return err
	}
	//: the method's own failure, wrapped.
	if uerr := u.UnmarshalBinary(p); uerr != nil {
		//: origin wins for an SDK error.
		return wrapUnmarshal(uerr, "UnmarshalBinary failed", typeField(v.Type()))
	}
	//: decoded by the type.
	return nil
}

// decodeSelfText hands UnmarshalText a copy of a str or bin payload; nil
// zeroes the target without calling it.
func decodeSelfText(d *decodeState, v reflect.Value) error {
	p, isNil, err := d.selfPayload(v)
	//: wrong family, malformed or truncated — or nil, which zeroes.
	if err != nil || isNil {
		//: done either way.
		return err
	}
	u, err := selfTarget[encoding.TextUnmarshaler](v)
	if err != nil {
		//: read-only.
		return err
	}
	//: the method's own failure, wrapped.
	if uerr := u.UnmarshalText(p); uerr != nil {
		//: origin wins for an SDK error.
		return wrapUnmarshal(uerr, "UnmarshalText failed", typeField(v.Type()))
	}
	//: decoded by the type.
	return nil
}

// selfPayload reads the payload a Binary/TextUnmarshaler receives: a copy of
// a bin or str. For nil it zeroes v and reports isNil.
func (d *decodeState) selfPayload(v reflect.Value) (payload []byte, isNil bool, err error) {
	//: nil is the zero value, method not called.
	if d.peekNil() {
		d.off++
		//: zeroed.
		return nil, true, zeroValue(v)
	}
	p, err := d.readBytesOrNil(v.Type())
	if err != nil {
		//: wrong family, malformed or truncated.
		return nil, false, err
	}
	//: the method may keep what it receives.
	return cloneBytes(p), false, nil
}
