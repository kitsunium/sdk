// Package msgpack — the struct encoder and the omitempty rule. A struct is a
// map of its layout's keys in declaration order, each key appended as bytes
// encoded once per type. omitempty is decided by the vendor-backed codec's
// rule: an IsZero() method when the value has one (a nil pointer to such a
// type is empty), else the kind's zero — no elements, false, 0, nil — and a
// struct is empty when every one of its fields would be left out.
//
// One quirk is kept on purpose because the wire shows it: a field inlined
// from an embedded struct POINTER that is nil is written as nil — unless the
// struct has an omitempty field anywhere, in which case such fields are left
// out. testdata/vendor-golden.txt pins the first case.
package msgpack

import (
	"reflect"
)

// newStructEncoder builds the encoder of struct type t from its layout.
func newStructEncoder(t reflect.Type) encodeFunc {
	layout := layoutOf(t)
	//: a type with two fields on one key is refused on every encode.
	if err := layout.defect(t, true); err != nil {
		//: decided once.
		return failingEncoder(err)
	}
	s := &structEncoder{
		fields:       make([]fieldEncoder, len(layout.fields)),
		asArray:      layout.asArray,
		hasOmitEmpty: layout.hasOmitEmpty,
	}
	//: one encoder per field, built (or found) once.
	for i, f := range layout.fields {
		s.fields[i] = fieldEncoder{field: f, enc: encoderFor(f.typ), empty: emptyFor(f.typ)}
	}
	//: the plan.
	return s.encode
}

// encode writes the struct as a map, or as an array with as_array.
func (s *structEncoder) encode(b []byte, v reflect.Value, depth int) ([]byte, error) {
	//: a struct is a container level.
	if depth >= maxDepth {
		//: absurd nesting.
		return b, depthExceeded(true)
	}
	//: the array form ignores omitempty.
	if s.asArray {
		//: every field, in order.
		return s.encodeArray(b, v, depth)
	}
	//: the map form.
	return s.encodeMap(b, v, depth)
}

// encodeArray writes every field, in order, as an array.
func (s *structEncoder) encodeArray(b []byte, v reflect.Value, depth int) ([]byte, error) {
	b = appendLength(b, len(s.fields), &arrayForms)
	//: one element per field.
	for i := range s.fields {
		var err error
		b, err = s.fields[i].encodeValue(b, v, depth)
		//: the first failure wins.
		if err != nil {
			//: stop.
			return b, err
		}
	}
	//: the whole array.
	return b, nil
}

// encodeMap writes the fields that are not left out, as a map.
func (s *structEncoder) encodeMap(b []byte, v reflect.Value, depth int) ([]byte, error) {
	n := len(s.fields)
	//: omitempty needs the count of kept fields before the header.
	if s.hasOmitEmpty {
		n = s.keptCount(v)
	}
	b = appendLength(b, n, &mapForms)
	//: key then value, for each kept field.
	for i := range s.fields {
		f := &s.fields[i]
		//: a left-out field writes nothing.
		if s.hasOmitEmpty && f.omitted(v) {
			continue
		}
		b = append(b, f.field.wireName...)
		var err error
		b, err = f.encodeValue(b, v, depth)
		//: the first failure wins.
		if err != nil {
			//: stop.
			return b, err
		}
	}
	//: every kept field written.
	return b, nil
}

// keptCount counts the fields omitempty does not leave out.
func (s *structEncoder) keptCount(v reflect.Value) int {
	n := 0
	//: one test per field.
	for i := range s.fields {
		//: count the kept ones.
		if !s.fields[i].omitted(v) {
			n++
		}
	}
	//: the map's size.
	return n
}

// omitted reports whether the field is left out of struct value v: when it is
// empty under omitempty, or when it sits behind a nil embedded pointer.
func (f *fieldEncoder) omitted(v reflect.Value) bool {
	fv, ok := fieldValue(v, f.field.index)
	//: behind a nil embedded pointer.
	if !ok {
		//: left out, as the vendor did on this path.
		return true
	}
	//: omitempty and empty.
	return f.field.omitEmpty && f.empty(fv)
}

// encodeValue writes the field's value from struct value v.
func (f *fieldEncoder) encodeValue(b []byte, v reflect.Value, depth int) ([]byte, error) {
	fv, ok := fieldValue(v, f.field.index)
	//: behind a nil embedded pointer, the field has no value.
	if !ok {
		//: nil.
		return appendNil(b), nil
	}
	//: the field's own encoder.
	return f.enc(b, fv, depth+1)
}

// fieldValue follows index from struct value v, through embedded pointers,
// and reports false when a nil one stands in the way.
func fieldValue(v reflect.Value, index []int) (reflect.Value, bool) {
	//: a field of the struct itself.
	if len(index) == 1 {
		//: direct.
		return v.Field(index[0]), true
	}
	//: an inlined field: one embedding per step.
	for i, x := range index {
		//: every step after the first may cross a pointer.
		if i > 0 && v.Kind() == reflect.Pointer {
			//: a nil embedding has no fields.
			if v.IsNil() {
				//: no value.
				return reflect.Value{}, false
			}
			v = v.Elem()
		}
		v = v.Field(x)
	}
	//: the field.
	return v, true
}

// emptyFor returns the omitempty test of type t.
func emptyFor(t reflect.Type) emptyFunc {
	//: an IsZero method decides first; an interface is decided at run time.
	if t.Kind() != reflect.Interface && t.Implements(isZeroerType) {
		//: the type's own notion of zero.
		return zeroerEmpty
	}
	//: otherwise the kind's zero.
	return kindEmpty(t.Kind())
}

// kindEmpty returns the omitempty test of a kind.
func kindEmpty(k reflect.Kind) emptyFunc {
	//: one test per kind family.
	switch k {
	//: no elements.
	case reflect.Array, reflect.Map, reflect.Slice, reflect.String:
		return lenEmpty
	//: false, 0, 0.0 — and −0.0, which compares equal to 0.
	case reflect.Bool, reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr,
		reflect.Float32, reflect.Float64:
		return scalarEmpty
	//: nil.
	case reflect.Pointer:
		return reflect.Value.IsNil
	//: decided by the dynamic value.
	case reflect.Interface:
		return isEmptyValue
	//: every field left out.
	case reflect.Struct:
		return structEmpty
	//: anything else is never empty.
	default:
		return neverEmpty
	}
}

// scalarEmpty reports whether a bool or number is false or compares equal to
// zero — so a negative zero is empty, as the vendor-backed codec decided.
func scalarEmpty(v reflect.Value) bool {
	//: one comparison per kind family.
	switch v.Kind() {
	//: false.
	case reflect.Bool:
		return !v.Bool()
	//: signed zero.
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return v.Int() == 0
	//: floating zero, either sign.
	case reflect.Float32, reflect.Float64:
		return v.Float() == 0
	//: unsigned zero.
	default:
		return v.Uint() == 0
	}
}

// isEmptyValue is the omitempty test decided on the value's dynamic type.
func isEmptyValue(v reflect.Value) bool {
	//: look through interfaces.
	for v.Kind() == reflect.Interface {
		//: a nil interface is empty.
		if v.IsNil() {
			//: empty.
			return true
		}
		v = v.Elem()
	}
	//: the dynamic type's test.
	return emptyFor(v.Type())(v)
}

// zeroerEmpty asks the value's IsZero method; a nil pointer or map is empty
// without asking.
func zeroerEmpty(v reflect.Value) bool {
	//: a nil pointer, map, slice… has nothing to ask.
	if isNilable(v.Kind()) && v.IsNil() {
		//: empty.
		return true
	}
	z, ok := interfaceOf[isZeroer](v)
	//: a value reached through an unexported embedding falls back to its kind.
	if !ok {
		//: the kind's zero.
		return kindEmpty(v.Kind())(v)
	}
	//: the type's own notion of zero.
	return z.IsZero()
}

// lenEmpty reports whether a string, slice, map or array has no elements.
func lenEmpty(v reflect.Value) bool {
	//: no elements.
	return v.Len() == 0
}

// neverEmpty is the test of a kind omitempty never leaves out.
func neverEmpty(_ reflect.Value) bool {
	//: channels and functions are never left out.
	return false
}

// structEmpty reports whether every field of a struct value would be left
// out, which with no omitempty field means only a struct with no fields.
func structEmpty(v reflect.Value) bool {
	layout := layoutOf(v.Type())
	//: without omitempty, any field is kept.
	if !layout.hasOmitEmpty {
		//: only a fieldless struct is empty.
		return len(layout.fields) == 0
	}
	//: one kept field makes it non-empty.
	for _, f := range layout.fields {
		fv, ok := fieldValue(v, f.index)
		//: kept: reachable and not (omitempty and empty).
		if ok && (!f.omitEmpty || !isEmptyValue(fv)) {
			//: not empty.
			return false
		}
	}
	//: every field left out.
	return true
}

// isNilable reports whether a kind has a nil value.
func isNilable(k reflect.Kind) bool {
	//: the kinds IsNil accepts.
	switch k {
	//: reference kinds.
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return true
	//: values.
	default:
		return false
	}
}
