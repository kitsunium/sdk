// Package bson — encoding of the containers: arrays, ordered documents,
// structs and maps. A map's entries are written in the map's own order and
// then put in ascending key order, so the same map always encodes to the same
// bytes.
package bson

import (
	"bytes"
	"encoding"
	"encoding/binary"
	"math"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/kitsunium/sdk/internal/core/codec/scratch"
	"github.com/kitsunium/sdk/internal/kernel/concur/recycler"
)

// Encoder sizing.
const (
	// maxRetainedSegments bounds the segment lists the map encoder pools: a
	// list grown past it by a very wide map is dropped instead of pinned.
	maxRetainedSegments int = 4096
	// elementHeadroom is the free space reserve keeps before each element.
	elementHeadroom int = 64
)

// segment is one encoded element of a map, located in the output so the
// elements can be put in key order once they are all written.
type segment struct {
	// start is the element's type byte.
	start int
	// keyEnd is the NUL ending its name; the name is buf[start+1:keyEnd].
	keyEnd int
	// end is one past its last byte.
	end int
}

// segmentPool recycles the per-map segment lists.
var segmentPool = recycler.NewCappedPool[*[]segment](
	func() *[]segment {
		//: room for an ordinary document.
		return new(make([]segment, 0, 32))
	},
	func(s *[]segment) { *s = (*s)[:0] },
	func(s *[]segment) int { return cap(*s) },
	maxRetainedSegments,
)

// encodeContainer opens a document or array, refusing past the depth bound.
func (e *encoder) encodeContainer(rv reflect.Value, p *typePlan, st encodeState) (byte, error) {
	child, start, err := e.openContainer(st)
	//: too deep.
	if err != nil {
		//: refused.
		return 0, err
	}
	t, err := e.encodeElements(rv, p, child)
	//: an element failed.
	if err != nil {
		//: the caller restores the buffer.
		return 0, err
	}
	//: the terminator and the length.
	return t, e.endDocument(start)
}

// encodeElements writes the elements of a container and returns its type.
func (e *encoder) encodeElements(rv reflect.Value, p *typePlan, st encodeState) (byte, error) {
	//: one layout per container mapping.
	switch p.kind {
	case kindSlice, kindArray:
		return typeArray, e.encodeArrayElements(rv, p, st)
	case kindD, kindDocSlice, kindDocArray:
		return typeDocument, e.encodeDocElements(rv, st)
	case kindMap:
		return typeDocument, e.encodeMapElements(rv, p, st, nil)
	default:
		//: a struct.
		return typeDocument, e.encodeStructElements(rv, p, st)
	}
}

// encodeArrayElements writes a slice's or array's elements under the keys
// "0", "1", ….
func (e *encoder) encodeArrayElements(rv reflect.Value, p *typePlan, st encodeState) error {
	elemState := st
	//: a slice's elements are addressable; an array's are when it is.
	if p.kind == kindSlice {
		elemState.addressable = true
	}
	n := rv.Len()
	//: in order.
	for i := range n {
		begin := e.beginIndex(i)
		t, err := e.encodeValue(rv.Index(i), p.elem, elemState)
		//: a value that cannot be encoded.
		if err != nil {
			//: refused.
			return err
		}
		e.buf[begin] = t
	}
	//: written.
	return nil
}

// encodeDocElements writes the E elements of a D, a slice convertible to one,
// or an array of E, in order.
func (e *encoder) encodeDocElements(rv reflect.Value, st encodeState) error {
	n := rv.Len()
	//: in order: the order is the point of a D.
	for i := range n {
		el := rv.Index(i)
		begin := len(e.buf)
		//: an element name BSON cannot hold.
		if _, err := e.beginElement(el.Field(0).String()); err != nil {
			//: refused.
			return err
		}
		value := el.Field(1)
		t, err := e.encodeAnyValue(value.Interface(), value.Elem(), st)
		//: a value that cannot be encoded.
		if err != nil {
			//: refused.
			return err
		}
		e.buf[begin] = t
	}
	//: written.
	return nil
}

// encodeStructElements writes a struct's fields, then its ",inline" map.
func (e *encoder) encodeStructElements(rv reflect.Value, p *typePlan, st encodeState) error {
	//: a struct the plan could not describe.
	if p.invalid != "" {
		//: refused.
		return marshalError(nil, p.invalid)
	}
	//: in declaration order.
	for _, f := range p.fields {
		//: one field.
		if err := e.encodeField(rv, f, st); err != nil {
			//: refused.
			return err
		}
	}
	//: no inline map: nothing more.
	if p.inlineMap == nil {
		//: written.
		return nil
	}
	inline := rv.Field(p.inlineMap.index[0])
	//: a nil inline map adds nothing.
	if inline.IsNil() {
		//: written.
		return nil
	}
	//: its entries, refused when one collides with a field name.
	return e.encodeMapElements(inline, p.inlineMap.plan, st, p.byName)
}

// encodeField writes one struct field, honouring omitempty and minsize. A
// field behind a nil ",inline" pointer is not written.
func (e *encoder) encodeField(rv reflect.Value, f *fieldPlan, st encodeState) error {
	fv, ok := fieldByIndex(rv, f.index)
	//: an inline pointer on the path is nil, or the field is empty under
	//: omitempty.
	if !ok || (f.flags.has(flagOmitEmpty) && isEmptyField(fv, f.plan)) {
		//: left out.
		return nil
	}
	//: a name an element cannot carry.
	if f.flags.has(flagBadName) {
		//: refused.
		return marshalError(nil, "field "+f.goName+" has an element name holding a NUL byte or invalid UTF-8")
	}
	e.reserve()
	begin := len(e.buf)
	e.buf = append(e.buf, 0)
	e.buf = append(e.buf, f.nameBytes...)
	e.buf = append(e.buf, 0)
	fieldState := st
	fieldState.minSize = st.minSize || f.flags.has(flagMinSize)
	t, err := e.encodeValue(fv, f.plan, fieldState)
	//: a value that cannot be encoded.
	if err != nil {
		//: refused.
		return err
	}
	e.buf[begin] = t
	//: written.
	return nil
}

// fieldByIndex walks an index path, reporting false when a pointer on it is
// nil.
func fieldByIndex(rv reflect.Value, index []int) (reflect.Value, bool) {
	v := rv.Field(index[0])
	//: the inlined steps.
	for _, i := range index[1:] {
		//: an inlined pointer.
		if v.Kind() == reflect.Pointer {
			//: nil: the fields behind it do not exist.
			if v.IsNil() {
				//: not reachable.
				return v, false
			}
			v = v.Elem()
		}
		v = v.Field(i)
	}
	//: the field.
	return v, true
}

// isEmptyField is omitempty's test. An interface field is empty when nil;
// one whose type is a non-empty interface without MarshalBSON is judged by
// its dynamic value, as the previous library judged it.
func isEmptyField(fv reflect.Value, p *typePlan) bool {
	//: any field that is not an interface.
	if fv.Kind() != reflect.Interface {
		//: its own value decides.
		return isEmptyValue(fv, p)
	}
	//: nil is empty whatever the interface.
	if fv.IsNil() {
		//: empty.
		return true
	}
	//: the empty interface, and an interface that writes itself.
	if p.kind == kindAny || p.marshaler != hookNone {
		//: not nil, so not empty.
		return false
	}
	elem := fv.Elem()
	//: the dynamic value decides.
	return isEmptyValue(elem, planFor(elem.Type()))
}

// isEmptyValue asks IsZero when the type has it and the value is not a nil
// pointer; otherwise a length of zero, false for a struct, and the zero value
// for everything else.
func isEmptyValue(v reflect.Value, p *typePlan) bool {
	//: the type's own answer.
	if p.zeroer && (v.Kind() != reflect.Pointer || !v.IsNil()) {
		//: IsZero, reached without allocating where possible.
		if z, ok := methodValue(v).(interface{ IsZero() bool }); ok {
			//: its verdict.
			return z.IsZero()
		}
	}
	//: by kind.
	switch v.Kind() {
	//: a length.
	case reflect.Array, reflect.Map, reflect.Slice, reflect.String:
		return v.Len() == 0
	//: a struct without IsZero is never empty.
	case reflect.Struct:
		return false
	default:
		//: numbers, booleans, pointers, interfaces.
		return v.IsZero()
	}
}

// encodeMapElements writes a map's entries in ascending key order. collisions,
// when not nil, are the field names an ",inline" map must not repeat.
func (e *encoder) encodeMapElements(rv reflect.Value, p *typePlan, st encodeState, collisions map[string]*fieldPlan) error {
	//: an empty map writes no key, whatever its key type.
	if rv.Len() == 0 {
		//: nothing to write.
		return nil
	}
	//: a key type with no element-name form.
	if p.keyEncode == keyUnsupported {
		//: refused once there is a key to write.
		return marshalError(nil, "map key type "+p.typ.Key().String()+" has no BSON element-name form")
	}
	segs := segmentPool.Get()
	defer segmentPool.Put(segs)
	valueState := st
	//: a map value is not addressable.
	valueState.addressable = false
	slots := p.acquireSlots()
	defer p.releaseSlots(slots)
	iter := rv.MapRange()
	//: every entry, in the map's order for now.
	for iter.Next() {
		slots.key.SetIterKey(iter)
		slots.value.SetIterValue(iter)
		seg, err := e.encodeMapEntry(slots.key, slots.value, p, valueState, collisions)
		//: an entry that cannot be encoded.
		if err != nil {
			//: refused.
			return err
		}
		*segs = append(*segs, seg)
	}
	e.sortSegments(*segs)
	//: written.
	return nil
}

// encodeMapEntry writes one map entry and returns where it lies.
func (e *encoder) encodeMapEntry(key, value reflect.Value, p *typePlan, st encodeState, collisions map[string]*fieldPlan) (segment, error) {
	begin := len(e.buf)
	keyEnd, err := e.beginMapKey(key, p.keyEncode, collisions)
	//: a key with no element-name form.
	if err != nil {
		//: refused.
		return segment{}, err
	}
	t, err := e.encodeValue(value, p.elem, st)
	//: a value that cannot be encoded.
	if err != nil {
		//: refused.
		return segment{}, err
	}
	e.buf[begin] = t
	//: the entry's bytes.
	return segment{start: begin, keyEnd: keyEnd, end: len(e.buf)}, nil
}

// beginMapKey writes an element's type placeholder and the map key as its
// name, and returns the index of the name's NUL.
func (e *encoder) beginMapKey(key reflect.Value, mode keyMode, collisions map[string]*fieldPlan) (int, error) {
	name, err := mapKeyName(key, mode)
	//: a key with no element-name form.
	if err != nil {
		//: refused.
		return 0, err
	}
	//: an inline map's key must not shadow a field.
	if collisions != nil && collisions[name] != nil {
		//: refused, as the previous library refused it.
		return 0, marshalError(nil, "an inline map key collides with a struct field name")
	}
	//: the element header.
	return e.beginElement(name)
}

// mapKeyName renders a map key as an element name.
func mapKeyName(key reflect.Value, mode keyMode) (string, error) {
	//: one rendering per key mode.
	switch mode {
	case keyString:
		return key.String(), nil
	case keyInt:
		return strconv.FormatInt(key.Int(), 10), nil
	case keyUint:
		return strconv.FormatUint(key.Uint(), 10), nil
	case keyText:
		return textKey(key)
	case keyUnsupported:
		//: refused before any key is rendered.
		return "", marshalError(nil, "map key type "+key.Type().String()+" has no BSON element-name form")
	default:
		//: no other mode exists.
		return "", marshalError(nil, "map key type "+key.Type().String()+" has no BSON element-name form")
	}
}

// textKey renders a key through its MarshalText method; a nil pointer key is
// the empty name.
func textKey(key reflect.Value) (string, error) {
	//: a nil pointer has no text and is not called.
	if key.Kind() == reflect.Pointer && key.IsNil() {
		//: the empty name.
		return "", nil
	}
	tm, ok := reflect.TypeAssert[encoding.TextMarshaler](key)
	//: planned as a text key, so the method is there.
	if !ok {
		//: refused.
		return "", marshalError(nil, "map key type "+key.Type().String()+" has no MarshalText")
	}
	text, err := tm.MarshalText()
	//: the method's own failure.
	if err != nil {
		//: wrapped.
		return "", marshalError(err, "MarshalText of map key type "+key.Type().String()+" failed")
	}
	//: the text.
	return string(text), nil
}

// sortSegments rewrites the map elements just written, between the first
// segment's start and the buffer's end, in ascending key order.
func (e *encoder) sortSegments(segs []segment) {
	//: zero or one element is already in order.
	if len(segs) < 2 {
		//: nothing to do.
		return
	}
	first := segs[0].start
	buf := e.buf
	slices.SortFunc(segs, func(x, y segment) int {
		//: byte-wise on the element names, as encoding/json orders keys.
		return bytes.Compare(buf[x.start+1:x.keyEnd], buf[y.start+1:y.keyEnd])
	})
	tmp := scratch.AcquireBuffer()
	defer scratch.ReleaseBuffer(tmp)
	tmp.Write(buf[first:])
	region := tmp.Bytes()
	out := buf[:first]
	//: each element, in key order.
	for _, s := range segs {
		out = append(out, region[s.start-first:s.end-first]...)
	}
	e.buf = out
}

// reserve keeps elementHeadroom bytes free, doubling the buffer when it must
// grow: past 256 bytes append grows a slice by about a quarter, so a large
// document would allocate four to five times its size on the way; doubling
// bounds that at about twice.
func (e *encoder) reserve() {
	//: room enough for the next element's header and a small value.
	if cap(e.buf)-len(e.buf) >= elementHeadroom {
		return
	}
	e.buf = slices.Grow(e.buf, max(cap(e.buf), elementHeadroom))
}

// beginElement writes a type placeholder and the element name, refusing a
// name BSON cannot carry, and returns the index of the name's NUL.
func (e *encoder) beginElement(name string) (int, error) {
	//: a cstring cannot hold a NUL, and is UTF-8.
	if strings.IndexByte(name, 0) >= 0 || !utf8.ValidString(name) {
		//: refused, without quoting the name.
		return 0, marshalError(nil, "an element name holds a NUL byte or invalid UTF-8")
	}
	e.reserve()
	e.buf = append(e.buf, 0)
	e.buf = append(e.buf, name...)
	keyEnd := len(e.buf)
	e.buf = append(e.buf, 0)
	//: the NUL's index.
	return keyEnd, nil
}

// beginIndex writes a type placeholder and the decimal index as the name, and
// returns where the type byte is.
func (e *encoder) beginIndex(i int) int {
	e.reserve()
	begin := len(e.buf)
	e.buf = append(e.buf, 0)
	e.buf = strconv.AppendInt(e.buf, int64(i), 10)
	e.buf = append(e.buf, 0)
	//: the placeholder's index.
	return begin
}

// reserveLength appends a placeholder for a document's length and returns
// where it is.
func (e *encoder) reserveLength() int {
	start := len(e.buf)
	e.buf = append(e.buf, 0, 0, 0, 0)
	//: patched by endDocument.
	return start
}

// endDocument writes the terminator and the length of the document started at
// start, refusing a document whose length an int32 cannot hold.
func (e *encoder) endDocument(start int) error {
	e.buf = append(e.buf, 0)
	size := len(e.buf) - start
	//: a length beyond int32 cannot be written.
	if int64(size) > math.MaxInt32 {
		//: refused.
		return marshalError(nil, "a document exceeds the 2 GiB an int32 length can describe")
	}
	binary.LittleEndian.PutUint32(e.buf[start:], uint32(size))
	//: closed.
	return nil
}
