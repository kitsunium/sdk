// Package cbor — the kind encoders of the scalars: booleans, integers of
// every width, floats and strings, each answering omitempty for its kind.
package cbor

import "reflect"

// boolEncoder encodes a bool.
type boolEncoder struct{}

// intEncoder encodes a signed integer of any width.
type intEncoder struct{}

// uintEncoder encodes an unsigned integer of any width.
type uintEncoder struct{}

// floatEncoder encodes a float32 as a single and a float64 as a double.
type floatEncoder struct{}

// stringEncoder encodes a string as a text string.
type stringEncoder struct{}

// encode appends true or false.
func (boolEncoder) encode(b []byte, v reflect.Value, _ *encodePlan, _ walkDepth) ([]byte, error) {
	//: one byte.
	return appendBool(b, v.Bool()), nil
}

// empty is true for false.
func (boolEncoder) empty(v reflect.Value, _ *encodePlan) (bool, error) {
	//: false.
	return !v.Bool(), nil
}

// encode appends major type 0 or 1 in the shortest head.
func (intEncoder) encode(b []byte, v reflect.Value, _ *encodePlan, _ walkDepth) ([]byte, error) {
	//: signed.
	return appendInt(b, v.Int()), nil
}

// empty is true for 0.
func (intEncoder) empty(v reflect.Value, _ *encodePlan) (bool, error) {
	//: zero.
	return v.Int() == 0, nil
}

// encode appends major type 0 in the shortest head.
func (uintEncoder) encode(b []byte, v reflect.Value, _ *encodePlan, _ walkDepth) ([]byte, error) {
	//: unsigned.
	return appendHead(b, majorUnsigned, v.Uint()), nil
}

// empty is true for 0.
func (uintEncoder) empty(v reflect.Value, _ *encodePlan) (bool, error) {
	//: zero.
	return v.Uint() == 0, nil
}

// encode appends a single for a float32 and a double for a float64.
func (floatEncoder) encode(b []byte, v reflect.Value, _ *encodePlan, _ walkDepth) ([]byte, error) {
	//: the width follows the Go type.
	if v.Kind() == reflect.Float32 {
		//: a single.
		return appendFloat32(b, float32(v.Float())), nil
	}
	//: a double.
	return appendFloat64(b, v.Float()), nil
}

// empty is true for 0 and −0.
func (floatEncoder) empty(v reflect.Value, _ *encodePlan) (bool, error) {
	//: zero of either sign.
	return v.Float() == 0, nil
}

// encode appends a text string, refusing one that is not UTF-8.
func (stringEncoder) encode(b []byte, v reflect.Value, _ *encodePlan, _ walkDepth) ([]byte, error) {
	//: UTF-8 checked.
	return appendText(b, v.String())
}

// empty is true when the string has no element.
func (stringEncoder) empty(v reflect.Value, _ *encodePlan) (bool, error) {
	//: nothing in it.
	return v.Len() == 0, nil
}
