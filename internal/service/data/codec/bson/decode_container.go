package bson

import (
	"bytes"
	"encoding"
	"reflect"
	"strconv"
)

// decodeSlice decodes into a slice: an array element by element, a document
// into a slice of E, a binary or a string into a slice of bytes.
func decodeSlice(t byte, val []byte, rv reflect.Value, p *typePlan, st decodeState) error {
	//: null and undefined leave a nil slice.
	if t == typeNull || t == typeUndefined {
		rv.SetZero()
		//: decoded.
		return nil
	}
	//: one rule per slice mapping.
	switch p.kind {
	case kindDocSlice:
		return decodeDocSlice(t, val, rv, p, st)
	case kindByteSlice:
		return decodeBytesSlice(t, val, rv, p, st)
	default:
		return decodeArrayIntoSlice(t, val, rv, p, st)
	}
}

// decodeDocSlice decodes a document into a slice of E; an array is refused.
func decodeDocSlice(t byte, val []byte, rv reflect.Value, p *typePlan, st decodeState) error {
	//: only a document is a slice of E.
	if t != typeDocument {
		//: refused.
		return mismatch(t, p, st)
	}
	st.ancestor = p.typ
	//: element by element, with this slice's type as the ancestor.
	return decodeESlice(val, rv, st)
}

// decodeBytesSlice decodes an array, a generic binary or a string into a
// slice of bytes.
func decodeBytesSlice(t byte, val []byte, rv reflect.Value, p *typePlan, st decodeState) error {
	//: an array, element by element.
	if t == typeArray {
		//: decoded as any slice.
		return decodeElements(val, rv, p, st)
	}
	//: a binary or a string, as bytes.
	if t == typeBinary || t == typeString {
		//: decoded in one copy.
		return decodeByteSlice(t, val, rv, p, st)
	}
	//: anything else does not fit.
	return mismatch(t, p, st)
}

// decodeArrayIntoSlice decodes an array into a slice of anything but bytes
// and E.
func decodeArrayIntoSlice(t byte, val []byte, rv reflect.Value, p *typePlan, st decodeState) error {
	//: only an array.
	if t != typeArray {
		//: refused.
		return mismatch(t, p, st)
	}
	//: element by element.
	return decodeElements(val, rv, p, st)
}

// reusableSlice returns rv resliced to n when its backing array holds n
// elements, a new slice of n otherwise: a nil slice becomes an empty one, as
// the previous library made it.
func reusableSlice(rv reflect.Value, n int) reflect.Value {
	//: a nil or short slice is replaced.
	if rv.IsNil() || rv.Cap() < n {
		//: a fresh one.
		return reflect.MakeSlice(rv.Type(), n, n)
	}
	//: the existing backing array.
	return rv.Slice(0, n)
}

// decodeElements decodes an array into a slice, each element into a zeroed
// slot.
func decodeElements(val []byte, rv reflect.Value, p *typePlan, st decodeState) error {
	n := countElements(val)
	s := reusableSlice(rv, n)
	it := newDocIter(val)
	//: element by element, into fresh slots.
	for i := 0; ; i++ {
		el, more, err := it.next()
		//: the end, or bytes that changed.
		if !more || err != nil {
			rv.Set(s)
			//: decoded.
			return err
		}
		//: the slice was sized by the count.
		if i >= n {
			//: changed under us.
			return errCorrupt()
		}
		slot := s.Index(i)
		slot.SetZero()
		//: the element's own failure.
		if err := decodeValue(el.typ, el.value, slot, p.elem, st); err != nil {
			//: refused.
			return err
		}
	}
}

// decodeESlice decodes a document into a slice of E, in order, each value
// decoded as an interface would be.
func decodeESlice(val []byte, rv reflect.Value, st decodeState) error {
	n := countElements(val)
	s := reusableSlice(rv, n)
	it := newDocIter(val)
	//: element by element.
	for i := 0; ; i++ {
		el, more, err := it.next()
		//: the end, or bytes that changed.
		if !more || err != nil {
			rv.Set(s)
			//: decoded.
			return err
		}
		//: the slice was sized by the count.
		if i >= n {
			//: changed under us.
			return errCorrupt()
		}
		//: the element's own failure.
		if err := setElement(s.Index(i), el, st); err != nil {
			//: refused.
			return err
		}
	}
}

// setElement decodes el into an E reached through reflection.
func setElement(target reflect.Value, el element, st decodeState) error {
	value, err := decodeAny(el.typ, el.value, st)
	//: the value's own failure.
	if err != nil {
		//: refused.
		return err
	}
	name := string(el.key)
	target.Field(0).SetString(name)
	//: a nil value is the zero interface.
	if value == nil {
		target.Field(1).SetZero()
		//: set.
		return nil
	}
	target.Field(1).Set(reflect.ValueOf(value))
	//: set.
	return nil
}

// decodeByteSlice decodes a generic binary or a string into a slice of bytes,
// reusing its backing array.
func decodeByteSlice(t byte, val []byte, rv reflect.Value, p *typePlan, st decodeState) error {
	data, err := bytesOf(t, val, p, st)
	//: not a generic binary.
	if err != nil {
		//: refused.
		return err
	}
	var dst []byte
	//: a nil slice becomes an empty one.
	if rv.IsNil() {
		dst = make([]byte, 0, len(data))
	} else {
		dst = rv.Bytes()[:0]
	}
	rv.SetBytes(append(dst, data...))
	//: decoded.
	return nil
}

// bytesOf returns the payload of a generic binary or of a string.
func bytesOf(t byte, val []byte, p *typePlan, st decodeState) ([]byte, error) {
	//: a binary of a generic subtype.
	if t == typeBinary {
		//: its bytes.
		return genericBinary(val, p, st)
	}
	text, ok := stringPayload(val)
	//: changed under us.
	if !ok {
		//: refused.
		return nil, errCorrupt()
	}
	//: the string's bytes.
	return text, nil
}

// decodeArray decodes into an array: an array element by element, a document
// into an array of E, a generic binary into an array of bytes. A value
// shorter than the array leaves the rest of it as it was; a longer one is
// refused.
func decodeArray(t byte, val []byte, rv reflect.Value, p *typePlan, st decodeState) error {
	//: null and undefined zero the array.
	if t == typeNull || t == typeUndefined {
		rv.SetZero()
		//: decoded.
		return nil
	}
	//: one rule per array mapping.
	switch p.kind {
	case kindDocArray:
		return decodeDocArray(t, val, rv, p, st)
	case kindByteArray:
		return decodeBytesArray(t, val, rv, p, st)
	default:
		return decodeArrayIntoArray(t, val, rv, p, st)
	}
}

// decodeDocArray decodes a document into an array of E. As the previous
// library did, it does not make the array's type the ancestor.
func decodeDocArray(t byte, val []byte, rv reflect.Value, p *typePlan, st decodeState) error {
	//: only a document.
	if t != typeDocument {
		//: refused.
		return mismatch(t, p, st)
	}
	//: more elements than the array holds.
	if countElements(val) > rv.Len() {
		//: refused.
		return unmarshalError(nil, inField("a document has more elements than "+p.typ.String()+" holds", st))
	}
	it := newDocIter(val)
	//: element by element.
	for i := 0; ; i++ {
		el, more, err := it.next()
		//: the end, or bytes that changed.
		if !more || err != nil {
			//: decoded.
			return err
		}
		//: the array was checked to be long enough.
		if i >= rv.Len() {
			//: changed under us.
			return errCorrupt()
		}
		//: the element's own failure.
		if err := setElement(rv.Index(i), el, st); err != nil {
			//: refused.
			return err
		}
	}
}

// decodeBytesArray decodes an array or a generic binary into an array of
// bytes.
func decodeBytesArray(t byte, val []byte, rv reflect.Value, p *typePlan, st decodeState) error {
	//: a binary, in one copy.
	if t == typeBinary {
		//: decoded.
		return decodeByteArray(val, rv, p, st)
	}
	//: an array, element by element.
	return decodeArrayIntoArray(t, val, rv, p, st)
}

// decodeArrayIntoArray decodes an array's elements into an array.
func decodeArrayIntoArray(t byte, val []byte, rv reflect.Value, p *typePlan, st decodeState) error {
	//: only an array.
	if t != typeArray {
		//: refused.
		return mismatch(t, p, st)
	}
	//: more elements than the array holds.
	if countElements(val) > rv.Len() {
		//: refused.
		return unmarshalError(nil, inField("an array has more elements than "+p.typ.String()+" holds", st))
	}
	it := newDocIter(val)
	//: element by element.
	for i := 0; ; i++ {
		el, more, err := it.next()
		//: the end, or bytes that changed.
		if !more || err != nil {
			//: decoded.
			return err
		}
		//: the array was checked to be long enough.
		if i >= rv.Len() {
			//: changed under us.
			return errCorrupt()
		}
		slot := rv.Index(i)
		slot.SetZero()
		//: the element's own failure.
		if err := decodeValue(el.typ, el.value, slot, p.elem, st); err != nil {
			//: refused.
			return err
		}
	}
}

// decodeByteArray copies a generic binary into an array of bytes.
func decodeByteArray(val []byte, rv reflect.Value, p *typePlan, st decodeState) error {
	data, err := genericBinary(val, p, st)
	//: not a generic binary.
	if err != nil {
		//: refused.
		return err
	}
	//: more bytes than the array holds.
	if len(data) > rv.Len() {
		//: refused.
		return unmarshalError(nil, inField("a binary has more bytes than "+p.typ.String()+" holds", st))
	}
	//: byte by byte, so an unaddressable array is handled too.
	for i, b := range data {
		rv.Index(i).SetUint(uint64(b))
	}
	//: decoded.
	return nil
}

// decodeD decodes a document into a D, reusing its backing array. A null
// leaves it nil; an undefined is refused, as the previous library refused it.
func decodeD(t byte, val []byte, rv reflect.Value, p *typePlan, st decodeState) error {
	//: null leaves a nil D.
	if t == typeNull {
		rv.SetZero()
		//: decoded.
		return nil
	}
	//: only a document is a D.
	if t != typeDocument {
		//: refused.
		return mismatch(t, p, st)
	}
	st.ancestor = typeOfD
	existing, _ := reflect.TypeAssert[D](rv)
	d, err := decodeDocument(val, existing[:0:cap(existing)], st)
	//: an element's own failure.
	if err != nil {
		//: refused.
		return err
	}
	setTyped(rv, d)
	//: decoded.
	return nil
}

// decodeMap decodes a document into a map, adding to what it already holds.
func decodeMap(t byte, val []byte, rv reflect.Value, p *typePlan, st decodeState) error {
	//: null and undefined leave a nil map.
	if t == typeNull || t == typeUndefined {
		rv.SetZero()
		//: decoded.
		return nil
	}
	//: only a document is a map.
	if t != typeDocument {
		//: refused.
		return mismatch(t, p, st)
	}
	//: a map[string]any, or M, without reflection.
	if m, ok := stringAnyMap(rv, val); ok {
		st.ancestor = p.typ
		//: filled in place.
		return fillStringAnyMap(val, m, st)
	}
	//: a nil map is made.
	if rv.IsNil() {
		rv.Set(reflect.MakeMapWithSize(p.typ, countElements(val)))
	}
	//: values of type interface{} make this map's type the ancestor.
	if p.typ.Elem() == typeOfAny {
		st.ancestor = p.typ
	}
	//: through reflection.
	return fillMap(val, rv, p, st)
}

// stringAnyMap returns the map[string]any rv holds, making it if nil, when
// rv's type is map[string]any or M.
func stringAnyMap(rv reflect.Value, val []byte) (map[string]any, bool) {
	//: only the two types the fast path knows.
	if rv.Type() != typeOfStringAnyMap && rv.Type() != typeOfM {
		//: the reflective path.
		return nil, false
	}
	//: a nil map is made, sized by the element count.
	if rv.IsNil() {
		rv.Set(reflect.MakeMapWithSize(rv.Type(), countElements(val)))
	}
	//: the map's own type first.
	if m, ok := reflect.TypeAssert[map[string]any](rv); ok {
		//: found.
		return m, true
	}
	m, ok := reflect.TypeAssert[M](rv)
	//: an M is a map[string]any.
	return m, ok
}

// fillStringAnyMap adds a document's elements to m, each decoded as an
// interface value.
func fillStringAnyMap(val []byte, m map[string]any, st decodeState) error {
	it := newDocIter(val)
	//: element by element; a repeated name keeps the last value.
	for {
		el, more, err := it.next()
		//: the end, or bytes that changed.
		if !more || err != nil {
			//: decoded.
			return err
		}
		//: the value's own failure.
		if err := setStringAnyEntry(m, el, st); err != nil {
			//: refused.
			return err
		}
	}
}

// setStringAnyEntry decodes one element into m.
func setStringAnyEntry(m map[string]any, el element, st decodeState) error {
	value, err := decodeAny(el.typ, el.value, st)
	//: the value's own failure.
	if err != nil {
		//: refused.
		return err
	}
	m[string(el.key)] = value
	//: set.
	return nil
}

// fillMap adds a document's elements to a map through reflection, each value
// decoded into a fresh slot.
func fillMap(val []byte, rv reflect.Value, p *typePlan, st decodeState) error {
	slots := p.acquireSlots()
	defer p.releaseSlots(slots)
	key, slot := slots.key, slots.value
	it := newDocIter(val)
	//: element by element; a repeated name keeps the last value.
	for {
		el, more, err := it.next()
		//: the end, or bytes that changed.
		if !more || err != nil {
			//: decoded.
			return err
		}
		k, err := mapKey(el.key, key, p, st)
		//: a name the key type cannot hold.
		if err != nil {
			//: refused.
			return err
		}
		slot.SetZero()
		//: the value's own failure.
		if err := decodeValue(el.typ, el.value, slot, p.elem, st); err != nil {
			//: refused.
			return err
		}
		rv.SetMapIndex(k, slot)
	}
}

// mapKey turns an element name into a key of the map's key type.
func mapKey(name []byte, slot reflect.Value, p *typePlan, st decodeState) (reflect.Value, error) {
	text := string(name)
	//: one rule per key mode.
	switch p.keyDecode {
	case keyString:
		slot.SetString(text)
		return slot, nil
	case keyInt:
		return intMapKey(text, slot, p, st)
	case keyUint:
		return uintMapKey(text, slot, p, st)
	case keyText:
		return textMapKey(name, p, st)
	case keyUnsupported:
		return slot, unmarshalError(nil, inField("map key type "+p.typ.Key().String()+" has no BSON element-name form", st))
	default:
		//: no other mode exists.
		return slot, unmarshalError(nil, inField("map key type "+p.typ.Key().String()+" has no BSON element-name form", st))
	}
}

// intMapKey parses a decimal signed key that fits the key type.
func intMapKey(text string, slot reflect.Value, p *typePlan, st decodeState) (reflect.Value, error) {
	n, err := strconv.ParseInt(text, 10, 64)
	//: not a decimal that fits the key type.
	if err != nil || slot.OverflowInt(n) {
		//: refused, without quoting the name.
		return slot, unmarshalError(nil, inField("an element name is not a "+p.typ.Key().String()+" map key", st))
	}
	slot.SetInt(n)
	//: the key.
	return slot, nil
}

// uintMapKey parses a decimal unsigned key that fits the key type.
func uintMapKey(text string, slot reflect.Value, p *typePlan, st decodeState) (reflect.Value, error) {
	n, err := strconv.ParseUint(text, 10, 64)
	//: not a decimal that fits the key type.
	if err != nil || slot.OverflowUint(n) {
		//: refused, without quoting the name.
		return slot, unmarshalError(nil, inField("an element name is not a "+p.typ.Key().String()+" map key", st))
	}
	slot.SetUint(n)
	//: the key.
	return slot, nil
}

// textMapKey builds a key through its UnmarshalText method.
func textMapKey(name []byte, p *typePlan, st decodeState) (reflect.Value, error) {
	ptr := reflect.New(p.typ.Key())
	tu, ok := reflect.TypeAssert[encoding.TextUnmarshaler](ptr)
	//: planned as a text key, so the method is there.
	if !ok {
		//: refused.
		return ptr.Elem(), unmarshalError(nil, inField("map key type "+p.typ.Key().String()+" has no UnmarshalText", st))
	}
	//: the method's own failure.
	if err := tu.UnmarshalText(bytes.Clone(name)); err != nil {
		//: wrapped.
		return ptr.Elem(), unmarshalError(err, inField("UnmarshalText of map key type "+p.typ.Key().String()+" failed", st))
	}
	//: the key.
	return ptr.Elem(), nil
}

// decodeStruct decodes a document into a struct, field by field; an element
// no field names goes to the ",inline" map, or is skipped.
func decodeStruct(t byte, val []byte, rv reflect.Value, p *typePlan, st decodeState) error {
	//: null and undefined zero the struct.
	if t == typeNull || t == typeUndefined {
		rv.SetZero()
		//: decoded.
		return nil
	}
	//: only a document is a struct.
	if t != typeDocument {
		//: refused.
		return mismatch(t, p, st)
	}
	//: a struct the plan could not describe.
	if p.invalid != "" {
		//: refused.
		return unmarshalError(nil, p.invalid)
	}
	//: the elements.
	return decodeFields(val, rv, p, st)
}

// decodeFields decodes a document's elements into a struct's fields.
func decodeFields(val []byte, rv reflect.Value, p *typePlan, st decodeState) error {
	it := newDocIter(val)
	next := 0
	//: element by element.
	for {
		el, more, err := it.next()
		//: the end, or bytes that changed.
		if !more || err != nil {
			//: decoded.
			return err
		}
		f := p.fieldFor(el.key, next)
		//: no field: the inline map, or nothing.
		if f == nil {
			//: the element's own failure.
			if err := decodeUnknown(el, rv, p, st); err != nil {
				//: refused.
				return err
			}
			continue
		}
		next = f.position + 1
		//: the field's own failure.
		if err := decodeField(el, rv, f, st); err != nil {
			//: refused.
			return err
		}
	}
}

// fieldFor finds the field an element names, trying first the field that
// follows the last one matched: a document written from this struct lists
// its fields in order, and the guess then saves the map lookup.
func (p *typePlan) fieldFor(key []byte, next int) *fieldPlan {
	//: the guess.
	if next < len(p.fields) && bytes.Equal(key, p.fields[next].nameBytes) {
		//: in order.
		return p.fields[next]
	}
	//: the index.
	return p.lookupField(key)
}

// decodeField decodes an element into a struct field, allocating the
// ",inline" pointers on its path and a nil pointer field, as the previous
// library did; a null then sets that pointer back to nil.
func decodeField(el element, rv reflect.Value, f *fieldPlan, st decodeState) error {
	fv := rv.Field(f.index[0])
	//: the inlined steps.
	for _, i := range f.index[1:] {
		//: an inlined pointer is allocated when nil.
		if fv.Kind() == reflect.Pointer {
			//: allocated.
			if fv.IsNil() {
				fv.Set(reflect.New(fv.Type().Elem()))
			}
			fv = fv.Elem()
		}
		fv = fv.Field(i)
	}
	//: a nil pointer field is allocated before decoding.
	if fv.Kind() == reflect.Pointer && fv.IsNil() {
		fv.Set(reflect.New(fv.Type().Elem()))
	}
	child := decodeState{truncate: st.truncate || f.flags.has(flagTruncate), field: f.goName}
	//: the field's own state: no ancestor, its truncate option, its name.
	return decodeValue(el.typ, el.value, fv, f.plan, child)
}

// decodeUnknown decodes an element no field names into the struct's
// ",inline" map, or skips it when there is none.
func decodeUnknown(el element, rv reflect.Value, p *typePlan, st decodeState) error {
	//: no inline map: the element is skipped.
	if p.inlineMap == nil {
		//: nothing to do.
		return nil
	}
	m := rv.Field(p.inlineMap.index[0])
	mp := p.inlineMap.plan
	//: a nil inline map is made.
	if m.IsNil() {
		m.Set(reflect.MakeMap(mp.typ))
	}
	slot := reflect.New(mp.typ.Elem()).Elem()
	//: the inline map's type is the ancestor, whatever its value type.
	child := decodeState{ancestor: mp.typ, truncate: st.truncate, field: p.inlineMap.goName}
	//: the value's own failure.
	if err := decodeValue(el.typ, el.value, slot, mp.elem, child); err != nil {
		//: refused.
		return err
	}
	name := string(el.key)
	m.SetMapIndex(reflect.ValueOf(name), slot)
	//: decoded.
	return nil
}
