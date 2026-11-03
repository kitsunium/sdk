package msgpack

import (
	"bytes"
	"encoding/binary"
	"io"
	"reflect"

	"github.com/kitsunium/sdk/internal/core/data/codec"
	coremsgpack "github.com/kitsunium/sdk/internal/core/data/codec/msgpack"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// strictLinearKeys is how many keys of one map the duplicate check compares
// pairwise before it switches to a set.
const strictLinearKeys int = 32

// Key classes of the duplicate check: two keys that decode to the same Go
// key compare equal whatever form they were written in.
const (
	// keyClassText is a str or a bin, which both decode to a string key.
	keyClassText byte = 't'
	// keyClassInt is an integer of any width or sign.
	keyClassInt byte = 'i'
	// keyClassRaw is any other key, compared by its encoding.
	keyClassRaw byte = 'r'
)

// strictCodec is the instance NewStrict returns: the registered codec's
// encoding, and a decode that reads one document one way or not at all.
type strictCodec struct {
	msgpackCodec
}

// NewStrict returns a fresh MessagePack codec, not registered, whose Unmarshal and
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

// Unmarshal parses data like the registered codec, refusing a repeated key
// before anything is decoded and an unknown member while it is.
func (c *strictCodec) Unmarshal(data []byte, v any) error {
	//: a nil or non-pointer target is the caller's error, refused first (#1101).
	if terr := codec.CheckTarget(v); terr != nil {
		return terr
	}
	//: no bytes is no document in this format (#1101).
	if len(data) == 0 {
		return codec.EmptyInput
	}
	//: the registered codec's size bound.
	if len(data) > maxMsgPackBytes {
		//: refused before reading.
		return c.msgpackCodec.Unmarshal(data, v)
	}
	//: one value, nothing after it, one reading.
	return unmarshalIntoMode(data, v, true)
}

// NewDecoder wraps r in a strict streaming codec.Decoder.
func (*strictCodec) NewDecoder(r io.Reader) codec.Decoder {
	d := newDecoder(r)
	d.strict = true
	//: every value framed, then decoded strictly.
	return d
}

// checkKeys walks the value at the cursor and refuses a map holding two keys
// that decode to the same Go key. It decodes nothing.
func (d *decodeState) checkKeys() error {
	h, err := d.readHeader()
	//: malformed or truncated.
	if err != nil {
		//: the header's own failure.
		return err
	}
	//: containers are walked; everything else is stepped over.
	switch h.fam {
	//: the pairs.
	case famMap:
		return d.checkMapKeys(h)
	//: the elements.
	case famArray:
		return d.checkElements(h)
	//: a payload or a scalar.
	default:
		_, err = d.skipBody(h, 0)
		//: stepped over.
		return err
	}
}

// checkElements walks an array's elements.
func (d *decodeState) checkElements(h header) error {
	//: a count the input cannot hold is refused first.
	if err := d.checkCount(h); err != nil {
		//: implausible count.
		return err
	}
	//: one level deeper.
	if err := d.enter(); err != nil {
		//: nested too deep.
		return err
	}
	//: each element.
	for range h.arg {
		//: the first failure wins.
		if err := d.checkKeys(); err != nil {
			//: stop.
			return err
		}
	}
	d.leave()
	//: walked.
	return nil
}

// checkMapKeys walks a map's pairs, refusing a key seen before in it.
func (d *decodeState) checkMapKeys(h header) error {
	//: a count the input cannot hold is refused first.
	if err := d.checkCount(h); err != nil {
		//: implausible count.
		return err
	}
	//: one level deeper.
	if err := d.enter(); err != nil {
		//: nested too deep.
		return err
	}
	var small [strictLinearKeys]normKey
	seen := small[:0]
	var many map[string]struct{}
	//: key, then value, per pair.
	for range h.arg {
		var err error
		//: a malformed key, or one this map already holds.
		if seen, many, err = d.checkPairKey(seen, many); err != nil {
			return err
		}
		//: the value, walked.
		if err := d.checkKeys(); err != nil {
			//: stop.
			return err
		}
	}
	d.leave()
	//: every key once.
	return nil
}

// normKey is an encoded key in the form two keys that decode to the same Go
// key share — a str or a bin by its bytes, an integer by its value, anything
// else by its encoding — read in place, without a copy.
type normKey struct {
	// content is the text's bytes, or the encoding of a raw key.
	content []byte
	// n is an integer key's value.
	n wireInt
	// class is the key's class.
	class byte
}

// equal reports whether two keys decode to the same Go key.
func (k *normKey) equal(o *normKey) bool {
	//: same class, same value, same bytes.
	return k.class == o.class && k.n == o.n && bytes.Equal(k.content, o.content)
}

// setKey is k as a map key, for the indexed set of a large map.
func (k *normKey) setKey() string {
	b := binary.BigEndian.AppendUint64([]byte{k.class}, k.n.u)
	//: a negative value apart.
	if k.n.neg {
		b = append(b, 1)
	}
	//: class, value, bytes.
	return string(append(b, k.content...))
}

// checkPairKey consumes a map pair's key and refuses it when the map already
// holds it, naming where — never the key. It returns seen and many as addKey
// leaves them.
func (d *decodeState) checkPairKey(seen []normKey, many map[string]struct{}) (kept []normKey, indexed map[string]struct{}, err error) {
	at := d.off
	//: the key's extent.
	if err := d.skip(); err != nil {
		//: malformed.
		return seen, many, err
	}
	kept, indexed, dup := addKey(seen, many, normalKey(d.data[at:d.off]))
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

// normalKey reads an encoded key in place.
func normalKey(key []byte) normKey {
	//: text: a str, or a bin, which a string-keyed map also reads.
	if p, ok := strPayload(key); ok {
		//: by its bytes.
		return normKey{class: keyClassText, content: p}
	}
	//: a bin key reads as a string too.
	if p, ok := binPayload(key); ok {
		//: the same class as a str.
		return normKey{class: keyClassText, content: p}
	}
	//: an integer, by value.
	if n, ok := intKey(key); ok {
		//: by its value.
		return normKey{class: keyClassInt, n: n}
	}
	//: anything else, by its encoding.
	return normKey{class: keyClassRaw, content: key}
}

// binPayload returns the bytes of an encoded bin, past its head.
func binPayload(key []byte) ([]byte, bool) {
	//: no head.
	if len(key) == 0 {
		//: not a bin.
		return nil, false
	}
	//: one head per bin form.
	switch key[0] {
	//: bin 8.
	case codeBin8:
		return key[min(len(key), 1+int(width8)):], true
	//: bin 16.
	case codeBin16:
		return key[min(len(key), 1+int(width16)):], true
	//: bin 32.
	case codeBin32:
		return key[min(len(key), 1+int(width32)):], true
	//: anything else.
	default:
		return nil, false
	}
}

// duplicateKey is the strict refusal of a repeated map key.
func duplicateKey(at int) error {
	//: the offset of the repeat; the key is a stranger's text.
	return errs.Wrap(nil, errs.WrapParams{
		Code:    coremsgpack.CodeMsgPackDuplicateKey,
		Reason:  "DUPLICATE_KEY",
		Public:  "MessagePack document repeats a map key",
		Private: "service/data/codec/msgpack.NewStrict: a map holds two keys that decode to the same key",
	}, errs.Int(fieldOffset, at))
}

// unknownMember is the strict refusal of a key no struct field claims.
func unknownMember(at int, t reflect.Type) error {
	//: the offset and the struct, never the key.
	return errs.Wrap(nil, errs.WrapParams{
		Code:    coremsgpack.CodeMsgPackUnknownMember,
		Reason:  "UNKNOWN_MEMBER",
		Public:  "MessagePack document carries an unknown member",
		Private: "service/data/codec/msgpack.NewStrict: a key no field of the struct claims",
	}, errs.Int(fieldOffset, at), typeField(t))
}
