// Package msgpack — slice, array and byte decoders. A declared element count
// is never trusted with memory: it is first checked against the bytes that
// remain, then at most preallocBytes worth of elements is reserved, and the
// slice grows as elements actually decode — so a hostile count costs what its
// input costs, not what it claims.
package msgpack

import (
	"reflect"

	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// sliceDecoder builds the decoder of slice type t.
func sliceDecoder(t reflect.Type) decodeFunc {
	//: any slice of a byte kind reads a bin or str.
	if t.Elem().Kind() == reflect.Uint8 {
		//: a copy of the payload.
		return decodeByteSlice
	}
	//: an array of elements.
	return newListDecoder(t.Elem()).decodeSlice
}

// arrayDecoder builds the decoder of array type t.
func arrayDecoder(t reflect.Type) decodeFunc {
	//: [N]byte reads a bin or str of at most N bytes.
	if t.Elem().Kind() == reflect.Uint8 {
		//: copied in place.
		return decodeByteArray
	}
	//: an array of at most N elements.
	return newListDecoder(t.Elem()).decodeArray
}

// newListDecoder builds the element plan of a slice or array.
func newListDecoder(elem reflect.Type) *listDecoder {
	firstCap := uint64(preallocBytes)
	//: a zero-size element reserves nothing however many there are.
	if size := uint64(elem.Size()); size > 0 {
		firstCap = max(firstCap/size, 1)
	}
	//: the plan.
	return &listDecoder{elem: decoderFor(elem), firstCap: firstCap}
}

// decodeSlice decodes an array into a slice, or nil into a nil slice.
func (l *listDecoder) decodeSlice(d *decodeState, v reflect.Value) error {
	h, err := d.readHeader()
	if err != nil {
		//: malformed or truncated.
		return err
	}
	//: an array or nil.
	switch h.fam {
	//: the elements.
	case famArray:
		return l.fillSlice(d, v, h)
	//: nil is a nil slice.
	case famNil:
		return zeroValue(v)
	//: anything else.
	default:
		return d.mismatch(h, v.Type())
	}
}

// fillSlice replaces v's elements with the array's.
func (l *listDecoder) fillSlice(d *decodeState, v reflect.Value, h header) error {
	//: a count the input cannot hold is refused before anything is reserved.
	if err := d.checkCount(h); err != nil {
		//: implausible count.
		return err
	}
	//: one level deeper.
	if err := d.enter(); err != nil {
		//: nested too deep.
		return err
	}
	n := int(h.arg)
	//: reuse the backing array when there is one, else reserve a bounded part.
	if v.Cap() == 0 || n == 0 {
		v.Set(reflect.MakeSlice(v.Type(), 0, int(min(h.arg, l.firstCap))))
	} else {
		v.SetLen(0)
	}
	//: one element per declared slot, growing as they arrive.
	for i := range n {
		//: double the capacity, never past the declared count.
		if i == v.Cap() {
			v.Grow(min(n-i, max(i, 1)))
		}
		v.SetLen(i + 1)
		elem := v.Index(i)
		elem.SetZero()
		//: the first failure wins.
		if err := l.elem(d, elem); err != nil {
			//: stop.
			return err
		}
	}
	d.leave()
	//: exactly the decoded elements.
	return nil
}

// decodeArray decodes an array into a Go array of at least as many elements,
// zeroing the ones the input does not reach; nil zeroes the whole array.
func (l *listDecoder) decodeArray(d *decodeState, v reflect.Value) error {
	h, err := d.readHeader()
	if err != nil {
		//: malformed or truncated.
		return err
	}
	//: an array or nil.
	switch h.fam {
	//: the elements.
	case famArray:
		return l.fillArray(d, v, h)
	//: nil is the zero array.
	case famNil:
		return zeroValue(v)
	//: anything else.
	default:
		return d.mismatch(h, v.Type())
	}
}

// fillArray decodes h's elements into the Go array v.
func (l *listDecoder) fillArray(d *decodeState, v reflect.Value, h header) error {
	//: more elements than the Go array holds is refused, not truncated.
	if h.arg > uint64(v.Len()) {
		//: name both lengths.
		return unmarshalFault("array has more elements than the Go array holds",
			typeField(v.Type()), errs.Int64(fieldLen, int64(h.arg)))
	}
	//: one level deeper.
	if err := d.enter(); err != nil {
		//: nested too deep.
		return err
	}
	//: decode the elements present, zero the rest.
	for i := range v.Len() {
		elem := v.Index(i)
		elem.SetZero()
		//: past the input's elements only zeroing is left.
		if uint64(i) >= h.arg {
			continue
		}
		//: the first failure wins.
		if err := l.elem(d, elem); err != nil {
			//: stop.
			return err
		}
	}
	d.leave()
	//: filled.
	return nil
}

// decodeByteSlice decodes a bin or str into a byte slice that owns a copy;
// nil is a nil slice, an empty payload an empty one.
func decodeByteSlice(d *decodeState, v reflect.Value) error {
	at := d.off
	//: nil keeps the slice nil.
	if d.peekNil() {
		d.off++
		//: a nil slice.
		return zeroValue(v)
	}
	p, err := d.readBytesOrNil(v.Type())
	if err != nil {
		//: wrong family, malformed or truncated.
		return err
	}
	//: a settable slice owns its copy.
	if !v.CanSet() {
		//: refuse rather than panic.
		return unmarshalFault("cannot set a value reached through an unexported field", errs.Int(fieldOffset, at))
	}
	v.SetBytes(cloneBytes(p))
	//: stored.
	return nil
}

// decodeByteArray decodes a bin or str of at most N bytes into a [N]byte,
// zeroing the bytes past it; nil zeroes the array.
func decodeByteArray(d *decodeState, v reflect.Value) error {
	p, err := d.readBytesOrNil(v.Type())
	if err != nil {
		//: wrong family, malformed or truncated.
		return err
	}
	//: more bytes than the array holds is refused, not truncated.
	if len(p) > v.Len() {
		//: name both lengths.
		return unmarshalFault("binary is longer than the Go array holds", typeField(v.Type()), errs.Int(fieldLen, len(p)))
	}
	dst := v.Bytes()
	clear(dst[copy(dst, p):])
	//: copied in place.
	return nil
}
