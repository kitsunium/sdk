// Package msgpack — map encoders. Pairs are written in Go's map iteration
// order, which is not deterministic, exactly as the vendor-backed codec wrote
// them: a caller who needs stable bytes encodes a struct, or a slice of pairs.
//
// The string-keyed maps a program builds most — map[string]any,
// map[string]string, map[string]int, map[string]int64, map[string]float64 and
// map[string]bool — are ranged over directly instead of through reflection,
// which is what makes Marshal of such a map cost one allocation (the result).
package msgpack

import (
	"reflect"
)

// newMapEncoder builds the encoder of map type t.
func newMapEncoder(t reflect.Type) encodeFunc {
	generic := &mapEncoder{
		keyType:  t.Key(),
		elemType: t.Elem(),
		key:      encoderFor(t.Key()),
		elem:     encoderFor(t.Elem()),
	}
	//: the common string-keyed maps range directly.
	if fast := stringMapEncoder(t, generic.encode); fast != nil {
		//: direct range, reflection as the fallback.
		return fast
	}
	//: any other map goes through reflection.
	return generic.encode
}

// stringMapEncoder returns the direct encoder of one of the common
// string-keyed map types, or nil. Only the exact unnamed types qualify; a
// named map type takes the reflective path, which writes the same bytes.
func stringMapEncoder(t reflect.Type, fallback encodeFunc) encodeFunc {
	//: one instantiation per supported element type.
	switch t {
	//: the document shape.
	case reflect.TypeFor[map[string]any]():
		return directMapEncoder(appendAnyElem, fallback)
	//: labels, headers, environment.
	case reflect.TypeFor[map[string]string]():
		return directMapEncoder(appendStringElem, fallback)
	//: counters.
	case reflect.TypeFor[map[string]int]():
		return directMapEncoder(appendIntElem[int], fallback)
	//: wide counters.
	case reflect.TypeFor[map[string]int64]():
		return directMapEncoder(appendIntElem[int64], fallback)
	//: measurements.
	case reflect.TypeFor[map[string]float64]():
		return directMapEncoder(appendFloat64Elem, fallback)
	//: flags.
	case reflect.TypeFor[map[string]bool]():
		return directMapEncoder(appendBoolElem, fallback)
	//: not one of them.
	default:
		return nil
	}
}

// directMapEncoder returns an encoder that ranges over a map[string]V with a
// plain Go loop, falling back to reflection for a map reached through an
// unexported field (whose value reflection may not hand out).
func directMapEncoder[V any](elem func([]byte, V, int) ([]byte, error), fallback encodeFunc) encodeFunc {
	//: the closure is the plan.
	return func(b []byte, v reflect.Value, depth int) ([]byte, error) {
		m, ok := interfaceOf[map[string]V](v)
		//: a read-only map is ranged through reflection.
		if !ok {
			//: same bytes, slower path.
			return fallback(b, v, depth)
		}
		//: the direct loop.
		return appendStringMap(b, m, elem, depth)
	}
}

// appendStringMap writes a map[string]V: nil for a nil map, else a map.
func appendStringMap[V any](b []byte, m map[string]V, elem func([]byte, V, int) ([]byte, error), depth int) ([]byte, error) {
	//: nil and empty differ on the wire.
	if m == nil {
		//: nil.
		return appendNil(b), nil
	}
	//: one level deeper.
	if depth >= maxDepth {
		//: absurd nesting.
		return b, depthExceeded(true)
	}
	b, err := appendMapHeader(b, len(m))
	//: refuse a count the wire cannot carry.
	if err != nil {
		//: b unchanged.
		return b, err
	}
	//: key, then value, per pair.
	for k, x := range m {
		b, err = appendString(b, k)
		//: a key the wire cannot carry.
		if err != nil {
			//: the first failure wins.
			return b, err
		}
		b, err = elem(b, x, depth+1)
		//: a value the codec cannot encode.
		if err != nil {
			//: the first failure wins.
			return b, err
		}
	}
	//: every pair written.
	return b, nil
}

// appendAnyElem writes a map[string]any element by its dynamic type.
func appendAnyElem(b []byte, x any, depth int) ([]byte, error) {
	//: the untyped fast paths, then the plans.
	return appendAny(b, x, depth)
}

// appendStringElem writes a map[string]string element.
func appendStringElem(b []byte, s string, _ int) ([]byte, error) {
	//: str.
	return appendString(b, s)
}

// appendIntElem writes a signed element in its shortest form.
func appendIntElem[T int | int64](b []byte, n T, _ int) ([]byte, error) {
	//: shortest form, as every integer.
	return appendInt(b, int64(n)), nil
}

// appendFloat64Elem writes a float64 element.
func appendFloat64Elem(b []byte, f float64, _ int) ([]byte, error) {
	//: float 64.
	return appendFloat64(b, f), nil
}

// appendBoolElem writes a bool element.
func appendBoolElem(b []byte, x bool, _ int) ([]byte, error) {
	//: true or false.
	return appendBool(b, x), nil
}

// encode writes any map through reflection.
func (m *mapEncoder) encode(b []byte, v reflect.Value, depth int) ([]byte, error) {
	//: nil and empty differ on the wire.
	if v.IsNil() {
		//: nil.
		return appendNil(b), nil
	}
	//: one level deeper.
	if depth >= maxDepth {
		//: a cycle through maps, or absurd nesting.
		return b, depthExceeded(true)
	}
	b, err := appendMapHeader(b, v.Len())
	//: refuse a count the wire cannot carry.
	if err != nil {
		//: b unchanged.
		return b, err
	}
	iter := v.MapRange()
	//: a map handed out normally is read into two reusable values; one
	//: reached through an unexported field only through copies.
	if v.CanInterface() {
		//: no allocation per pair.
		return m.encodePairsInPlace(b, iter, depth)
	}
	//: copies, read-only.
	return m.encodePairsCopied(b, iter, depth)
}

// encodePairsInPlace writes every pair through two values reused for the whole
// map, so a pair costs no allocation.
func (m *mapEncoder) encodePairsInPlace(b []byte, iter *reflect.MapIter, depth int) ([]byte, error) {
	key := reflect.New(m.keyType).Elem()
	val := reflect.New(m.elemType).Elem()
	//: one pair per step.
	for iter.Next() {
		key.SetIterKey(iter)
		val.SetIterValue(iter)
		var err error
		b, err = m.encodePair(b, key, val, depth)
		//: the first failure wins.
		if err != nil {
			//: stop.
			return b, err
		}
	}
	//: every pair written.
	return b, nil
}

// encodePairsCopied writes every pair from the iterator's own copies.
func (m *mapEncoder) encodePairsCopied(b []byte, iter *reflect.MapIter, depth int) ([]byte, error) {
	//: one pair per step.
	for iter.Next() {
		var err error
		b, err = m.encodePair(b, iter.Key(), iter.Value(), depth)
		//: the first failure wins.
		if err != nil {
			//: stop.
			return b, err
		}
	}
	//: every pair written.
	return b, nil
}

// encodePair writes one key and its value.
func (m *mapEncoder) encodePair(b []byte, key, val reflect.Value, depth int) ([]byte, error) {
	b, err := m.key(b, key, depth+1)
	//: a key the codec cannot encode.
	if err != nil {
		//: stop.
		return b, err
	}
	//: then the value.
	return m.elem(b, val, depth+1)
}
