// Package cbor — the kind encoders of pointers, interfaces, byte strings,
// arrays and the types CBOR cannot carry.
package cbor

import "reflect"

// pointerEncoder writes null for a nil pointer and the pointed-to value
// otherwise.
type pointerEncoder struct{}

// interfaceEncoder writes null for a nil interface and its dynamic value
// otherwise.
type interfaceEncoder struct{}

// byteSequenceEncoder writes a []byte or a [N]byte as a byte string.
type byteSequenceEncoder struct{}

// sequenceEncoder writes a slice or an array of anything but bytes as an
// array.
type sequenceEncoder struct{}

// refusedEncoder fails the encoding of a type CBOR cannot carry.
type refusedEncoder struct{}

// encode appends null for nil, the pointed-to value otherwise.
func (pointerEncoder) encode(b []byte, v reflect.Value, p *encodePlan, at walkDepth) ([]byte, error) {
	//: nil is null.
	if v.IsNil() {
		//: null.
		return append(b, nullByte), nil
	}
	next, err := at.follow()
	//: a pointer is one more indirection.
	if err != nil {
		//: a cycle.
		return b, err
	}
	//: the pointed-to value, through its plan.
	return p.elem.kind.encode(b, v.Elem(), p.elem, next)
}

// empty is true for a nil pointer.
func (pointerEncoder) empty(v reflect.Value, _ *encodePlan) (bool, error) {
	//: nil.
	return v.IsNil(), nil
}

// encode appends null for nil, the dynamic value otherwise.
func (interfaceEncoder) encode(b []byte, v reflect.Value, _ *encodePlan, at walkDepth) ([]byte, error) {
	//: nil is null.
	if v.IsNil() {
		//: null.
		return append(b, nullByte), nil
	}
	next, err := at.follow()
	//: an interface is one more indirection.
	if err != nil {
		//: a cycle.
		return b, err
	}
	//: the dynamic value, through the untyped switch when it can be read.
	if v.CanInterface() {
		//: no copy: the interface already holds the value.
		return appendValue(b, v.Interface(), next)
	}
	elem := v.Elem()
	plan := encodePlanFor(elem.Type())
	//: through the dynamic type's plan.
	return plan.kind.encode(b, elem, plan, next)
}

// empty is true for a nil interface.
func (interfaceEncoder) empty(v reflect.Value, _ *encodePlan) (bool, error) {
	//: nil.
	return v.IsNil(), nil
}

// encode appends a byte string; a nil slice is null.
func (byteSequenceEncoder) encode(b []byte, v reflect.Value, _ *encodePlan, _ walkDepth) ([]byte, error) {
	//: a nil slice is null.
	if v.Kind() == reflect.Slice && v.IsNil() {
		//: null.
		return append(b, nullByte), nil
	}
	b = appendHead(b, majorBytes, uint64(v.Len()))
	//: a slice, or an addressable array, exposes its bytes.
	if v.Kind() == reflect.Slice || v.CanAddr() {
		//: one copy.
		return append(b, v.Bytes()...), nil
	}
	//: an array held by value is read byte by byte.
	for i := range v.Len() {
		b = append(b, byte(v.Index(i).Uint()))
	}
	//: the whole string.
	return b, nil
}

// empty is true when the byte string has no element.
func (byteSequenceEncoder) empty(v reflect.Value, _ *encodePlan) (bool, error) {
	//: nothing in it.
	return v.Len() == 0, nil
}

// encode appends an array; a nil slice is null.
func (sequenceEncoder) encode(b []byte, v reflect.Value, p *encodePlan, at walkDepth) ([]byte, error) {
	//: a nil slice is null.
	if v.Kind() == reflect.Slice && v.IsNil() {
		//: null.
		return append(b, nullByte), nil
	}
	inner, err := at.enter()
	//: an array is one level of nesting.
	if err != nil {
		//: too deep.
		return b, err
	}
	b = appendHead(b, majorArray, uint64(v.Len()))
	//: each element through the element plan.
	for i := range v.Len() {
		b, err = p.elem.kind.encode(b, v.Index(i), p.elem, inner)
		//: the first failure ends the encoding.
		if err != nil {
			//: refused.
			return b, err
		}
	}
	//: the whole array.
	return b, nil
}

// empty is true when the array has no element.
func (sequenceEncoder) empty(v reflect.Value, _ *encodePlan) (bool, error) {
	//: nothing in it.
	return v.Len() == 0, nil
}

// encode fails with the refusal the plan was built with.
func (refusedEncoder) encode(b []byte, _ reflect.Value, p *encodePlan, _ walkDepth) ([]byte, error) {
	//: the refusal decided when the plan was built.
	return b, encodeFailure(p.refusal)
}

// empty is never true: the encoding fails anyway.
func (refusedEncoder) empty(_ reflect.Value, _ *encodePlan) (bool, error) {
	//: always written.
	return false, nil
}
