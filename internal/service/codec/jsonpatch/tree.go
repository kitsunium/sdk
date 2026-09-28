// Package jsonpatch — a JSON document read into a tree, compared by value, and
// written back compact.
package jsonpatch

import (
	"bytes"
	"encoding/json/jsontext"
	"errors"
	"io"
	"math/big"
	"slices"
	"strconv"
	"strings"

	kerrs "github.com/kitsunium/sdk/internal/kernel/errs"
)

// The FNV-1a 64-bit parameters the digests are built with, and the width of
// the integers they take in.
const (
	// fnvOffset is FNV-1a's offset basis.
	fnvOffset uint64 = 14695981039346656037
	// fnvPrime is FNV-1a's prime.
	fnvPrime uint64 = 1099511628211
	// uint64Bytes is how many bytes an integer adds to a digest.
	uint64Bytes int = 8
	// byteBits is how far the next byte of an integer is shifted.
	byteBits uint = 8
)

// node is one JSON value.
type node struct {
	// fields holds an object's members by name.
	fields map[string]*node
	// text is a string's value, unescaped, or a number as written.
	text string
	// names are an object's member names, in the document's order.
	names []string
	// items are an array's elements.
	items []*node
	// sum is a digest of the value that two equal values share: equal
	// compares it first, so two values that differ are told apart in one
	// comparison however large they are, and aligning two arrays costs one
	// comparison a pair of elements rather than a walk of both.
	sum uint64
	// kind is the value's kind, as jsontext names it: 'n', 'f', 't', '"',
	// '0', '{' or '['.
	kind jsontext.Kind
}

// parse reads raw as exactly one JSON value, strictly — jsontext's defaults:
// no duplicate member name, valid UTF-8, nesting within its bound — and no
// trailing data. which names the document in a refusal.
func parse(which string, raw []byte) (*node, error) {
	dec := jsontext.NewDecoder(bytes.NewReader(raw))
	root, err := readValue(dec)
	//: not one well-formed value.
	if err != nil {
		//: NotJSON, where reading stopped.
		return nil, notJSON(which, dec.InputOffset(), err)
	}
	//: a second value, or anything but whitespace, after the first.
	if _, trailing := dec.ReadToken(); !errors.Is(trailing, io.EOF) {
		//: NotJSON, where the trailing data starts.
		return nil, notJSON(which, dec.InputOffset(), trailing)
	}
	//: the tree.
	return root, nil
}

// readValue reads the next value from dec into a node.
func readValue(dec *jsontext.Decoder) (*node, error) {
	tok, err := dec.ReadToken()
	//: nothing, or not a token.
	if err != nil {
		//: the decoder's refusal.
		return nil, err
	}
	// The token is void after the decoder's next call: its kind and its text
	// are taken now.
	value := &node{kind: tok.Kind()}
	switch value.kind {
	//: an object: its members, in order.
	case '{':
		err = readObject(dec, value)
	//: an array: its elements, in order.
	case '[':
		err = readArray(dec, value)
	//: a string, unescaped, or a number as written.
	case '"', '0':
		value.text = tok.String()
	//: null, true or false: the kind is the value.
	default:
	}
	//: the digest, from the children's, once the value is whole.
	if err == nil {
		value.sum = digest(value)
	}
	//: nil, or the decoder's refusal inside a container.
	return value, err
}

// digest returns the digest of n, whose children already have theirs: the
// kind, then what equal compares — a string's text, a number's sign, digits
// and power of ten (every zero alike), an array's elements in order, an
// object's members as a sum, so that their order changes nothing.
func digest(n *node) uint64 {
	h := fnvByte(fnvOffset, byte(n.kind))
	switch n.kind {
	//: the unescaped text.
	case '"':
		//: the string.
		return fnvString(h, n.text)
	//: the number, reduced as sameNumber reduces it.
	case '0':
		//: sign, digits, power of ten.
		return numberDigest(h, n.text)
	//: the elements' digests, in order.
	case '[':
		//: each element.
		for _, item := range n.items {
			h = fnvUint(h, item.sum)
		}
		//: the array.
		return h
	//: the members' digests, summed, so their order does not count.
	case '{':
		var members uint64
		//: each member: its name, with its value.
		for name, value := range n.fields {
			members += mix(fnvString(fnvOffset, name) ^ mix(value.sum))
		}
		//: the object.
		return fnvUint(fnvUint(h, uint64(len(n.fields))), members)
	//: null, true, false: the kind is the value.
	default:
		//: the literal.
		return h
	}
}

// numberDigest adds a number's sign, digits and power of ten to h, every zero
// alike, so that two numbers sameNumber calls one share it.
func numberDigest(h uint64, text string) uint64 {
	negative, digits, exponent, ok := canonical(text)
	switch {
	//: not a number jsontext accepted — unreachable — as written.
	case !ok:
		//: the text.
		return fnvString(h, text)
	//: zero, whatever its sign or its exponent.
	case digits == "":
		//: one digest for every zero.
		return fnvByte(h, '0')
	//: the digits and the power of ten.
	case negative:
		h = fnvByte(h, '-')
	//: positive.
	default:
	}
	//: the three parts.
	return fnvString(fnvString(h, digits), "e"+exponent.String())
}

// fnvByte adds one byte to an FNV-1a digest.
func fnvByte(h uint64, b byte) uint64 {
	//: xor, then multiply.
	return (h ^ uint64(b)) * fnvPrime
}

// fnvString adds s's bytes to an FNV-1a digest, after its length, so that
// two strings side by side never read as one.
func fnvString(h uint64, s string) uint64 {
	h = fnvUint(h, uint64(len(s)))
	//: each byte.
	for i := range len(s) {
		h = fnvByte(h, s[i])
	}
	//: the digest.
	return h
}

// fnvUint adds v's eight bytes to an FNV-1a digest.
func fnvUint(h, v uint64) uint64 {
	//: low byte first.
	for range uint64Bytes {
		h = fnvByte(h, byte(v))
		v >>= byteBits
	}
	//: the digest.
	return h
}

// mix scrambles v — splitmix64's finalizer — so that summing the members of
// an object does not let two members cancel out.
func mix(v uint64) uint64 {
	v ^= v >> 30
	v *= 0xbf58476d1ce4e5b9
	v ^= v >> 27
	v *= 0x94d049bb133111eb
	//: the last shift.
	return v ^ (v >> 31)
}

// readObject reads an object's members into obj, up to and including the
// closing brace.
func readObject(dec *jsontext.Decoder, obj *node) error {
	obj.fields = map[string]*node{}
	//: until the object ends.
	for dec.PeekKind() != '}' {
		name, member, err := readMember(dec)
		//: a malformed member, or a name given twice.
		if err != nil {
			//: the decoder's refusal.
			return err
		}
		obj.names = append(obj.names, name)
		obj.fields[name] = member
	}
	_, err := dec.ReadToken()
	//: nil, or a truncated document.
	return err
}

// readArray reads an array's elements into arr, up to and including the
// closing bracket.
func readArray(dec *jsontext.Decoder, arr *node) error {
	//: until the array ends.
	for dec.PeekKind() != ']' {
		item, err := readValue(dec)
		//: a malformed element.
		if err != nil {
			//: the decoder's refusal.
			return err
		}
		arr.items = append(arr.items, item)
	}
	_, err := dec.ReadToken()
	//: nil, or a truncated document.
	return err
}

// readMember reads one object member: its name, then its value. The name is
// taken out of its token at once: a token read from a decoder is void after
// the decoder's next call.
func readMember(dec *jsontext.Decoder) (name string, value *node, err error) {
	tok, err := dec.ReadToken()
	//: not a name — the decoder refuses a duplicate here too.
	if err != nil {
		//: the decoder's refusal.
		return "", nil, err
	}
	name = tok.String()
	value, err = readValue(dec)
	//: nil, or a malformed value.
	return name, value, err
}

// equal reports whether a and b are the same JSON value in RFC 6902's sense
// (§4.6): the same kind, strings equal once unescaped, numbers numerically
// equal, arrays equal element by element, objects with the same members,
// whatever their order.
func equal(a, b *node) bool {
	//: the digests, then the kind: two values that differ nearly always
	//: differ here, and two that are equal always agree.
	if a.sum != b.sum || a.kind != b.kind {
		//: different values.
		return false
	}
	switch a.kind {
	//: a string's value.
	case '"':
		//: unescaped, so "é" is "é".
		return a.text == b.text
	//: a number's value.
	case '0':
		//: 1, 1.0 and 1e0 are one number.
		return sameNumber(a.text, b.text)
	//: every element, in order.
	case '[':
		//: element by element.
		return slices.EqualFunc(a.items, b.items, equal)
	//: every member, by name.
	case '{':
		//: the same number of members, each equal to its namesake.
		return len(a.fields) == len(b.fields) && everyMember(a, b)
	//: null, true, false.
	default:
		//: the kind is the value.
		return true
	}
}

// everyMember reports whether every member of a has an equal namesake in b.
func everyMember(a, b *node) bool {
	//: by name.
	for name, value := range a.fields {
		other, found := b.fields[name]
		//: missing, or different.
		if !found || !equal(value, other) {
			//: different objects.
			return false
		}
	}
	//: the same members.
	return true
}

// sameNumber reports whether two JSON numbers, as written, are the same
// number. JSON numbers are exact decimals, so they are compared exactly:
// reduced to a sign, significant digits and a power of ten, in time linear in
// their text — never by computing ten to an exponent a document chose.
func sameNumber(a, b string) bool {
	//: written alike, which is the common case.
	if a == b {
		//: the same number.
		return true
	}
	negA, digitsA, expA, okA := canonical(a)
	negB, digitsB, expB, okB := canonical(b)
	//: an exponent big.Int cannot read — unreachable for a number jsontext
	//: accepted — is compared as written, and was not written alike.
	if !okA || !okB {
		//: different numbers.
		return false
	}
	//: zero, whatever its sign or exponent.
	if digitsA == "" || digitsB == "" {
		//: both zero, or not.
		return digitsA == digitsB
	}
	//: sign, digits and power of ten.
	return negA == negB && digitsA == digitsB && expA.Cmp(expB) == 0
}

// canonical reduces a JSON number to a sign, its significant digits without
// leading or trailing zeros — empty for zero — and the power of ten they are
// multiplied by; ok is false for an exponent that is not an integer, which a
// number jsontext accepted never has.
func canonical(number string) (negative bool, digits string, exponent *big.Int, ok bool) {
	negative = strings.HasPrefix(number, "-")
	mantissa, power, _ := strings.Cut(strings.TrimPrefix(number, "-"), "e")
	//: an upper-case exponent marker.
	if power == "" && strings.Contains(mantissa, "E") {
		mantissa, power, _ = strings.Cut(mantissa, "E")
	}
	integer, fraction, _ := strings.Cut(mantissa, ".")
	exponent = new(big.Int)
	//: the exponent as written, whatever its length: jsontext bounds nothing
	//: there, and a big.Int reads it in linear time.
	if power != "" {
		//: SetString leaves its receiver undefined when it fails.
		if _, parsed := exponent.SetString(strings.TrimPrefix(power, "+"), 10); !parsed {
			//: no number to reduce.
			return false, "", nil, false
		}
	}
	digits = strings.TrimLeft(integer+fraction, "0")
	exponent.Sub(exponent, big.NewInt(int64(len(fraction))))
	trimmed := strings.TrimRight(digits, "0")
	exponent.Add(exponent, big.NewInt(int64(len(digits)-len(trimmed))))
	//: the three parts.
	return negative, trimmed, exponent, true
}

// encode writes n compact: members in the document's order, a number as
// written, a string escaped as jsontext escapes it.
func encode(n *node) []byte {
	var buf bytes.Buffer
	enc := jsontext.NewEncoder(&buf)
	//: never fails: every value came out of a strict read.
	if err := write(enc, n); err != nil {
		//: unreachable; the value's kind, which says what it was.
		return []byte(strconv.Quote(n.kind.String()))
	}
	//: without the newline the encoder ends a top-level value with.
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n"))
}

// write writes n to enc.
func write(enc *jsontext.Encoder, n *node) error {
	switch n.kind {
	//: the members, in order.
	case '{':
		//: open, members, close.
		return writeObject(enc, n)
	//: the elements, in order.
	case '[':
		//: open, elements, close.
		return writeContainer(enc, jsontext.BeginArray, jsontext.EndArray, func() error {
			//: each element.
			for _, item := range n.items {
				//: the element.
				if err := write(enc, item); err != nil {
					//: the encoder's refusal.
					return err
				}
			}
			//: every element.
			return nil
		})
	//: a number, as written.
	case '0':
		//: the text is a valid JSON number.
		return enc.WriteValue(jsontext.Value(n.text))
	//: a string or a literal: one token.
	default:
		//: the token.
		return enc.WriteToken(scalarToken(n))
	}
}

// writeObject writes an object's members to enc, in the document's order.
func writeObject(enc *jsontext.Encoder, obj *node) error {
	//: open, members, close.
	return writeContainer(enc, jsontext.BeginObject, jsontext.EndObject, func() error {
		//: each member: its name, then its value.
		for _, name := range obj.names {
			//: the name.
			if err := enc.WriteToken(jsontext.String(name)); err != nil {
				//: the encoder's refusal.
				return err
			}
			//: the value.
			if err := write(enc, obj.fields[name]); err != nil {
				//: the encoder's refusal.
				return err
			}
		}
		//: every member.
		return nil
	})
}

// scalarToken is the token of a string, true, false or null.
func scalarToken(n *node) jsontext.Token {
	switch n.kind {
	//: a string, escaped by the encoder.
	case '"':
		//: its value.
		return jsontext.String(n.text)
	//: true.
	case 't':
		//: the literal.
		return jsontext.True
	//: false.
	case 'f':
		//: the literal.
		return jsontext.False
	//: null.
	default:
		//: the literal.
		return jsontext.Null
	}
}

// writeTokenner is what writing a container needs of an encoder.
type writeTokenner interface {
	// WriteToken writes one token.
	WriteToken(jsontext.Token) error
}

// writeContainer writes an object or an array: begin, what body writes, end.
func writeContainer(enc writeTokenner, begin, end jsontext.Token, body func() error) error {
	//: the opening token.
	if err := enc.WriteToken(begin); err != nil {
		//: the encoder's refusal.
		return err
	}
	//: the contents.
	if err := body(); err != nil {
		//: the encoder's refusal.
		return err
	}
	//: the closing token.
	return enc.WriteToken(end)
}

// notJSON is NotJSON naming which document and the offset reading stopped
// at. The decoder's error travels as a field only when it is a syntax error,
// whose message this package rebuilds from its offset alone: jsontext quotes
// the character it stopped at, and that character is the document's.
func notJSON(which string, offset int64, cause error) error {
	problem := "not one well-formed JSON value"
	//: the one refusal that has nothing to do with a character.
	if errors.Is(cause, io.ErrUnexpectedEOF) || errors.Is(cause, io.EOF) {
		problem = "the document ends before its value does"
	}
	//: the document and where, never what.
	return kerrs.Wrap(NotJSON, kerrs.WrapParams{},
		kerrs.String("document", which), kerrs.Int64("offset", offset), kerrs.String("problem", problem))
}
