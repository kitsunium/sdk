package cbor

import (
	"bytes"
	"encoding/binary"
	"io"
	"reflect"

	"github.com/kitsunium/sdk/internal/core/data/codec"
	corecbor "github.com/kitsunium/sdk/internal/core/data/codec/cbor"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// strictLinearKeys is how many keys of one map the duplicate check compares
// pairwise before it switches to a set.
const strictLinearKeys int = 32

// Key classes of the duplicate check: two keys that decode to the same Go
// key compare equal whatever head or chunking each was written with.
const (
	// keyClassText is a text string, by its content.
	keyClassText byte = 't'
	// keyClassBytes is a byte string, by its content.
	keyClassBytes byte = 'b'
	// keyClassUnsigned is an unsigned integer, by its value.
	keyClassUnsigned byte = 'u'
	// keyClassNegative is a negative integer, by its argument.
	keyClassNegative byte = 'n'
	// keyClassRaw is any other key, by its encoding.
	keyClassRaw byte = 'r'
)

// strictCodec is the instance NewStrict returns: the registered codec's
// encoding, and a decode that reads one document one way or not at all.
type strictCodec struct {
	cborCodec
}

// NewStrict returns a fresh CBOR codec, not registered, whose Unmarshal and
// Decode read a document somebody else wrote one way or not at all (ADR
// 0102): a map that repeats a key — two keys that decode to the same Go key,
// whatever form each was written in — is refused with DUPLICATE_KEY before
// anything is decoded, and a member no field of the target struct claims
// with UNKNOWN_MEMBER. The registered codec keeps last-wins and skips unknown
// members. Encoding is the registered codec's.
func NewStrict() codec.Codec {
	//: never added to the registry.
	return &strictCodec{}
}

// Unmarshal validates data like the registered codec, then refuses a
// repeated key, then decodes refusing an unknown member.
func (c *strictCodec) Unmarshal(data []byte, v any) error {
	//: a nil or non-pointer target is the caller's error, refused first (#1101).
	if terr := codec.CheckTarget(v); terr != nil {
		return terr
	}
	//: no bytes is no document in this format (#1101).
	if len(data) == 0 {
		return codec.EmptyInput
	}
	n, err := validateItem(data, maxCBORNestedLevels)
	//: not well-formed, not valid, past a bound, or followed by bytes.
	if err != nil || n != len(data) {
		//: the registered codec's refusal.
		return c.cborCodec.Unmarshal(data, v)
	}
	//: one reading of the document.
	return unmarshalItemMode(data, v, decodeMode{strict: true})
}

// NewDecoder wraps r in a strict streaming codec.Decoder.
func (*strictCodec) NewDecoder(r io.Reader) codec.Decoder {
	//: the validator resumes across reads; every item decodes strictly.
	return &cborDecoder{r: r, walk: validator{limit: maxCBORNestedLevels}, mode: decodeMode{strict: true}}
}

// checkKeys walks the validated item at the cursor and refuses a map holding
// two keys that decode to the same Go key. It decodes nothing.
func (d *decodeState) checkKeys() error {
	h, err := d.peek()
	//: never on validated input.
	if err != nil {
		//: corrupt.
		return err
	}
	d.off += h.size
	//: containers are walked; everything else is stepped over.
	switch h.major {
	//: a payload, or chunks.
	case majorBytes, majorText:
		return d.skipString(h)
	//: the elements.
	case majorArray:
		return d.checkEntries(h, false)
	//: the pairs.
	case majorMap:
		return d.checkEntries(h, true)
	//: the enclosed item.
	case majorTag:
		return d.checkKeys()
	//: an integer or a simple value is its head.
	default:
		return nil
	}
}

// checkEntries walks an array's elements or a map's pairs, definite or not.
func (d *decodeState) checkEntries(h itemHead, pairs bool) error {
	var small [strictLinearKeys]normKey
	seen := small[:0]
	var many map[string]struct{}
	//: entry after entry, up to the count or the break.
	for i := uint64(0); h.info == infoIndefinite || i < h.arg; i++ {
		//: an indefinite container ends at its break.
		if h.info == infoIndefinite && d.consumeBreak() {
			//: done.
			return nil
		}
		//: a map pair's key first.
		if pairs {
			var err error
			//: a key this map already holds stops the walk.
			if seen, many, err = d.checkPairKey(seen, many); err != nil {
				return err
			}
		}
		//: the element, or the pair's value, walked.
		if err := d.checkKeys(); err != nil {
			//: stop.
			return err
		}
	}
	//: walked.
	return nil
}

// consumeBreak consumes the break that ends an indefinite container, and
// reports whether the next byte was one.
func (d *decodeState) consumeBreak() bool {
	//: not a break: an entry follows.
	if d.off >= len(d.data) || d.data[d.off] != breakByte {
		return false
	}
	d.off++
	//: the container's end.
	return true
}

// checkPairKey consumes a map pair's key and refuses it when the map already
// holds it, naming where — never the key. It returns seen and many as addKey
// leaves them.
func (d *decodeState) checkPairKey(seen []normKey, many map[string]struct{}) (kept []normKey, indexed map[string]struct{}, err error) {
	at := d.off
	key, err := d.normalKey()
	//: never on validated input.
	if err != nil {
		//: corrupt.
		return seen, many, err
	}
	kept, indexed, dup := addKey(seen, many, key)
	//: a key this map already holds.
	if dup {
		//: refused.
		return kept, indexed, duplicateKey(at)
	}
	//: new.
	return kept, indexed, nil
}

// addKey records key among the keys one map has shown — seen while they are
// at most strictLinearKeys, compared pairwise on the caller's stack, many once
// indexed by their setKey — and reports whether the map already held it. It
// takes and returns the two by value, so seen's array stays the caller's
// stack array.
func addKey(seen []normKey, many map[string]struct{}, key normKey) (kept []normKey, indexed map[string]struct{}, dup bool) {
	//: indexed: one lookup, one insert.
	if many != nil {
		return seen, many, indexKey(many, &key)
	}
	//: pairwise.
	for i := range seen {
		//: a repeat.
		if seen[i].equal(&key) {
			return seen, nil, true
		}
	}
	//: room left on the stack: kept there.
	if len(seen) < strictLinearKeys {
		return append(seen, key), nil, false
	}
	//: past the linear bound: index every key so far, then this one.
	many = make(map[string]struct{}, 4*strictLinearKeys)
	for i := range seen {
		indexKey(many, &seen[i])
	}
	return seen, many, indexKey(many, &key)
}

// indexKey inserts key into many and reports whether it was there already.
func indexKey(many map[string]struct{}, key *normKey) bool {
	k := key.setKey()
	_, held := many[k]
	many[k] = struct{}{}
	return held
}

// normKey is a key in the form two keys that decode to the same Go key
// share: a string by its content, an integer by its argument, anything else
// by its encoding. A definite string's content is read in place.
type normKey struct {
	// content is a string's content, or the encoding of a raw key.
	content []byte
	// arg is an integer key's argument.
	arg uint64
	// class is the key's class.
	class byte
}

// equal reports whether two keys decode to the same Go key.
func (k *normKey) equal(o *normKey) bool {
	//: same class, same argument, same bytes.
	return k.class == o.class && k.arg == o.arg && bytes.Equal(k.content, o.content)
}

// setKey is k as a map key, for the indexed set of a large map.
func (k *normKey) setKey() string {
	//: class, argument, bytes.
	return string(append(binary.BigEndian.AppendUint64([]byte{k.class}, k.arg), k.content...))
}

// normalKey consumes a key and returns it in its normal form.
func (d *decodeState) normalKey() (normKey, error) {
	start := d.off
	h, err := d.peek()
	//: never on validated input.
	if err != nil {
		//: corrupt.
		return normKey{}, err
	}
	//: one form per major type.
	switch h.major {
	//: a text string, by its content, chunks joined.
	case majorText:
		return d.stringContent(h, keyClassText)
	//: a byte string, likewise.
	case majorBytes:
		return d.stringContent(h, keyClassBytes)
	//: an integer, by its argument, whatever head width carried it.
	case majorUnsigned:
		d.off += h.size
		//: by value.
		return normKey{class: keyClassUnsigned, arg: h.arg}, nil
	//: −1−n, likewise.
	case majorNegative:
		d.off += h.size
		//: by argument.
		return normKey{class: keyClassNegative, arg: h.arg}, nil
	//: anything else, by its encoding.
	default:
		//: the whole item.
		if serr := d.skip(); serr != nil {
			//: corrupt.
			return normKey{}, serr
		}
		//: in place.
		return normKey{class: keyClassRaw, content: d.data[start:d.off]}, nil
	}
}

// stringContent consumes a string whose head is h and returns its content in
// place when definite, the chunks of an indefinite one joined.
func (d *decodeState) stringContent(h itemHead, class byte) (normKey, error) {
	d.off += h.size
	//: a definite string is its payload.
	if h.info != infoIndefinite {
		p, err := d.take(h.arg)
		//: in place.
		return normKey{class: class, content: p}, err
	}
	var out []byte
	//: chunks, each a definite string of the same type, up to the break.
	for d.off < len(d.data) && d.data[d.off] != breakByte {
		chunk, err := d.peek()
		//: never on validated input.
		if err != nil {
			//: corrupt.
			return normKey{}, err
		}
		d.off += chunk.size
		p, terr := d.take(chunk.arg)
		//: never on validated input.
		if terr != nil {
			//: corrupt.
			return normKey{}, terr
		}
		out = append(out, p...)
	}
	d.off++
	//: the joined content.
	return normKey{class: class, content: out}, nil
}

// duplicateKey is the strict refusal of a repeated map key.
func duplicateKey(at int) error {
	//: the offset of the repeat; the key is a stranger's text.
	return errs.Wrap(nil, errs.WrapParams{
		Code:    corecbor.CodeCBORDuplicateKey,
		Reason:  "DUPLICATE_KEY",
		Public:  "CBOR document repeats a map key",
		Private: "service/data/codec/cbor.NewStrict: a map holds two keys that decode to the same key",
	}, errs.Int(codec.FieldOffset, at))
}

// unknownMember is the strict refusal of a key no struct field claims.
func unknownMember(at int, t reflect.Type) error {
	//: the offset and the struct, never the key.
	return errs.Wrap(nil, errs.WrapParams{
		Code:    corecbor.CodeCBORUnknownMember,
		Reason:  "UNKNOWN_MEMBER",
		Public:  "CBOR document carries an unknown member",
		Private: "service/data/codec/cbor.NewStrict: a key no field of the struct claims",
	}, errs.Int(codec.FieldOffset, at), errs.String("type", t.String()))
}

// NewBoundedDecoder implements codec.BoundedStreamingCodec (#891): the strict
// stream decoder under a byte ceiling per item.
func (*strictCodec) NewBoundedDecoder(r io.Reader, maxBytes int64) (codec.Decoder, error) {
	//: every item decoded strictly.
	return newBoundedDecoder(r, maxBytes, decodeMode{strict: true})
}
