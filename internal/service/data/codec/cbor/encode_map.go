package cbor

import (
	"bytes"
	"cmp"
	"encoding/binary"
	"maps"
	"math/big"
	"reflect"
	"slices"
	"strings"
)

// The bookkeeping of a map's order.
const (
	// smallMapPairs is how many pairs a map can have before the bookkeeping
	// of its order leaves the stack, and before an insertion sort stops
	// beating the general one.
	smallMapPairs int = 16
	// keyPrefixBytes is how many leading bytes of an encoded key are compared
	// as one integer before the whole keys are.
	keyPrefixBytes int = 8
)

// pairSpan locates one encoded pair, relative to the start of the first.
type pairSpan struct {
	// prefix is the key's first keyPrefixBytes bytes, big-endian, zero-padded.
	// Encoded items are prefix-free, so two keys that differ are ordered by
	// their prefixes unless both run past them.
	prefix uint64
	// key is where the pair's key starts.
	key int
	// value is where the pair's value starts, and its key ends.
	value int
	// end is where the pair ends.
	end int
}

// mapEncoder writes a map with its pairs in order.
type mapEncoder struct{}

// bigIntEncoder writes a big.Int as an integer or a bignum.
type bigIntEncoder struct{}

// encode appends a map; a nil map is null.
func (mapEncoder) encode(b []byte, v reflect.Value, p *encodePlan, at walkDepth) ([]byte, error) {
	//: nil is null, as every nil map is.
	if v.IsNil() {
		//: null.
		return append(b, nullByte), nil
	}
	//: the two map types untyped documents and attributes are made of.
	if encoded, handled, err := appendKnownMap(b, v, at); handled {
		//: encoded without reflection.
		return encoded, err
	}
	inner, err := at.enter()
	//: a map is one level of nesting.
	if err != nil {
		//: too deep.
		return b, err
	}
	b = appendHead(b, majorMap, uint64(v.Len()))
	//: an empty map is its head alone.
	if v.Len() == 0 {
		//: done.
		return b, nil
	}
	//: pairs encoded, then put in order.
	return appendSortedPairs(b, v, p, inner)
}

// empty is true when the map has no element.
func (mapEncoder) empty(v reflect.Value, _ *encodePlan) (bool, error) {
	//: nothing in it.
	return v.Len() == 0, nil
}

// appendKnownMap encodes v without reflection when it is exactly a
// map[string]any or a map[string]string.
func appendKnownMap(b []byte, v reflect.Value, at walkDepth) (encoded []byte, handled bool, err error) {
	//: the untyped document.
	if m, ok := reflect.TypeAssert[map[string]any](v); ok {
		encoded, err = appendStringAnyMap(b, m, at)
		//: encoded.
		return encoded, true, err
	}
	//: attributes, headers, labels.
	if m, ok := reflect.TypeAssert[map[string]string](v); ok {
		encoded, err = appendStringStringMap(b, m, at)
		//: encoded.
		return encoded, true, err
	}
	//: any other map goes through its plan.
	return b, false, nil
}

// appendStringAnyMap appends an untyped document's map, its keys in the
// deterministic order.
func appendStringAnyMap(b []byte, m map[string]any, at walkDepth) ([]byte, error) {
	var small [smallMapPairs]string
	b, inner, keys, err := openStringMap(b, m, at, small[:0])
	//: each pair in order.
	for _, k := range keys {
		b, err = appendText(b, k)
		//: a key that is not UTF-8 is refused.
		if err != nil {
			//: refused.
			return b, err
		}
		b, err = appendValue(b, m[k], inner)
		//: the first failure ends the encoding.
		if err != nil {
			//: refused.
			return b, err
		}
	}
	//: the whole map, null, or the depth refusal.
	return b, err
}

// appendStringStringMap appends a map of strings, its keys in the
// deterministic order.
func appendStringStringMap(b []byte, m map[string]string, at walkDepth) ([]byte, error) {
	var small [smallMapPairs]string
	b, _, keys, err := openStringMap(b, m, at, small[:0])
	//: each pair in order.
	for _, k := range keys {
		b, err = appendText(b, k)
		//: a key that is not UTF-8 is refused.
		if err != nil {
			//: refused.
			return b, err
		}
		b, err = appendText(b, m[k])
		//: a value that is not UTF-8 is refused.
		if err != nil {
			//: refused.
			return b, err
		}
	}
	//: the whole map, null, or the depth refusal.
	return b, err
}

// openStringMap writes the head of a map keyed by strings — null for a nil
// one — and returns its keys in the deterministic order: shorter first, then
// bytewise, which is the bytewise order of their encodings, the length being
// in the head. A nil map or a refusal returns no key. The keys are collected
// into buf, which a small map does not outgrow.
func openStringMap[V any](b []byte, m map[string]V, at walkDepth, buf []string) (opened []byte, inner walkDepth, keys []string, err error) {
	//: nil is null.
	if m == nil {
		//: no pair to write.
		return append(b, nullByte), at, nil, nil
	}
	inner, err = at.enter()
	//: a map is one level of nesting.
	if err != nil {
		//: too deep.
		return b, at, nil, err
	}
	keys = slices.AppendSeq(buf, maps.Keys(m))
	slices.SortFunc(keys, compareTextKeys)
	//: the head, then the keys to write after it.
	return appendHead(b, majorMap, uint64(len(m))), inner, keys, nil
}

// compareTextKeys orders two text keys as their encodings order: the shorter
// first, since the length opens the head, then bytewise.
func compareTextKeys(a, b string) int {
	//: the head carries the length, and a longer length is a greater head.
	if len(a) != len(b) {
		//: shorter first.
		return cmp.Compare(len(a), len(b))
	}
	//: bytewise.
	return strings.Compare(a, b)
}

// appendSortedPairs appends every pair of the non-empty map v, then reorders
// them by encoded key.
func appendSortedPairs(b []byte, v reflect.Value, p *encodePlan, at walkDepth) ([]byte, error) {
	holders := p.holders.Get()
	defer releaseHolders(p.holders, holders)
	var small [smallMapPairs]pairSpan
	spans := small[:0]
	start := len(b)
	//: one pair at a time, key and value copied into the reused holders.
	for iter := v.MapRange(); iter.Next(); {
		holders.key.SetIterKey(iter)
		holders.value.SetIterValue(iter)
		var span pairSpan
		var err error
		b, span, err = appendPair(b, holders, p, at, start)
		//: the first failure ends the encoding.
		if err != nil {
			//: refused.
			return b, err
		}
		spans = append(spans, span)
	}
	//: in order, or refused for a repeated key.
	return reorderPairs(b, start, spans)
}

// appendPair appends one key and its value, and says where they lie relative
// to start.
func appendPair(b []byte, holders *mapHolders, p *encodePlan, at walkDepth, start int) (encoded []byte, span pairSpan, err error) {
	span.key = len(b) - start
	b, err = p.key.kind.encode(b, holders.key, p.key, at)
	//: the key first.
	if err != nil {
		//: refused.
		return b, span, err
	}
	span.value = len(b) - start
	span.prefix = keyPrefix(b[start+span.key : start+span.value])
	b, err = p.elem.kind.encode(b, holders.value, p.elem, at)
	span.end = len(b) - start
	//: then the value.
	return b, span, err
}

// keyPrefix reads up to the first keyPrefixBytes bytes of an encoded key as a
// big-endian integer, zero-padded.
func keyPrefix(key []byte) uint64 {
	var padded [keyPrefixBytes]byte
	copy(padded[:], key)
	//: one comparison decides most orders.
	return binary.BigEndian.Uint64(padded[:])
}

// comparePairs orders two pairs by their encoded keys.
func comparePairs(region []byte, x, y *pairSpan) int {
	//: the prefixes differ for almost every pair of keys.
	if x.prefix != y.prefix {
		//: decided in one comparison.
		return cmp.Compare(x.prefix, y.prefix)
	}
	//: both keys run past their prefix: bytewise over the rest.
	return bytes.Compare(region[x.key:x.value], region[y.key:y.value])
}

// reorderPairs sorts the pairs written at b[start:] by their encoded keys,
// refusing two equal keys, and rewrites them in that order.
func reorderPairs(b []byte, start int, spans []pairSpan) ([]byte, error) {
	region := b[start:]
	sortPairs(region, spans)
	//: equal keys are adjacent once sorted.
	for i := 1; i < len(spans); i++ {
		//: an invalid map, which the codec does not write.
		if comparePairs(region, &spans[i-1], &spans[i]) == 0 {
			//: refused.
			return b, encodeFailure("two keys of one map that encode to the same CBOR key")
		}
	}
	size := len(region)
	b = append(b, region...)
	unsorted := b[start+size:]
	cursor := start
	//: the pairs, in order, over the unsorted ones.
	for i := range spans {
		cursor += copy(b[cursor:], unsorted[spans[i].key:spans[i].end])
	}
	//: the copy at the tail is dropped.
	return b[:start+size], nil
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

// encode appends a big.Int as an integer when it fits one — major type 0 or
// 1 — and as a bignum, tag 2 or 3 over its magnitude, otherwise.
func (bigIntEncoder) encode(b []byte, v reflect.Value, _ *encodePlan, at walkDepth) ([]byte, error) {
	n, _ := reflect.TypeAssert[big.Int](v)
	//: through the shared appender.
	return appendBigInt(b, &n, at)
}

// empty is never true: a big.Int is always written.
func (bigIntEncoder) empty(_ reflect.Value, _ *encodePlan) (bool, error) {
	//: always written.
	return false, nil
}

// appendBigInt appends n in the shortest form that holds it.
func appendBigInt(b []byte, n *big.Int, at walkDepth) ([]byte, error) {
	//: n ≥ 0 is major type 0, or tag 2.
	if n.Sign() >= 0 {
		//: fits a head.
		if n.IsUint64() {
			//: an unsigned integer.
			return appendHead(b, majorUnsigned, n.Uint64()), nil
		}
		//: a positive bignum.
		return appendBignum(b, tagPositiveBignum, n.Bytes(), at)
	}
	encoded := new(big.Int).Not(n)
	//: n < 0 carries −1−n, which is ^n.
	if encoded.IsUint64() {
		//: a negative integer.
		return appendHead(b, majorNegative, encoded.Uint64()), nil
	}
	//: a negative bignum.
	return appendBignum(b, tagNegativeBignum, encoded.Bytes(), at)
}

// appendBignum appends tag over the magnitude, as a byte string. The tag is
// one level of nesting, as the decoder counts it.
func appendBignum(b []byte, tag uint64, magnitude []byte, at walkDepth) ([]byte, error) {
	//: a tag nests.
	if _, err := at.enter(); err != nil {
		//: too deep.
		return b, err
	}
	b = appendHead(b, majorTag, tag)
	b = appendHead(b, majorBytes, uint64(len(magnitude)))
	//: big-endian, no leading zero.
	return append(b, magnitude...), nil
}
