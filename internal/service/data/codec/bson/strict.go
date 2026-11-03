package bson

import (
	"bytes"
	"io"
	"reflect"

	"github.com/kitsunium/sdk/internal/core/data/codec"
	corebson "github.com/kitsunium/sdk/internal/core/data/codec/bson"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// strictLinearNames is how many element names of one document the duplicate
// check compares pairwise before it switches to a set.
const strictLinearNames int = 32

// strictCodec is the instance NewStrict returns: the registered codec's
// encoding, and a decode that reads one document one way or not at all.
type strictCodec struct {
	bsonCodec
}

// NewStrict returns a fresh BSON codec, not registered, whose Unmarshal and
// Decode read a document somebody else wrote one way or not at all (ADR
// 0102): a document — at any depth — holding two elements of one name is
// refused with DUPLICATE_KEY before anything is decoded, and an element no
// field of the target struct claims, where the struct has no ",inline" map
// to collect it, with UNKNOWN_MEMBER. The registered codec keeps last-wins
// and skips unknown elements. Encoding is the registered codec's.
func NewStrict() codec.Codec {
	//: never added to the registry.
	return &strictCodec{}
}

// Unmarshal validates data like the registered codec, then refuses a
// repeated name, then decodes refusing an unknown member.
func (c *strictCodec) Unmarshal(data []byte, v any) error {
	//: a nil or non-pointer target is the caller's error, refused first (#1101).
	if terr := checkTarget(v); terr != nil {
		return terr
	}
	//: no bytes is no document in this format (#1101).
	if len(data) == 0 {
		return codec.EmptyInput
	}
	//: the registered codec's bounds and validation.
	if len(data) > maxBSONBytes || validateRoot(data) != nil {
		//: its refusal.
		return c.bsonCodec.Unmarshal(data, v)
	}
	//: one reading of the document.
	return decodeStrict(data, v)
}

// NewDecoder wraps r in a strict streaming codec.Decoder.
func (*strictCodec) NewDecoder(r io.Reader) codec.Decoder {
	d, _ := (&bsonCodec{}).NewDecoder(r).(*bsonDecoder)
	d.strict = true
	//: every document decoded strictly.
	return d
}

// decodeStrict decodes the validated document data into v strictly.
func decodeStrict(data []byte, v any) error {
	//: a repeated name anywhere refuses the whole document.
	if err := checkNames(data, 0); err != nil {
		//: nothing decoded.
		return err
	}
	//: the decode, refusing an unknown member.
	return decodeIntoMode(data, v, true)
}

// checkNames walks the validated document doc and refuses one holding two
// elements of one name; base is doc's offset in the input, for the message.
// An array's names are its indexes, which no decoder reads: only the
// documents inside it are walked.
func checkNames(doc []byte, base int) error {
	return checkDocument(doc, base, true)
}

// checkDocument walks a document, checking its names when names is set.
func checkDocument(doc []byte, base int, names bool) error {
	var small [strictLinearNames][]byte
	seen := small[:0]
	var many map[string]struct{}
	it := newDocIter(doc)
	//: element by element.
	for {
		at := base + lengthSize + it.pos
		el, more, err := it.next()
		//: the end, or bytes that changed.
		if !more || err != nil {
			//: walked.
			return err
		}
		//: the names of a document, not the indexes of an array — one this
		//: document already holds is refused, naming where, never the name.
		if names {
			var dup bool
			if seen, many, dup = addName(seen, many, el.key); dup {
				return duplicateName(at)
			}
		}
		//: the documents inside.
		if err := checkNested(el, at); err != nil {
			//: stop.
			return err
		}
	}
}

// addName records name among the element names one document has shown —
// seen while they are at most strictLinearNames, compared pairwise on the
// caller's stack, many once indexed — and reports whether the document
// already held it. It takes and returns the two by value, so seen's array
// stays the caller's stack array.
func addName(seen [][]byte, many map[string]struct{}, name []byte) (kept [][]byte, indexed map[string]struct{}, dup bool) {
	//: indexed: one lookup, one insert.
	if many != nil {
		return seen, many, indexName(many, name)
	}
	//: pairwise.
	for _, n := range seen {
		//: a repeat.
		if bytes.Equal(n, name) {
			return seen, nil, true
		}
	}
	//: room left on the stack: kept there.
	if len(seen) < strictLinearNames {
		return append(seen, name), nil, false
	}
	//: past the linear bound: index every name so far, then this one.
	many = make(map[string]struct{}, 4*strictLinearNames)
	for _, n := range seen {
		indexName(many, n)
	}
	return seen, many, indexName(many, name)
}

// indexName inserts name into many and reports whether it was there already.
func indexName(many map[string]struct{}, name []byte) bool {
	key := string(name)
	_, held := many[key]
	many[key] = struct{}{}
	return held
}

// checkNested walks the documents an element holds.
func checkNested(el element, at int) error {
	//: one rule per container type.
	switch el.typ {
	//: an embedded document.
	case typeDocument:
		return checkDocument(el.value, at, true)
	//: an array: its documents, not its index names.
	case typeArray:
		return checkDocument(el.value, at, false)
	//: a scalar.
	default:
		return nil
	}
}

// duplicateName is the strict refusal of a repeated element name.
func duplicateName(at int) error {
	//: the offset of the repeat; the name is a stranger's text.
	return errs.Wrap(nil, errs.WrapParams{
		Code:    corebson.CodeBSONDuplicateKey,
		Reason:  "DUPLICATE_KEY",
		Public:  "BSON document repeats an element name",
		Private: "service/data/codec/bson.NewStrict: a document holds two elements of one name",
	}, errs.Int(codec.FieldOffset, at))
}

// unknownMember is the strict refusal of an element no struct field claims.
func unknownMember(t reflect.Type) error {
	//: the struct, never the name.
	return errs.Wrap(nil, errs.WrapParams{
		Code:    corebson.CodeBSONUnknownMember,
		Reason:  "UNKNOWN_MEMBER",
		Public:  "BSON document carries an unknown member",
		Private: "service/data/codec/bson.NewStrict: an element no field of the struct claims",
	}, errs.String("type", t.String()))
}
