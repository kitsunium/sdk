package msgpack

import (
	"bytes"
	"cmp"
	"encoding/binary"
	"maps"
	"reflect"
	"slices"

	"github.com/kitsunium/sdk/internal/core/data/codec/scratch"
)

// The bookkeeping of a map's order (#1104).
const (
	// smallMapPairs is how many pairs a map can have before the bookkeeping
	// of its order leaves the stack, and before an insertion sort stops
	// beating the general one.
	smallMapPairs int = 16
	// prefixBytes is how many leading payload bytes of a string key are
	// compared as one integer before the whole keys are.
	prefixBytes int = 8
)

// pairSpan locates one encoded pair, relative to the start of the first.
type pairSpan struct {
	// prefix is a string key's first prefixBytes payload bytes, big-endian
	// and zero-padded: two string keys whose prefixes differ are ordered by
	// one integer comparison.
	prefix uint64
	// key is where the pair's key starts.
	key int
	// value is where the pair's value starts, and its key ends.
	value int
	// end is where the pair ends.
	end int
	// str is set when the key is a string, and prefix holds its start.
	str bool
}

// wireInt is an encoded integer key, read back for ordering: neg holds a
// negative value as its int64 bits, otherwise u is the value.
type wireInt struct {
	// u is the value, or a negative value's two's-complement bits.
	u uint64
	// neg is set for a value below zero.
	neg bool
}

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
	var small [smallMapPairs]string
	buf := small[:0]
	//: a large map's keys are reserved once.
	if len(m) > smallMapPairs {
		buf = make([]string, 0, len(m))
	}
	keys := sortedStringKeys(m, buf)
	//: key, then value, per pair, in key order.
	for _, k := range keys {
		x := m[k]
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
	start := len(b)
	var small [smallMapPairs]pairSpan
	spans := small[:0]
	//: a large map's bookkeeping is reserved once.
	if n := v.Len(); n > smallMapPairs {
		spans = make([]pairSpan, 0, n)
	}
	iter := v.MapRange()
	key, val := m.holders(v)
	//: key, then value, per pair; the order is put right afterwards.
	for iter.Next() {
		k, x := m.pair(iter, key, val)
		span := pairSpan{key: len(b) - start}
		b, err = m.key(b, k, depth+1)
		//: a key the codec cannot encode.
		if err != nil {
			//: the first failure wins.
			return b, err
		}
		span.value = len(b) - start
		markKey(b[start:], &span)
		b, err = m.elem(b, x, depth+1)
		//: a value the codec cannot encode.
		if err != nil {
			//: the first failure wins.
			return b, err
		}
		span.end = len(b) - start
		spans = append(spans, span)
	}
	//: every pair written, then put in key order.
	return reorderPairs(b, start, spans), nil
}

// holders returns the two values a map handed out normally is read into for
// its whole walk, so a pair costs no allocation; a map reached through an
// unexported field gets none and is read through the iterator's copies.
func (m *mapEncoder) holders(v reflect.Value) (keyHolder, elemHolder reflect.Value) {
	//: reflection refuses to set from a read-only map's iterator.
	if !v.CanInterface() {
		//: copies, read-only.
		return reflect.Value{}, reflect.Value{}
	}
	//: reused for every pair.
	return reflect.New(m.keyType).Elem(), reflect.New(m.elemType).Elem()
}

// pair returns the iterator's current key and value: read into the holders
// when there are any, else the iterator's own copies.
func (*mapEncoder) pair(iter *reflect.MapIter, keyHolder, elemHolder reflect.Value) (key, elem reflect.Value) {
	//: no holders: a read-only map.
	if !keyHolder.IsValid() {
		//: copies.
		return iter.Key(), iter.Value()
	}
	keyHolder.SetIterKey(iter)
	elemHolder.SetIterValue(iter)
	//: the reused values.
	return keyHolder, elemHolder
}

// sortedStringKeys returns m's keys in byte-wise order — encoding/json's —
// collected into buf, which a small map does not outgrow.
func sortedStringKeys[V any](m map[string]V, buf []string) []string {
	keys := slices.AppendSeq(buf, maps.Keys(m))
	//: an insertion sort for a small map, the general one beyond.
	if len(keys) > smallMapPairs {
		slices.Sort(keys)
		//: sorted.
		return keys
	}
	//: insert each key among the sorted ones before it.
	for i := 1; i < len(keys); i++ {
		//: shift the larger ones right.
		for j := i; j > 0 && keys[j-1] > keys[j]; j-- {
			keys[j-1], keys[j] = keys[j], keys[j-1]
		}
	}
	//: sorted.
	return keys
}

// reorderPairs sorts the pairs written at b[start:] by their encoded keys and
// rewrites them in that order, in place: the region is copied once into a
// pooled scratch buffer and copied back pair by pair, so b never grows.
func reorderPairs(b []byte, start int, spans []pairSpan) []byte {
	//: zero or one pair is already in order.
	if len(spans) < 2 {
		//: nothing to move.
		return b
	}
	region := b[start:]
	sortPairs(region, spans)
	tmp := scratch.AcquireBuffer()
	tmp.Write(region)
	unsorted := tmp.Bytes()
	cursor := start
	//: the pairs, in order, over the unsorted ones.
	for i := range spans {
		cursor += copy(b[cursor:], unsorted[spans[i].key:spans[i].end])
	}
	scratch.ReleaseBuffer(tmp)
	//: same length, pairs in order.
	return b
}

// sortPairs orders spans by encoded key: an insertion sort for a small map,
// which is most of them, and the general sort beyond.
func sortPairs(region []byte, spans []pairSpan) {
	//: a large map.
	if len(spans) > smallMapPairs {
		slices.SortFunc(spans, func(x, y pairSpan) int {
			//: by encoded key.
			return comparePairs(region, &x, &y)
		})
		//: sorted.
		return
	}
	//: insert each pair among the sorted ones before it.
	for i := 1; i < len(spans); i++ {
		//: shift the larger ones right.
		for j := i; j > 0 && comparePairs(region, &spans[j-1], &spans[j]) > 0; j-- {
			spans[j-1], spans[j] = spans[j], spans[j-1]
		}
	}
}

// comparePairs orders two pairs by their keys, and two pairs whose keys
// encode alike — an int8(1) and an int64(1) in one map[any]any, two NaN
// float keys — by their values, so even those maps encode one way.
func comparePairs(region []byte, x, y *pairSpan) int {
	//: two string keys whose first bytes differ: one comparison.
	if x.str && y.str && x.prefix != y.prefix {
		//: decided by the prefixes.
		return cmp.Compare(x.prefix, y.prefix)
	}
	//: the keys decide almost always.
	if c := compareEncodedKeys(region[x.key:x.value], region[y.key:y.value]); c != 0 {
		//: decided.
		return c
	}
	//: equal keys: the values decide.
	return bytes.Compare(region[x.value:x.end], region[y.value:y.end])
}

// compareEncodedKeys orders two encoded keys: two strings byte-wise over
// their payloads, as encoding/json orders keys; two integers by value,
// whatever width each was written in; anything else by its encoding.
func compareEncodedKeys(a, b []byte) int {
	//: two strings.
	if sa, ok := strPayload(a); ok {
		//: both strings: their bytes.
		if sb, isStr := strPayload(b); isStr {
			//: byte-wise.
			return bytes.Compare(sa, sb)
		}
	}
	//: two integers.
	if ia, ok := intKey(a); ok {
		//: both integers: their values.
		if ib, isInt := intKey(b); isInt {
			//: numerically.
			return ia.compare(ib)
		}
	}
	//: mixed or other kinds: the encodings, which is still one order.
	return bytes.Compare(a, b)
}

// strPayload returns the bytes of an encoded str, past its head.
func strPayload(key []byte) ([]byte, bool) {
	//: no head.
	if len(key) == 0 {
		//: not a str.
		return nil, false
	}
	//: one head per str form.
	switch c := key[0]; {
	//: fixstr: the length is in the head byte.
	case c&^fixstrLenMask == fixstrPrefix:
		return key[1:], true
	//: str 8.
	case c == codeStr8 && len(key) >= 1+int(width8):
		return key[1+int(width8):], true
	//: str 16.
	case c == codeStr16 && len(key) >= 1+int(width16):
		return key[1+int(width16):], true
	//: str 32.
	case c == codeStr32 && len(key) >= 1+int(width32):
		return key[1+int(width32):], true
	//: anything else.
	default:
		return nil, false
	}
}

// intKey reads back an encoded integer key.
func intKey(key []byte) (wireInt, bool) {
	//: no head.
	if len(key) == 0 {
		//: not an integer.
		return wireInt{}, false
	}
	c, body := key[0], key[1:]
	//: the two fixints carry the value in the head.
	switch {
	//: positive fixint.
	case c <= posFixintMax:
		return wireInt{u: uint64(c)}, true
	//: negative fixint.
	case c >= negFixintMin:
		return signedKey(int64(int8(c))), true //nolint:gosec // the byte is the value
	//: the sized forms.
	default:
		return sizedIntKey(c, body)
	}
}

// sizedIntKey reads a uint 8–64 or int 8–64 key through the header table:
// the head's family and width, the body read big-endian at that width, and a
// signed value sign-extended from it — the decoder's own path (beUint, widen).
func sizedIntKey(c byte, body []byte) (wireInt, bool) {
	info := &headerTable[c]
	//: not a sized integer, or a body of another width.
	if (info.fam != famUint && info.fam != famInt) || len(body) != int(info.width) {
		return wireInt{}, false
	}
	raw := widen(beUint(body), info)
	//: unsigned: the value as read.
	if info.fam == famUint {
		return wireInt{u: raw}, true
	}
	//: signed: its two's complement, sign-extended.
	return signedKey(int64(raw)), true //nolint:gosec // two's complement
}

// signedKey places a signed value: a negative one apart, a non-negative one
// with the unsigned values it equals.
func signedKey(n int64) wireInt {
	//: below zero.
	if n < 0 {
		//: kept as its bits.
		return wireInt{u: uint64(n), neg: true} //nolint:gosec // two's complement bits, read back as int64
	}
	//: zero or above compares as unsigned.
	return wireInt{u: uint64(n)}
}

// compare orders two integer keys by value.
func (a wireInt) compare(b wireInt) int {
	//: a negative value is below every other.
	if a.neg != b.neg {
		//: the negative one first.
		if a.neg {
			//: a below b.
			return -1
		}
		//: b below a.
		return 1
	}
	//: two negatives compare as signed.
	if a.neg {
		//: signed.
		return cmp.Compare(int64(a.u), int64(b.u)) //nolint:gosec // bits written from an int64
	}
	//: two non-negatives compare as unsigned.
	return cmp.Compare(a.u, b.u)
}

// markKey records, for the key written at region[span.key:span.value],
// whether it is a string and the integer its first bytes make.
func markKey(region []byte, span *pairSpan) {
	payload, ok := strPayload(region[span.key:span.value])
	//: only string keys are compared by prefix.
	if !ok {
		//: compared in full.
		return
	}
	var padded [prefixBytes]byte
	copy(padded[:], payload)
	span.str = true
	span.prefix = binary.BigEndian.Uint64(padded[:])
}
