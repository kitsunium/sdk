package jcs

import (
	"bytes"
	"encoding/json/jsontext"
	jsonv2 "encoding/json/v2"
	"errors"
	"io"
	"slices"
	"strconv"
	"unicode/utf16"
	"unicode/utf8"

	corejcs "github.com/kitsunium/sdk/internal/core/data/codec/jcs"
	"github.com/kitsunium/sdk/internal/kernel/concur/recycler"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/kernel/jsonenc"
)

// The escapes of RFC 8785 §3.2.2.2.
const (
	// hexDigits are the lower-case digits of a \u00XX escape.
	hexDigits = "0123456789abcdef"
	// firstPrintable is the first byte RFC 8785 writes as itself: below it
	// are the C0 controls, all escaped.
	firstPrintable byte = 0x20
	// firstSupplementary is the first character UTF-16 writes as a pair.
	firstSupplementary rune = 0x10000
)

// readerPool recycles the reader Canonicalize parses with: a jsontext.Decoder
// carries several kilobytes of state, which a document of a few members —
// a JWK's required members — would otherwise allocate on every call.
var readerPool = recycler.NewPool[*reader](func() *reader {
	//: a decoder over an empty reader, repointed on each use.
	r := &reader{}
	r.dec = jsontext.NewDecoder(&r.src)
	return r
})

// reader is a pooled decoder and the bytes.Reader it reads.
type reader struct {
	// src is the document being read.
	src bytes.Reader
	// dec reads src with jsontext's defaults, I-JSON's.
	dec *jsontext.Decoder
}

// member is one object member, canonicalized: its name, and where its value's
// canonical bytes sit in the object's buffer.
type member struct {
	// name is the member name, unescaped.
	name string
	// start and end delimit the value in the buffer.
	start, end int
}

// Canonicalize returns the RFC 8785 canonical form of the JSON document doc.
// It returns NotJSON — naming the offset, never a byte of the document — when
// doc is not exactly one I-JSON value, and NumberInvalid for a number past
// the largest finite double.
func Canonicalize(doc []byte) ([]byte, error) {
	//: a pooled decoder over doc; jsontext's defaults are I-JSON's —
	//: duplicate names and invalid UTF-8 refused.
	r := readerPool.Get()
	r.src.Reset(doc)
	r.dec.Reset(&r.src)
	//: the canonical form, then the reader forgets doc and goes back.
	out, err := canonicalize(doc, r.dec)
	r.src.Reset(nil)
	r.dec.Reset(&r.src)
	readerPool.Put(r)
	//: the bytes, or the refusal.
	return out, err
}

// canonicalize reads exactly one value from dec, the reader over doc, and
// returns its canonical form.
func canonicalize(doc []byte, dec tokenReader) ([]byte, error) {
	//: about the document's size: the canonical form drops its whitespace and
	//: may add escapes.
	out, err := appendValue(make([]byte, 0, len(doc)), dec)
	//: refused inside the value.
	if err != nil {
		return nil, err
	}
	//: exactly one value: nothing but whitespace after it.
	if _, terr := dec.ReadToken(); !errors.Is(terr, io.EOF) {
		//: trailing data, or a truncated stream.
		return nil, notJSON(dec)
	}
	//: the canonical bytes.
	return out, nil
}

// Marshal returns the canonical form of v: its encoding/json/v2 encoding —
// the default options, which refuse invalid UTF-8 as I-JSON does — then
// Canonicalize. A value json/v2 refuses is MarshalFailed.
func Marshal(v any) ([]byte, error) {
	//: the value's JSON, under json/v2's defaults.
	doc, err := jsonv2.Marshal(v)
	//: refused by the encoder; its error names no value.
	if err != nil {
		return nil, errs.Wrap(err, errs.WrapParams{
			Code:    corejcs.CodeJCSMarshalFailed,
			Reason:  "MARSHAL_FAILED",
			Public:  "The value has no JSON encoding",
			Private: "service/data/codec/jcs.Marshal: encoding/json/v2 refused the value",
		})
	}
	//: the canonical form of that JSON.
	return Canonicalize(doc)
}

// appendValue appends the canonical form of the next value of dec.
func appendValue(dst []byte, dec tokenReader) ([]byte, error) {
	//: by the value's kind.
	switch dec.PeekKind() {
	//: an object: members sorted by name.
	case '{':
		return appendObject(dst, dec)
	//: an array: elements in order.
	case '[':
		return appendArray(dst, dec)
	//: a number: ECMAScript's spelling of its double.
	case '0':
		return appendNumber(dst, dec)
	//: a string, or a literal.
	default:
		return appendScalar(dst, dec)
	}
}

// appendScalar appends a string or a literal.
func appendScalar(dst []byte, dec tokenReader) ([]byte, error) {
	//: the token, validated by the reader.
	tok, err := dec.ReadToken()
	//: malformed, or not a value at all.
	if err != nil {
		return nil, notJSON(dec)
	}
	//: by kind.
	switch tok.Kind() {
	//: a string, re-escaped.
	case '"':
		return appendString(dst, tok.String()), nil
	//: the three literals as written.
	case 'n':
		return append(dst, "null"...), nil
	//: true.
	case 't':
		return append(dst, "true"...), nil
	//: false.
	case 'f':
		return append(dst, "false"...), nil
	//: a closing delimiter where a value was expected.
	default:
		return nil, notJSON(dec)
	}
}

// appendNumber appends a number as ECMAScript writes its IEEE 754 double.
func appendNumber(dst []byte, dec tokenReader) ([]byte, error) {
	//: the literal, validated by the reader.
	raw, err := dec.ReadValue()
	//: malformed.
	if err != nil {
		return nil, notJSON(dec)
	}
	//: the nearest double; past the largest finite one is a range error.
	f, perr := strconv.ParseFloat(string(raw), 64)
	//: no double holds it.
	if perr != nil {
		return nil, errs.Wrap(corejcs.NumberInvalid, errs.WrapParams{}, errs.Int64("offset", dec.InputOffset()))
	}
	//: ECMAScript's spelling.
	return appendFloat(dst, f), nil
}

// appendFloat appends f as ECMAScript's Number::toString writes it (RFC 8785
// §3.2.2.3): the shortest digits that read back as f, in decimal notation for
// a magnitude from 1e-6 up to 1e21 and in exponent notation outside it, with
// the exponent's sign and no leading zero; -0 is 0. f must be finite.
//
// That is encoding/json's spelling of a float64, ES6's by design, which
// jsonenc.AppendFloat writes for every emitter of the SDK (#1039) — but for
// the negative zero, which encoding/json writes -0 and ECMAScript 0.
func appendFloat(dst []byte, f float64) []byte {
	//: both zeros are 0.
	if f == 0 {
		return append(dst, '0')
	}
	//: finite, read by ParseFloat without a range error: always a number.
	number, _ := jsonenc.AppendFloat(dst, f)
	return number
}

// appendString appends s as RFC 8785 §3.2.2.2 writes a string: '"' and '\'
// escaped, the control characters as \b \t \n \f \r or \u00xx, everything
// else as itself.
func appendString(dst []byte, s string) []byte {
	//: the opening quote.
	dst = append(dst, '"')
	//: runs of plain bytes copied whole.
	plain := 0
	for i := range len(s) {
		//: a byte to escape ends the run.
		c := s[i]
		if c >= firstPrintable && c != '"' && c != '\\' {
			continue
		}
		//: the run, then the escape.
		dst = append(dst, s[plain:i]...)
		dst = appendEscape(dst, c)
		plain = i + 1
	}
	//: the last run and the closing quote.
	dst = append(dst, s[plain:]...)
	return append(dst, '"')
}

// appendEscape appends the escape of c, a quote, a backslash or a control
// character.
func appendEscape(dst []byte, c byte) []byte {
	//: the named escapes.
	switch c {
	//: quote.
	case '"':
		return append(dst, '\\', '"')
	//: backslash.
	case '\\':
		return append(dst, '\\', '\\')
	//: backspace.
	case '\b':
		return append(dst, '\\', 'b')
	//: tab.
	case '\t':
		return append(dst, '\\', 't')
	//: line feed.
	case '\n':
		return append(dst, '\\', 'n')
	//: form feed.
	case '\f':
		return append(dst, '\\', 'f')
	//: carriage return.
	case '\r':
		return append(dst, '\\', 'r')
	//: any other control character, lower-case hex.
	default:
		return append(dst, '\\', 'u', '0', '0', hexDigits[c>>4], hexDigits[c&0xF])
	}
}

// appendArray appends an array, its elements canonicalized in order.
func appendArray(dst []byte, dec tokenReader) ([]byte, error) {
	//: the opening bracket.
	if _, err := dec.ReadToken(); err != nil {
		return nil, notJSON(dec)
	}
	dst = append(dst, '[')
	//: each element, comma-separated.
	for first := true; dec.PeekKind() != ']'; first = false {
		//: the separator.
		if !first {
			dst = append(dst, ',')
		}
		//: the element.
		var err error
		if dst, err = appendValue(dst, dec); err != nil {
			return nil, err
		}
	}
	//: the closing bracket, or a truncation.
	if _, err := dec.ReadToken(); err != nil {
		return nil, notJSON(dec)
	}
	return append(dst, ']'), nil
}

// appendObject appends an object: each member's value canonicalized into a
// buffer of the object's own, then the members written in the order of their
// names' UTF-16 code units (§3.2.3).
func appendObject(dst []byte, dec tokenReader) ([]byte, error) {
	//: the opening brace.
	if _, err := dec.ReadToken(); err != nil {
		return nil, notJSON(dec)
	}
	//: the members and their values.
	members, values, err := readMembers(dec)
	if err != nil {
		return nil, err
	}
	//: the members in UTF-16 order.
	slices.SortFunc(members, func(a, b member) int { return compareUTF16(a.name, b.name) })
	//: written with no whitespace.
	return appendMembers(dst, members, values), nil
}

// readMembers reads an object's members up to and including its closing
// brace: each name, and its value canonicalized into values.
func readMembers(dec tokenReader) (members []member, values []byte, err error) {
	//: until the closing brace.
	for dec.PeekKind() != '}' {
		//: the name, copied before the next read voids the token; jsontext
		//: refuses a duplicate.
		tok, terr := dec.ReadToken()
		if terr != nil || tok.Kind() != '"' {
			return nil, nil, notJSON(dec)
		}
		name := tok.String()
		//: the value, canonicalized after the others.
		start := len(values)
		if values, err = appendValue(values, dec); err != nil {
			return nil, nil, err
		}
		members = append(members, member{name: name, start: start, end: len(values)})
	}
	//: the closing brace, or a truncation.
	if _, err = dec.ReadToken(); err != nil {
		return nil, nil, notJSON(dec)
	}
	//: every member read.
	return members, values, nil
}

// appendMembers writes sorted members as an object, with no whitespace.
func appendMembers(dst []byte, members []member, values []byte) []byte {
	//: the opening brace.
	dst = append(dst, '{')
	for i, m := range members {
		//: the separator.
		if i > 0 {
			dst = append(dst, ',')
		}
		//: name, colon, value.
		dst = appendString(dst, m.name)
		dst = append(dst, ':')
		dst = append(dst, values[m.start:m.end]...)
	}
	//: the closing brace.
	return append(dst, '}')
}

// compareUTF16 orders two valid UTF-8 strings by their UTF-16 code units:
// byte order but where a character past U+FFFF — a surrogate pair, 0xD800 up
// — sorts before one from U+E000 to U+FFFF.
func compareUTF16(a, b string) int {
	//: rune by rune until they differ.
	for a != "" && b != "" {
		//: the next character of each.
		ra, na := utf8.DecodeRuneInString(a)
		rb, nb := utf8.DecodeRuneInString(b)
		//: the first code unit decides.
		if ua, ub := firstUnit(ra), firstUnit(rb); ua != ub {
			return int(ua) - int(ub)
		}
		//: same first unit: for a pair, the second decides.
		if ra != rb {
			return int(ra) - int(rb)
		}
		a, b = a[na:], b[nb:]
	}
	//: a prefix sorts first.
	return len(a) - len(b)
}

// firstUnit is r's first UTF-16 code unit.
func firstUnit(r rune) rune {
	//: a character of the basic plane is its own unit.
	if r < firstSupplementary {
		return r
	}
	//: the high surrogate.
	hi, _ := utf16.EncodeRune(r)
	return hi
}

// inputOffsetter is what notJSON reads the offset from: the jsontext.Decoder.
type inputOffsetter interface {
	// InputOffset is the offset of the reader in the document.
	InputOffset() int64
}

// tokenReader is the part of jsontext.Decoder the canonical writer reads
// with.
type tokenReader interface {
	inputOffsetter
	// PeekKind is the kind of the next token, without reading it.
	PeekKind() jsontext.Kind
	// ReadToken reads the next token.
	ReadToken() (jsontext.Token, error)
	// ReadValue reads the next value whole.
	ReadValue() (jsontext.Value, error)
}

// notJSON is NotJSON at the reader's offset.
func notJSON(dec inputOffsetter) error {
	//: the offset, never the content.
	return errs.Wrap(corejcs.NotJSON, errs.WrapParams{}, errs.Int64("offset", dec.InputOffset()))
}
