package cbor

import (
	"bytes"
	"reflect"
)

// decode decodes a byte string — or an array of small integers, or
// a bignum's magnitude — into a []byte. The result never aliases the input.
func (byteSliceDecoder) decode(d *decodeState, v reflect.Value, p *decodePlan) error {
	h, done, err := d.begin(v, p)
	//: null, a BinaryUnmarshaler, or a broken walk.
	if done || err != nil {
		//: handled.
		return err
	}
	//: three shapes carry bytes.
	switch h.major {
	case majorBytes:
		//: copied.
		return d.storeBytes(v, h)
	case majorArray:
		//: element by element.
		return d.fillSlice(v, p, h)
	case majorTag:
		//: the magnitude of a bignum, as fxamacker/cbor stored it.
		return d.storeBignumBytes(v, h, d.storeBytes)
	default:
		//: recorded and skipped.
		return d.mismatch(h, p.typ)
	}
}

// storeBytes consumes the byte string h opens into a new slice set on v.
func (d *decodeState) storeBytes(v reflect.Value, h itemHead) error {
	content, fresh, err := d.stringBytes(h)
	//: never on validated input.
	if err != nil {
		//: corrupt.
		return err
	}
	//: a definite string aliases the input, which stays the caller's.
	if !fresh {
		content = bytes.Clone(content)
	}
	v.SetBytes(content)
	//: stored.
	return nil
}

// storeBignumBytes consumes a bignum tag and hands its byte string to store.
func (d *decodeState) storeBignumBytes(v reflect.Value, h itemHead, store func(reflect.Value, itemHead) error) error {
	d.off += h.size
	content, err := d.peek()
	//: never on validated input.
	if err != nil {
		//: corrupt.
		return err
	}
	//: validation checked the content is a byte string.
	return store(v, content)
}

// decode decodes a byte string — or an array of small integers, or
// a bignum's magnitude — into a [N]byte, zeroing what the input leaves out.
func (byteArrayDecoder) decode(d *decodeState, v reflect.Value, p *decodePlan) error {
	h, done, err := d.begin(v, p)
	//: null, a BinaryUnmarshaler, or a broken walk.
	if done || err != nil {
		//: handled.
		return err
	}
	//: three shapes carry bytes.
	switch h.major {
	case majorBytes:
		//: copied in.
		return d.copyBytes(v, h)
	case majorArray:
		//: element by element.
		return d.fillArray(v, p, h)
	case majorTag:
		//: the magnitude of a bignum.
		return d.storeBignumBytes(v, h, d.copyBytes)
	default:
		//: recorded and skipped.
		return d.mismatch(h, p.typ)
	}
}

// copyBytes consumes the byte string h opens into the array v: extra bytes
// are dropped and missing ones zeroed.
func (d *decodeState) copyBytes(v reflect.Value, h itemHead) error {
	content, _, err := d.stringBytes(h)
	//: never on validated input.
	if err != nil {
		//: corrupt.
		return err
	}
	//: byte by byte, which needs no address.
	for i := range v.Len() {
		var b byte
		//: past the input, the array is zeroed.
		if i < len(content) {
			b = content[i]
		}
		v.Index(i).SetUint(uint64(b))
	}
	//: stored.
	return nil
}

// decode decodes an array into a slice.
func (sliceDecoder) decode(d *decodeState, v reflect.Value, p *decodePlan) error {
	h, done, err := d.begin(v, p)
	//: null, a BinaryUnmarshaler, or a broken walk.
	if done || err != nil {
		//: handled.
		return err
	}
	//: only an array fills a slice.
	if h.major != majorArray {
		//: recorded and skipped.
		return d.mismatch(h, p.typ)
	}
	//: element by element.
	return d.fillSlice(v, p, h)
}

// fillSlice consumes the array h opens into the slice v, sized to it.
func (d *decodeState) fillSlice(v reflect.Value, p *decodePlan, h itemHead) error {
	d.off += h.size
	count, err := d.entryCount(h)
	//: never on validated input.
	if err != nil {
		//: corrupt.
		return err
	}
	prepareSlice(v, p.typ, count)
	//: each element in place.
	for i := range count {
		//: never fails on validated input.
		if err := p.elem.kind.decode(d, v.Index(i), p.elem); err != nil {
			//: corrupt.
			return err
		}
	}
	//: an indefinite array's break is still to be read.
	return d.closeIndefinite(h)
}

// prepareSlice sizes the slice v to count elements, reusing its backing
// array when it is large enough and zeroing the elements it reuses. An empty
// array gives an empty, non-nil slice.
func prepareSlice(v reflect.Value, t reflect.Type, count int) {
	//: a new backing array when there is none big enough.
	if v.IsNil() || v.Cap() < count || count == 0 {
		v.Set(reflect.MakeSlice(t, count, count))
		//: fresh, already zero.
		return
	}
	v.SetLen(count)
	//: nothing of the previous content survives.
	for i := range count {
		v.Index(i).SetZero()
	}
}

// decode decodes an array into a Go array.
func (arrayDecoder) decode(d *decodeState, v reflect.Value, p *decodePlan) error {
	h, done, err := d.begin(v, p)
	//: null, a BinaryUnmarshaler, or a broken walk.
	if done || err != nil {
		//: handled.
		return err
	}
	//: only an array fills an array.
	if h.major != majorArray {
		//: recorded and skipped.
		return d.mismatch(h, p.typ)
	}
	//: element by element.
	return d.fillArray(v, p, h)
}

// fillArray consumes the array h opens into the Go array v: elements past
// v's length are skipped, and v's elements past the input's are zeroed.
func (d *decodeState) fillArray(v reflect.Value, p *decodePlan, h itemHead) error {
	d.off += h.size
	seq := newSequence(h)
	filled := 0
	//: each element of the input.
	for {
		more, err := d.next(&seq)
		//: the input is exhausted, or the walk is broken.
		if err != nil || !more {
			zeroFrom(v, filled)
			//: done.
			return err
		}
		//: stored while the array has room, skipped after.
		if err := d.arrayElement(v, p, filled); err != nil {
			//: corrupt.
			return err
		}
		filled++
	}
}

// arrayElement stores the next element into v[i], or skips it past v's end.
func (d *decodeState) arrayElement(v reflect.Value, p *decodePlan, i int) error {
	//: no room left.
	if i >= v.Len() {
		//: dropped.
		return d.skip()
	}
	//: in place.
	return p.elem.kind.decode(d, v.Index(i), p.elem)
}

// zeroFrom zeroes the elements of the array v from index from on.
func zeroFrom(v reflect.Value, from int) {
	//: the elements the input did not reach.
	for i := from; i < v.Len(); i++ {
		v.Index(i).SetZero()
	}
}

// decode decodes a map into a Go map, which it allocates when nil and
// otherwise adds to. A key or value that cannot be stored leaves its pair
// out; a later pair with an equal key replaces an earlier one.
func (mapDecoder) decode(d *decodeState, v reflect.Value, p *decodePlan) error {
	h, done, err := d.begin(v, p)
	//: null, a BinaryUnmarshaler, or a broken walk.
	if done || err != nil {
		//: handled.
		return err
	}
	//: only a map fills a map.
	if h.major != majorMap {
		//: recorded and skipped.
		return d.mismatch(h, p.typ)
	}
	d.off += h.size
	//: allocated on demand, sized to the input.
	if v.IsNil() {
		v.Set(reflect.MakeMapWithSize(p.typ, mapHint(h)))
	}
	seq := newSequence(h)
	//: the two map types untyped documents and attributes are made of.
	if handled, knownErr := d.knownMapEntries(v, &seq); handled {
		//: decoded without reflection.
		return knownErr
	}
	//: through the key and value plans.
	return d.mapEntries(v, p, &seq)
}

// knownMapEntries decodes the pairs of seq without reflection when v is
// exactly a map[string]any or a map[string]string.
func (d *decodeState) knownMapEntries(v reflect.Value, seq *sequence) (handled bool, err error) {
	//: the untyped document.
	if m, ok := reflect.TypeAssert[map[string]any](v); ok {
		//: values decoded untyped.
		return true, stringKeyedEntries(d, m, seq, d.anyEntry)
	}
	//: attributes, headers, labels.
	if m, ok := reflect.TypeAssert[map[string]string](v); ok {
		//: values decoded as text.
		return true, stringKeyedEntries(d, m, seq, d.textEntry)
	}
	//: any other map goes through its plans.
	return false, nil
}

// stringKeyedEntries decodes the pairs of seq into a map keyed by strings,
// each value read by value.
func stringKeyedEntries[V any](d *decodeState, m map[string]V, seq *sequence, value func() (V, bool, error)) error {
	//: pair after pair.
	for {
		more, err := d.next(seq)
		//: the map is complete, or the walk is broken.
		if err != nil || !more {
			//: done.
			return err
		}
		//: never fails on validated input.
		if err := stringKeyedEntry(d, m, value); err != nil {
			//: corrupt.
			return err
		}
	}
}

// stringKeyedEntry decodes one pair into m, leaving it out when the key or
// the value cannot be stored.
func stringKeyedEntry[V any](d *decodeState, m map[string]V, value func() (V, bool, error)) error {
	key, stored, err := d.readText(stringType)
	//: never on validated input.
	if err != nil {
		//: corrupt.
		return err
	}
	//: a key that is not text takes its value with it.
	if !stored {
		//: skipped.
		return d.skip()
	}
	elem, stored, err := value()
	//: a value that could not be stored leaves the pair out.
	if err == nil && stored {
		m[key] = elem
	}
	//: inserted, or not.
	return err
}

// anyEntry decodes a map value untyped; stored is false when anything inside
// it could not be decoded.
func (d *decodeState) anyEntry() (value any, stored bool, err error) {
	before := d.failures
	value, err = d.decodeAny()
	//: a failure inside the value leaves the pair out.
	return value, d.failures == before, err
}

// textEntry decodes a map value as a string.
func (d *decodeState) textEntry() (value string, stored bool, err error) {
	//: the same rules as a string field.
	return d.readText(stringType)
}

// readText consumes an item stored into a string of type t: a text string's
// content, past any transparent tag. Null stores nothing, which for a fresh
// key or value is the empty string; anything else is recorded and skipped.
func (d *decodeState) readText(t reflect.Type) (text string, stored bool, err error) {
	h, err := d.skipTags()
	//: never on validated input.
	if err != nil {
		//: corrupt.
		return "", false, err
	}
	//: null leaves the empty string.
	if isNull(h) {
		d.off += h.size
		//: "".
		return "", true, nil
	}
	//: only text.
	if h.major != majorText {
		//: recorded and skipped.
		return "", false, d.mismatch(h, t)
	}
	content, _, err := d.stringBytes(h)
	//: a copy.
	return string(content), err == nil, err
}

// mapEntries decodes the pairs of seq through the key and value plans. The
// key and value are decoded into two values reused across pairs and zeroed
// before each one; SetMapIndex copies them into the map.
func (d *decodeState) mapEntries(v reflect.Value, p *decodePlan, seq *sequence) error {
	holders := p.holders.Get()
	defer releaseHolders(p.holders, holders)
	//: pair after pair.
	for {
		more, err := d.next(seq)
		//: the map is complete, or the walk is broken.
		if err != nil || !more {
			//: done.
			return err
		}
		//: never fails on validated input.
		if err := d.mapEntry(v, p, holders.key, holders.value); err != nil {
			//: corrupt.
			return err
		}
	}
}

// mapEntry decodes one pair into the map v.
func (d *decodeState) mapEntry(v reflect.Value, p *decodePlan, key, value reflect.Value) error {
	key.SetZero()
	before := d.failures
	//: never fails on validated input.
	if err := p.key.kind.decode(d, key, p.key); err != nil {
		//: corrupt.
		return err
	}
	//: a key that could not be decoded, or not hashed, takes its value with it.
	if d.failures != before || !d.keyUsable(p, key) {
		//: skipped.
		return d.skip()
	}
	value.SetZero()
	before = d.failures
	//: never fails on validated input.
	if err := p.elem.kind.decode(d, value, p.elem); err != nil {
		//: corrupt.
		return err
	}
	//: a value that could not be stored leaves the pair out.
	if d.failures == before {
		v.SetMapIndex(key, value)
	}
	//: done.
	return nil
}

// keyUsable reports whether key can be inserted into a Go map. Only a key
// type with an interface inside is checked; a key that would panic the map
// is recorded as a failure.
func (d *decodeState) keyUsable(p *decodePlan, key reflect.Value) bool {
	//: a key type without an interface is always comparable.
	if !p.has(planKeyCheck) || hashable(key) {
		//: usable.
		return true
	}
	d.note(func() string {
		//: the type, never the value.
		return "a map key that cannot be a Go map key of type " + p.key.typ.String()
	})
	//: refused.
	return false
}
