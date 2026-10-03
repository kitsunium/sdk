// Package msgpack — map decoders. A typed map decodes each key and value with
// its own type's decoder, into two values reused for the whole map. A key
// type that can hold an interface (map[any]T, a struct key with an interface
// field) is checked for hashability before insertion: an array decoded as a
// key would otherwise panic inside the runtime's map — the vendor's decoder
// did exactly that on such input.
package msgpack

import (
	"reflect"

	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// Map preallocation bounds.
const (
	// mapPairEstimate is the bytes assumed per reserved pair when sizing a
	// map.
	mapPairEstimate uint64 = 64
	// mapPreallocPairs bounds the pairs a declared count may reserve.
	mapPreallocPairs uint64 = uint64(preallocBytes) / mapPairEstimate
)

// mapDecoder decodes a map pair by pair, through reflection.
type mapDecoder struct {
	// typ is the map type.
	typ reflect.Type
	// keyType is the key type.
	keyType reflect.Type
	// elemType is the element type.
	elemType reflect.Type
	// key decodes one key.
	key decodeFunc
	// elem decodes one element.
	elem decodeFunc
	// checkHashable is set when a decoded key may be unhashable.
	checkHashable bool
}

// newMapDecoder builds the decoder of map type t.
func newMapDecoder(t reflect.Type) decodeFunc {
	//: the common string-keyed maps decode without reflection per pair.
	if fast := stringMapDecoder(t); fast != nil {
		//: direct.
		return fast
	}
	m := &mapDecoder{
		typ:           t,
		keyType:       t.Key(),
		elemType:      t.Elem(),
		key:           decoderFor(t.Key()),
		elem:          decoderFor(t.Elem()),
		checkHashable: mayBeUnhashable(t.Key()),
	}
	//: the plan.
	return m.decode
}

// stringMapDecoder returns the direct decoder of one of the common
// string-keyed map types — the ones encode_map.go ranges over directly — or
// nil. Only the exact unnamed types qualify; a named map type takes the
// reflective path, which decodes the same.
func stringMapDecoder(t reflect.Type) decodeFunc {
	//: one instantiation per supported element type.
	switch t {
	//: the document shape.
	case reflect.TypeFor[map[string]any]():
		return directMapDecoder(decodeAnyElem)
	//: labels, headers, environment.
	case reflect.TypeFor[map[string]string]():
		return directMapDecoder(decodeStringElem)
	//: counters.
	case reflect.TypeFor[map[string]int]():
		return directMapDecoder(decodeIntElem[int])
	//: wide counters.
	case reflect.TypeFor[map[string]int64]():
		return directMapDecoder(decodeIntElem[int64])
	//: measurements.
	case reflect.TypeFor[map[string]float64]():
		return directMapDecoder(decodeFloat64Elem)
	//: flags.
	case reflect.TypeFor[map[string]bool]():
		return directMapDecoder(decodeBoolElem)
	//: not one of them.
	default:
		return nil
	}
}

// decode decodes a map into a map, or nil into a nil map.
func (m *mapDecoder) decode(d *decodeState, v reflect.Value) error {
	h, err := d.readHeader()
	if err != nil {
		//: malformed or truncated.
		return err
	}
	//: a map or nil.
	switch h.fam {
	//: the pairs.
	case famMap:
		return m.fill(d, v, h)
	//: nil is a nil map.
	case famNil:
		return zeroValue(v)
	//: anything else.
	default:
		return d.mismatch(h, v.Type())
	}
}

// fill adds h's pairs to the map v, creating it when nil.
func (m *mapDecoder) fill(d *decodeState, v reflect.Value, h header) error {
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
	//: a nil map is created with a bounded reservation.
	if v.IsNil() {
		v.Set(reflect.MakeMapWithSize(m.typ, int(min(h.arg, mapPreallocPairs))))
	}
	key := reflect.New(m.keyType).Elem()
	val := reflect.New(m.elemType).Elem()
	//: one pair per declared count.
	for range h.arg {
		//: the first failure wins.
		if err := m.pair(d, v, key, val); err != nil {
			//: stop.
			return err
		}
	}
	d.leave()
	//: filled.
	return nil
}

// pair decodes one key and value into the scratch values and stores them.
func (m *mapDecoder) pair(d *decodeState, v, key, val reflect.Value) error {
	at := d.off
	key.SetZero()
	//: the key, by the key type's decoder.
	if err := m.key(d, key); err != nil {
		//: stop.
		return err
	}
	//: a key holding a slice, a map or a function cannot be stored.
	if m.checkHashable && !key.Comparable() {
		//: refuse instead of letting the runtime panic.
		return unmarshalFault("map key is not hashable", errs.Int(fieldOffset, at), typeField(m.keyType))
	}
	val.SetZero()
	//: the value, by the element type's decoder.
	if err := m.elem(d, val); err != nil {
		//: stop.
		return err
	}
	v.SetMapIndex(key, val)
	//: stored; a repeated key keeps its last value.
	return nil
}

// directMapDecoder returns the decoder of a map[string]V that stores each
// pair with a plain Go assignment.
func directMapDecoder[V any](elem func(*decodeState) (V, error)) decodeFunc {
	//: the closure is the plan.
	return func(d *decodeState, v reflect.Value) error {
		h, err := d.readHeader()
		if err != nil {
			//: malformed or truncated.
			return err
		}
		//: a map or nil.
		switch h.fam {
		//: the pairs.
		case famMap:
			return fillDirectMap(d, v, h, elem)
		//: nil is a nil map.
		case famNil:
			return zeroValue(v)
		//: anything else.
		default:
			return d.mismatch(h, v.Type())
		}
	}
}

// fillDirectMap adds h's pairs to the map[string]V v, creating it when nil.
func fillDirectMap[V any](d *decodeState, v reflect.Value, h header, elem func(*decodeState) (V, error)) error {
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
	target, ok := interfaceOf[*map[string]V](v.Addr())
	//: a map reached through an unexported embedding.
	if !ok {
		//: refuse rather than panic.
		return unmarshalFault("cannot set a value reached through an unexported field", typeField(v.Type()))
	}
	//: a nil map is created with a bounded reservation.
	if *target == nil {
		*target = make(map[string]V, min(h.arg, mapPreallocPairs))
	}
	m := *target
	//: one pair per declared count.
	for range h.arg {
		key, kerr := d.readKeyString()
		if kerr != nil {
			//: not a string key, or truncated.
			return kerr
		}
		x, verr := elem(d)
		if verr != nil {
			//: the first failure wins.
			return verr
		}
		//: a repeated key keeps its last value.
		m[key] = x
	}
	d.leave()
	//: filled.
	return nil
}

// decodeAnyElem decodes a map[string]any element.
func decodeAnyElem(d *decodeState) (any, error) {
	//: the untyped value.
	return d.decodeAny()
}

// decodeStringElem decodes a map[string]string element from str, bin or nil.
func decodeStringElem(d *decodeState) (string, error) {
	p, err := d.readBytesOrNil(stringType)
	//: the string owns a copy of the bytes.
	return string(p), err
}

// decodeIntElem decodes a signed element, refusing a value it cannot hold.
func decodeIntElem[T int | int64](d *decodeState) (T, error) {
	h, err := d.readHeader()
	if err != nil {
		//: malformed or truncated.
		return 0, err
	}
	n, ok := signedArg(h)
	//: not an integer, or wider than T (int is 32 bits on 32-bit platforms).
	if !ok || int64(T(n)) != n {
		//: refuse instead of wrapping.
		return 0, d.rangeOrMismatch(h, reflect.TypeFor[T](), isInteger(h.fam))
	}
	//: stored.
	return T(n), nil
}

// decodeFloat64Elem decodes a float64 element; integers are accepted.
func decodeFloat64Elem(d *decodeState) (float64, error) {
	h, err := d.readHeader()
	if err != nil {
		//: malformed or truncated.
		return 0, err
	}
	f, ok := floatArg(h)
	//: not a number.
	if !ok {
		//: wrong family.
		return 0, d.mismatch(h, float64Type)
	}
	//: stored.
	return f, nil
}

// decodeBoolElem decodes a bool element; nil is false.
func decodeBoolElem(d *decodeState) (bool, error) {
	h, err := d.readHeader()
	if err != nil {
		//: malformed or truncated.
		return false, err
	}
	b, ok := boolArg(h)
	//: not a boolean.
	if !ok {
		//: wrong family.
		return false, d.mismatch(h, boolType)
	}
	//: stored.
	return b, nil
}

// mayBeUnhashable reports whether a value of key type t can hold something
// that is not hashable: an interface, or an array or struct containing one.
func mayBeUnhashable(t reflect.Type) bool {
	//: only these kinds can hide a dynamic type.
	switch t.Kind() {
	//: the dynamic type decides.
	case reflect.Interface:
		return true
	//: an array of possibly unhashable elements.
	case reflect.Array:
		return mayBeUnhashable(t.Elem())
	//: a struct with a possibly unhashable field.
	case reflect.Struct:
		return structMayBeUnhashable(t)
	//: every other key kind is hashable by construction.
	default:
		return false
	}
}

// structMayBeUnhashable reports whether any field of t may be unhashable.
func structMayBeUnhashable(t reflect.Type) bool {
	//: one field is enough.
	for f := range t.Fields() {
		//: recurse into the field's type.
		if mayBeUnhashable(f.Type) {
			//: possibly unhashable.
			return true
		}
	}
	//: hashable.
	return false
}
