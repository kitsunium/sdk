// Package cbor — decoding into an untyped target. Each data item becomes its
// default Go value, built without reflection:
//
//	unsigned integer          uint64
//	negative integer          int64, or big.Int below math.MinInt64
//	byte string               []byte
//	text string               string
//	array                     []any
//	map, every key text       map[string]any
//	map, any other key        map[any]any
//	true, false               bool
//	null, undefined           nil
//	half, single, double      float64
//	tag 0 or 1                time.Time
//	tag 2 or 3 (bignum)       big.Int
//	any other tag             its content, as though untagged
//
// An unassigned simple value has no Go value and is refused, as RFC 8949
// §5.4 allows a decoder to; so is a map key Go cannot hash — a byte string,
// an array, a map or a bignum — since it cannot be a key of a map[any]any.
package cbor

import (
	"bytes"
	"iter"
	"maps"
	"math"
	"math/big"
)

// decodeAny decodes the item at the cursor into its default Go value.
func (d *decodeState) decodeAny() (any, error) {
	h, err := d.peek()
	//: never on validated input.
	if err != nil {
		//: corrupt.
		return nil, err
	}
	//: one builder per major type.
	switch h.major {
	case majorUnsigned, majorNegative:
		d.off += h.size
		//: uint64, int64 or big.Int.
		return integerAny(h), nil
	case majorBytes, majorText:
		//: []byte or string.
		return d.stringAny(h)
	case majorArray:
		//: []any.
		return d.arrayAny(h)
	case majorMap:
		//: map[string]any or map[any]any.
		return d.mapAny(h)
	case majorTag:
		//: a time, a bignum, or the content.
		return d.tagAny(h)
	default:
		d.off += h.size
		//: bool, nil, float64 — or refused.
		return d.simpleAny(h), nil
	}
}

// integerAny is the default Go value of an integer head.
func integerAny(h itemHead) any {
	//: an unsigned integer is a uint64, whatever its size.
	if h.major == majorUnsigned {
		//: uint64.
		return h.arg
	}
	//: −1−n fits an int64 when n does.
	if h.arg <= math.MaxInt64 {
		//: int64.
		return ^int64(h.arg)
	}
	//: below math.MinInt64: a big.Int, as fxamacker/cbor returned it.
	return *new(big.Int).Not(new(big.Int).SetUint64(h.arg))
}

// stringAny decodes a byte string to a []byte the caller owns, or a text
// string to a string.
func (d *decodeState) stringAny(h itemHead) (any, error) {
	content, fresh, err := d.stringBytes(h)
	//: never on validated input.
	if err != nil {
		//: corrupt.
		return nil, err
	}
	//: text was checked UTF-8 by validation.
	if h.major == majorText {
		//: a copy.
		return string(content), nil
	}
	//: joined chunks are already a copy.
	if fresh {
		//: owned.
		return content, nil
	}
	//: the input stays the caller's.
	return bytes.Clone(content), nil
}

// arrayAny decodes an array to a []any.
func (d *decodeState) arrayAny(h itemHead) (any, error) {
	d.off += h.size
	count, err := d.entryCount(h)
	//: never on validated input.
	if err != nil {
		//: corrupt.
		return nil, err
	}
	out := make([]any, count)
	//: each element.
	for i := range out {
		//: never fails on validated input.
		if out[i], err = d.decodeAny(); err != nil {
			//: corrupt.
			return nil, err
		}
	}
	//: an indefinite array's break is still to be read.
	return out, d.closeIndefinite(h)
}

// closeIndefinite consumes the break of the indefinite container h once its
// counted entries are read.
func (d *decodeState) closeIndefinite(h itemHead) error {
	//: a definite container has no break.
	if h.info != infoIndefinite {
		//: done.
		return nil
	}
	//: never on validated input.
	if d.off >= len(d.data) || d.data[d.off] != breakByte {
		//: corrupt.
		return d.corrupt()
	}
	d.off++
	//: closed.
	return nil
}

// mapAny decodes a map to a map[string]any while every key is a text
// string, switching to a map[any]any at the first key that is not.
func (d *decodeState) mapAny(h itemHead) (any, error) {
	d.off += h.size
	seq := newSequence(h)
	m := make(map[string]any, mapHint(h))
	//: pair after pair.
	for {
		more, err := d.next(&seq)
		//: the map is complete, or the walk is broken.
		if err != nil || !more {
			//: the map.
			return m, err
		}
		switched, key, err := d.textPair(m)
		//: never on validated input.
		if err != nil {
			//: corrupt.
			return m, err
		}
		//: the first key that is not text changes the map's type.
		if switched {
			//: continued as a map[any]any.
			return d.mapAnyGeneric(m, key, &seq)
		}
	}
}

// textPair decodes one pair of a map still decoded as a map[string]any.
// switched is true, with the pair's key and its value unread, when the key
// is not a text string.
func (d *decodeState) textPair(m map[string]any) (switched bool, key any, err error) {
	key, stored, err := d.keyAny()
	//: never on validated input.
	if err != nil {
		//: corrupt.
		return false, nil, err
	}
	//: a key that could not be decoded takes its value with it.
	if !stored {
		//: skipped.
		return false, nil, d.skip()
	}
	text, isText := key.(string)
	//: a key that is not text changes the map's type.
	if !isText {
		//: the caller goes on as a map[any]any.
		return true, key, nil
	}
	m[text], err = d.decodeAny()
	//: inserted.
	return false, nil, err
}

// keyAny decodes a map key. stored is false when the key could not be
// decoded — the failure is recorded and the key consumed.
func (d *decodeState) keyAny() (key any, stored bool, err error) {
	before := d.failures
	key, err = d.decodeAny()
	//: a failure inside the key leaves it unusable.
	return key, err == nil && d.failures == before, err
}

// mapAnyGeneric goes on decoding a map as a map[any]any, from the pairs
// decoded so far and the first key that is not text.
func (d *decodeState) mapAnyGeneric(texts map[string]any, key any, seq *sequence) (any, error) {
	m := make(map[any]any, len(texts)+1)
	maps.Insert(m, untypedPairs(texts))
	stored := true
	//: the pending key, then each following pair.
	for {
		//: never fails on validated input.
		if err := d.entryAny(m, key, stored); err != nil {
			//: corrupt.
			return m, err
		}
		more, err := d.next(seq)
		//: the map is complete, or the walk is broken.
		if err != nil || !more {
			//: the map.
			return m, err
		}
		key, stored, err = d.keyAny()
		//: never on validated input.
		if err != nil {
			//: corrupt.
			return m, err
		}
	}
}

// untypedPairs yields the pairs of texts with their keys as any, for a map
// that turned out to need keys of every type.
func untypedPairs(texts map[string]any) iter.Seq2[any, any] {
	//: one pair at a time.
	return func(yield func(any, any) bool) {
		//: every pair decoded so far.
		for text, value := range texts {
			//: the consumer may stop early.
			if !yield(text, value) {
				//: stopped.
				return
			}
		}
	}
}

// entryAny decodes the value of key into m. The value is skipped when the
// key was not stored — its failure is already recorded — or when it cannot
// be a Go map key.
func (d *decodeState) entryAny(m map[any]any, key any, stored bool) error {
	//: a failed key takes its value with it.
	if !stored {
		//: skipped.
		return d.skip()
	}
	//: a slice, a map or a big.Int panics as a map key.
	if !hashableAny(key) {
		d.note(func() string {
			//: the type, never the value.
			return "a map key that cannot be a Go map key (a byte string, an array, a map or a bignum)"
		})
		//: the value goes with it.
		return d.skip()
	}
	value, err := d.decodeAny()
	m[key] = value
	//: inserted.
	return err
}

// hashableAny reports whether a default Go value can be a map key: every
// one can except the slices, the maps and big.Int.
func hashableAny(key any) bool {
	//: the default values with a slice or a map inside.
	switch key.(type) {
	case []byte, []any, map[string]any, map[any]any, big.Int:
		//: not comparable.
		return false
	default:
		//: comparable.
		return true
	}
}

// mapHint sizes a new map for the container h: its pair count, or nothing
// for an indefinite one.
func mapHint(h itemHead) int {
	//: an indefinite map gives no count.
	if h.info == infoIndefinite {
		//: grown as needed.
		return 0
	}
	//: validation bounded the count.
	return int(h.arg)
}

// tagAny decodes a tag: 0 and 1 to a time.Time, 2 and 3 to a big.Int, and
// any other tag to its content.
func (d *decodeState) tagAny(h itemHead) (any, error) {
	d.off += h.size
	//: the four tags RFC 8949 §3.4 defines a value for.
	switch h.arg {
	case tagDateTime, tagEpoch:
		//: validation checked the content's type.
		return d.timeAny()
	case tagPositiveBignum, tagNegativeBignum:
		n, err := d.bignumContent(h.arg)
		//: never on validated input.
		if err != nil {
			//: corrupt.
			return nil, err
		}
		//: a big.Int, by value, as fxamacker/cbor returned it.
		return *n, nil
	default:
		//: an unrecognised tag is transparent.
		return d.decodeAny()
	}
}

// timeAny decodes a tag 0 or 1 content to a time.Time, or records it as not
// decodable and returns nil.
func (d *decodeState) timeAny() (any, error) {
	h, err := d.peek()
	//: never on validated input.
	if err != nil {
		//: corrupt.
		return nil, err
	}
	t, stored, err := d.timeValue(h)
	//: an unparsable date/time was recorded.
	if err != nil || !stored {
		//: nil.
		return nil, err
	}
	//: the instant.
	return t, nil
}

// simpleAny is the default Go value of a major type 7 head, already consumed.
func (d *decodeState) simpleAny(h itemHead) any {
	//: assigned simple values and floats.
	switch {
	case h.info == simpleFalse || h.info == simpleTrue:
		//: bool.
		return h.info == simpleTrue
	case h.info == simpleNull || h.info == simpleUndefined:
		//: nil.
		return nil
	case isFloatHead(h):
		//: float64, exactly.
		return floatOf(h)
	default:
		d.note(func() string {
			//: RFC 8949 §5.4 lets a decoder refuse what it does not know.
			return "an unassigned simple value, which has no Go value"
		})
		//: nil.
		return nil
	}
}

// bignumContent consumes the byte string of a bignum tagged tag and returns
// its value: n for tag 2, −1−n for tag 3.
func (d *decodeState) bignumContent(tag uint64) (*big.Int, error) {
	h, err := d.peek()
	//: never on validated input.
	if err != nil {
		//: corrupt.
		return nil, err
	}
	content, _, err := d.stringBytes(h)
	//: never on validated input.
	if err != nil {
		//: corrupt.
		return nil, err
	}
	n := new(big.Int).SetBytes(content)
	//: −1−n is the bitwise complement.
	if tag == tagNegativeBignum {
		n.Not(n)
	}
	//: the value.
	return n, nil
}
