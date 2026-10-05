package bson

import (
	"bytes"
	"reflect"
)

// decodeState is what a value's decoding depends on besides its bytes and its
// target.
type decodeState struct {
	// ancestor is the type a document decodes as inside an interface: the
	// nearest enclosing map whose values are interface{} or slice of E, as the
	// previous library tracked it; nil means D.
	ancestor reflect.Type
	// truncate lets a double with a fraction decode into an integer.
	truncate bool
	// field is the Go name of the innermost struct field, for messages.
	field string
}

// element is one element of a document: its type byte, its name and the bytes
// of its value.
type element struct {
	// typ is the BSON type byte.
	typ byte
	// key is the element name, aliasing the input.
	key []byte
	// value is the value's bytes, aliasing the input.
	value []byte
}

// errCorrupt reports bytes that changed shape after validation — input the
// caller modified while it was being decoded.
func errCorrupt() error {
	//: never reached with input left alone.
	return unmarshalError(nil, "the input changed while it was decoded")
}

// decodeInto decodes the validated document data into v.
func decodeInto(data []byte, v any) error {
	//: there is nowhere to decode into.
	if v == nil {
		//: refused.
		return unmarshalError(nil, "the target is nil")
	}
	rv := reflect.ValueOf(v)
	//: a target that reads the whole document itself.
	if u, ok := v.(interface{ UnmarshalBSON(data []byte) error }); ok {
		//: hand it a copy, never the caller's buffer.
		return rootHook(u, rv, data)
	}
	target, err := rootTarget(rv)
	//: a target nothing can be written through.
	if err != nil {
		//: refused.
		return err
	}
	//: the root document into the target.
	return decodeValue(typeDocument, data, target, planFor(target.Type()), decodeState{})
}

// rootTarget follows a pointer to what it points at, or keeps a map, which is
// filled in place.
func rootTarget(rv reflect.Value) (reflect.Value, error) {
	//: a non-nil pointer or map.
	if (rv.Kind() == reflect.Pointer || rv.Kind() == reflect.Map) && !rv.IsNil() {
		//: a pointer is followed.
		if rv.Kind() == reflect.Pointer {
			//: what it points at.
			return rv.Elem(), nil
		}
		//: the map itself.
		return rv, nil
	}
	//: nil, or a value, which cannot be written through.
	return rv, unmarshalError(nil, "the target must be a non-nil pointer or map, not "+describeTarget(rv))
}

// describeTarget names a refused target's type, or nil.
func describeTarget(rv reflect.Value) string {
	//: a nil pointer or map still has a type.
	if (rv.Kind() == reflect.Pointer || rv.Kind() == reflect.Map) && rv.IsNil() {
		//: named as nil.
		return "a nil " + rv.Type().String()
	}
	//: any other value.
	return rv.Type().String()
}

// rootHook calls a target's own UnmarshalBSON with a copy of the document.
func rootHook(u interface{ UnmarshalBSON(data []byte) error }, rv reflect.Value, data []byte) error {
	//: a nil pointer's method is not called.
	if rv.Kind() == reflect.Pointer && rv.IsNil() {
		//: refused.
		return unmarshalError(nil, "the target is a nil "+rv.Type().String())
	}
	//: the method's own failure.
	if err := u.UnmarshalBSON(bytes.Clone(data)); err != nil {
		//: wrapped; an SDK error keeps its code.
		return unmarshalError(err, "UnmarshalBSON of "+rv.Type().String()+" failed")
	}
	//: decoded.
	return nil
}

// decodeValue decodes the value of type t, whose bytes are val, into rv.
func decodeValue(t byte, val []byte, rv reflect.Value, p *typePlan, st decodeState) error {
	//: the type's own UnmarshalBSON comes first.
	if p.unmarshaler != hookNone {
		//: called when reachable; otherwise the kind decides.
		if handled, err := decodeHook(t, val, rv, p); handled {
			//: its verdict.
			return err
		}
	}
	//: the scalars.
	if handled, err := decodeScalar(t, val, rv, p, st); handled {
		//: decoded.
		return err
	}
	//: the containers.
	if handled, err := decodeContainer(t, val, rv, p, st); handled {
		//: decoded.
		return err
	}
	//: the codec's own value types and the stdlib types.
	return decodeSpecial(t, val, rv, p, st)
}

// decodeContainer decodes into the container mappings and the indirections,
// and reports whether p was one.
func decodeContainer(t byte, val []byte, rv reflect.Value, p *typePlan, st decodeState) (bool, error) {
	//: one rule per container mapping.
	switch p.kind {
	case kindByteSlice, kindSlice, kindDocSlice:
		return true, decodeSlice(t, val, rv, p, st)
	case kindByteArray, kindArray, kindDocArray:
		return true, decodeArray(t, val, rv, p, st)
	case kindD:
		return true, decodeD(t, val, rv, p, st)
	case kindMap:
		return true, decodeMap(t, val, rv, p, st)
	case kindStruct:
		return true, decodeStruct(t, val, rv, p, st)
	case kindPointer:
		return true, decodePointer(t, val, rv, p, st)
	case kindAny:
		return true, decodeInterface(t, val, rv, st)
	default:
		//: not a container.
		return false, nil
	}
}

// decodeHook calls UnmarshalBSON with a copy of the value's bytes: on a
// pointer type it allocates a nil target first and leaves a null value nil;
// on a type whose pointer has the method, through the address.
func decodeHook(t byte, val []byte, rv reflect.Value, p *typePlan) (bool, error) {
	target, handled := hookTarget(val, rv, p)
	//: not reachable, or already decided.
	if !handled || !target.IsValid() {
		//: the kind decides, or the null was taken.
		return handled, nil
	}
	u, ok := methodValue(target).(interface{ UnmarshalBSON(data []byte) error })
	//: an interface holding nothing.
	if !ok {
		//: refused.
		return true, unmarshalError(nil, "cannot decode a BSON "+bsonTypeName(t)+" into a nil "+p.typ.String())
	}
	//: the method's own failure.
	if err := u.UnmarshalBSON(cloneOrNil(val)); err != nil {
		//: wrapped; an SDK error keeps its code.
		return true, unmarshalError(err, "UnmarshalBSON of "+p.typ.String()+" failed")
	}
	//: decoded.
	return true, nil
}

// hookTarget returns what UnmarshalBSON is called on: the address for a
// pointer receiver (handled is false when there is none); for a pointer type,
// the pointer, allocated when nil, or the invalid Value after setting it to
// nil for a value-less BSON type.
func hookTarget(val []byte, rv reflect.Value, p *typePlan) (reflect.Value, bool) {
	//: a pointer receiver needs an address.
	if p.unmarshaler == hookPointer {
		//: not addressable: the kind decides.
		if !rv.CanAddr() {
			//: not handled.
			return reflect.Value{}, false
		}
		//: the address.
		return rv.Addr(), true
	}
	//: a value receiver on a value type: the value.
	if rv.Kind() != reflect.Pointer {
		//: as it is.
		return rv, true
	}
	//: a value-less BSON type leaves the pointer nil.
	if len(val) == 0 {
		rv.SetZero()
		//: decided, with nothing to call.
		return reflect.Value{}, true
	}
	//: a nil pointer gets a target.
	if rv.IsNil() {
		rv.Set(reflect.New(rv.Type().Elem()))
	}
	//: the pointer.
	return rv, true
}

// cloneOrNil copies b, or returns nil for an empty b.
func cloneOrNil(b []byte) []byte {
	//: nothing to copy.
	if len(b) == 0 {
		//: nil.
		return nil
	}
	//: a copy the callee may keep.
	return bytes.Clone(b)
}

// mismatch reports a BSON type the target cannot hold.
func mismatch(t byte, p *typePlan, st decodeState) error {
	//: the BSON type, the Go type, and the field when known.
	return unmarshalError(nil, inField("cannot decode a BSON "+bsonTypeName(t)+" into "+p.typ.String(), st))
}

// inField appends the innermost struct field's name to a message, when there
// is one.
func inField(message string, st decodeState) string {
	//: outside any struct.
	if st.field == "" {
		//: the message as it is.
		return message
	}
	//: the Go field name.
	return message + " (field " + st.field + ")"
}

// docIter walks the elements of a validated document.
type docIter struct {
	// body is the element list, without the length prefix and terminator.
	body []byte
	// pos is where the next element starts.
	pos int
}

// newDocIter starts a walk over doc, a whole document.
func newDocIter(doc []byte) docIter {
	//: a document shorter than the empty one has no elements.
	if len(doc) < minDocumentSize {
		//: an exhausted walk.
		return docIter{}
	}
	//: between the prefix and the terminator.
	return docIter{body: doc[lengthSize : len(doc)-1]}
}

// next returns the next element. more is false at the end; err reports bytes
// that are not the validated document any more.
func (it *docIter) next() (el element, more bool, err error) {
	//: the end of the element list.
	if it.pos >= len(it.body) {
		//: no more elements.
		return element{}, false, nil
	}
	el.typ = it.body[it.pos]
	rest := it.body[it.pos+1:]
	keyLen := cstringEnd(rest)
	//: the name must still be terminated.
	if keyLen < 0 {
		//: changed under us.
		return element{}, false, errCorrupt()
	}
	el.key = rest[:keyLen]
	rest = rest[keyLen+1:]
	size := valueSize(el.typ, rest)
	//: the value must still fit.
	if size < 0 {
		//: changed under us.
		return element{}, false, errCorrupt()
	}
	el.value = rest[:size]
	it.pos += 1 + keyLen + 1 + size
	//: the element.
	return el, true, nil
}

// countElements counts a validated document's elements.
func countElements(doc []byte) int {
	it := newDocIter(doc)
	n := 0
	//: element by element.
	for {
		_, more, err := it.next()
		//: the end, or bytes that changed: the count so far.
		if !more || err != nil {
			//: counted.
			return n
		}
		n++
	}
}

// decodePointer decodes into a pointer's target, allocating it when nil; a
// null or undefined sets the pointer to nil.
func decodePointer(t byte, val []byte, rv reflect.Value, p *typePlan, st decodeState) error {
	//: no value: a nil pointer.
	if t == typeNull || t == typeUndefined {
		rv.SetZero()
		//: decoded.
		return nil
	}
	//: a nil pointer gets a target; an existing one is decoded into.
	if rv.IsNil() {
		rv.Set(reflect.New(p.typ.Elem()))
	}
	//: the target.
	return decodeValue(t, val, rv.Elem(), p.elem, st)
}

// decodeInterface decodes into an empty interface: the value its BSON type
// decodes as on its own.
func decodeInterface(t byte, val []byte, rv reflect.Value, st decodeState) error {
	value, err := decodeAny(t, val, st)
	//: the value's own failure.
	if err != nil {
		//: refused.
		return err
	}
	//: a null is the nil interface.
	if value == nil {
		rv.SetZero()
		//: decoded.
		return nil
	}
	rv.Set(reflect.ValueOf(value))
	//: decoded.
	return nil
}
