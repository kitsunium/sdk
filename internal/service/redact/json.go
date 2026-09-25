// Package redact — copying a JSON document with its secrets replaced, within
// an exact byte bound.
package redact

import (
	"bytes"
	"encoding/json"
	"encoding/json/jsontext"
	"errors"
	"io"
	"reflect"
	"unicode/utf8"

	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// Byte costs the bound accounts for.
const (
	// minValueBytes is the smallest value a cut can still write where a value
	// is owed: the Ellipsis as a JSON string, `"…"`.
	minValueBytes int = len(`"` + Ellipsis + `"`)
	// quoteBytes is the two quotation marks around a string.
	quoteBytes int = 2
	// separatorBytes is a comma between members or elements, or the colon
	// after a member name.
	separatorBytes int = 1
)

// quotedPlaceholder is the Placeholder as a JSON string.
const quotedPlaceholder string = `"` + Placeholder + `"`

// truncatedValue is what a value that cannot fit becomes.
const truncatedValue string = `"` + Ellipsis + `"`

// fieldOffset carries where the reader refused a document.
const fieldOffset string = "offset"

// hexDigits spells the four digits of a \uXXXX escape.
const hexDigits string = "0123456789abcdef"

// lineSeparator and paragraphSeparator are legal inside a JSON string and end
// a line in JavaScript, so a document shown in a page is escaped for them.
const (
	lineSeparator      rune = 0x2028
	paragraphSeparator rune = 0x2029
)

// shortEscapes maps each character JSON escapes with a backslash and one
// letter — or with a backslash and itself — to that letter.
var shortEscapes = map[rune]byte{
	'"': '"', '\\': '\\', '\b': 'b', '\f': 'f', '\n': 'n', '\r': 'r', '\t': 't',
}

// DocumentValue is a JSON document with its secrets replaced, and whether it
// had to be cut to fit its bound.
type DocumentValue struct {
	// JSON is always one well-formed JSON value, never longer than the bound.
	JSON json.RawMessage
	// Truncated reports that members, elements or the tail of a string were
	// left out to fit the bound. The containers that were cut are still
	// closed, so JSON stays well-formed.
	Truncated bool
}

// JSON returns document with its secrets replaced, at most maxBytes long
// (raised to MinBytes): every member whose name the Redactor recognises has
// its value, whatever it is, replaced by Placeholder, and every string has
// the credentials of the URLs in it replaced.
//
// document is read, never modified. One that is not exactly one JSON value is
// refused with DocumentInvalid and nothing is returned for it: a partial copy
// of a document that does not parse cannot be trusted to have had its secrets
// recognised. Duplicate member names and invalid UTF-8 are tolerated — this
// is for showing a document, not for accepting one — and invalid UTF-8 is
// shown as U+FFFD.
func (r *Redactor) JSON(document []byte, maxBytes int) (redacted DocumentValue, err error) {
	//: a document has no Go type, so only names and URLs are judged.
	return r.redact(document, nil, maxBytes)
}

// Value returns v encoded as encoding/json would put it on the wire, with its
// secrets replaced, at most maxBytes long (raised to MinBytes). Beyond what
// JSON recognises by name, a struct field the Redactor's tag or field rule
// declares secret is replaced too — found by walking v's type the way
// encoding/json lays it out, once per type. A type that writes its own JSON
// (json.Marshaler, encoding.TextMarshaler) is opaque to that walk, and only
// the names in its output are judged.
//
// v is encoded, never modified. A value encoding/json refuses — a channel, a
// function, a cycle — is refused with ValueUnencodable.
func (r *Redactor) Value(v any, maxBytes int) (redacted DocumentValue, err error) {
	encoded, encodeErr := json.Marshal(v)
	//: nothing on the wire, nothing to redact.
	if encodeErr != nil {
		//: the type, never the value: the value is what may be secret.
		return DocumentValue{}, errs.Wrap(ValueUnencodable, errs.WrapParams{},
			errs.String("type", reflect.TypeOf(v).String()))
	}
	//: nil encodes as null and declares nothing.
	var declared *plan
	if v != nil {
		declared = r.planOf(reflect.TypeOf(v))
	}
	//: the wire form, judged by names and by declaration.
	return r.redact(encoded, declared, maxBytes)
}

// redact copies one document with its secrets replaced, within the bound.
func (r *Redactor) redact(document []byte, declared *plan, maxBytes int) (DocumentValue, error) {
	limit := bound(maxBytes)
	c := copier{
		redactor: r,
		decoder: jsontext.NewDecoder(bytes.NewReader(document),
			jsontext.AllowDuplicateNames(true), jsontext.AllowInvalidUTF8(true)),
		bound: limit,
		out:   make([]byte, 0, min(len(document), limit)),
	}
	//: the document, or the reason it is not one.
	if copyErr := c.value(declared, false); copyErr != nil {
		//: nothing returned for a document that does not parse.
		return DocumentValue{}, c.invalid()
	}
	//: exactly one value: anything after it is not part of a document.
	if _, trailingErr := c.decoder.ReadToken(); !errors.Is(trailingErr, io.EOF) {
		//: a second value, or garbage.
		return DocumentValue{}, c.invalid()
	}
	//: never longer than the bound, always well-formed.
	return DocumentValue{JSON: c.out, Truncated: c.cut}, nil
}

// copier streams one JSON value from decoder into out, writing nothing that
// would leave too little room for the closers it owes.
type copier struct {
	redactor *Redactor
	decoder  *jsontext.Decoder
	out      []byte
	bound    int
	// depth is how many containers are open: one closing byte owed for each.
	depth int
	// cut is set once anything was left out; from then on nothing more is
	// written, and every open container is closed as it ends.
	cut bool
}

// room returns how many bytes the next token may take, the closers owed kept
// aside.
func (c *copier) room() int {
	//: the bound, minus what is written, minus one byte per open container.
	return c.bound - len(c.out) - c.depth
}

// invalid is the refusal of a document the reader could not read.
func (c *copier) invalid() error {
	//: where the reader stopped, never what it read.
	return errs.Wrap(DocumentInvalid, errs.WrapParams{}, errs.Int64(fieldOffset, c.decoder.InputOffset()))
}

// value copies the next value. A secret one — by its member's name, or by
// declaration — is skipped and written as the Placeholder.
//
// The caller guarantees room for at least minValueBytes.
func (c *copier) value(declared *plan, secretName bool) error {
	//: whatever it holds, it is not shown.
	if secretName || (declared != nil && declared.secret) {
		//: the value is read past, never copied.
		if skipErr := c.decoder.SkipValue(); skipErr != nil {
			//: malformed.
			return skipErr
		}
		c.scalar(quotedPlaceholder)
		//: replaced.
		return nil
	}
	token, readErr := c.decoder.ReadToken()
	//: malformed, or ended early.
	if readErr != nil {
		//: the caller refuses the whole document.
		return readErr
	}
	switch token.Kind() {
	//: an object.
	case jsontext.KindBeginObject:
		//: members, judged by name and by declaration.
		return c.object(declared)
	//: an array.
	case jsontext.KindBeginArray:
		//: elements, judged by declaration.
		return c.array(declared)
	//: a string: its URLs lose their credentials.
	case jsontext.KindString:
		c.text(c.redactor.scrub(token.String()))
	//: a number, true, false or null: written as read, or not at all.
	default:
		c.scalar(token.String())
	}
	//: copied.
	return nil
}

// object copies the members of an object whose opening brace was read.
func (c *copier) object(declared *plan) error {
	c.open('{')
	first := true
	//: until the closing brace.
	for c.decoder.PeekKind() != jsontext.KindEndObject {
		nameToken, readErr := c.decoder.ReadToken()
		//: malformed, or ended inside the object.
		if readErr != nil {
			//: refused.
			return readErr
		}
		name := nameToken.String()
		//: nothing more is written once something was left out, but the input
		//: is still read, so a malformed tail is still refused.
		if c.cut || !c.fitsMember(first, name) {
			c.cut = true
			//: read past the value, write nothing.
			if skipErr := c.decoder.SkipValue(); skipErr != nil {
				//: refused.
				return skipErr
			}
			continue
		}
		c.member(first, name)
		first = false
		//: judged by its own name and by what the type declared for it.
		if copyErr := c.value(declared.member(name), c.redactor.Name(name)); copyErr != nil {
			//: refused.
			return copyErr
		}
	}
	//: the closing brace itself.
	return c.close('}')
}

// array copies the elements of an array whose opening bracket was read.
func (c *copier) array(declared *plan) error {
	c.open('[')
	first := true
	//: until the closing bracket.
	for c.decoder.PeekKind() != jsontext.KindEndArray {
		//: once something was left out, or when not even a truncated value
		//: fits, the rest is read past.
		if c.cut || c.room() < c.separator(first)+minValueBytes {
			c.cut = true
			//: read past the element, write nothing.
			if skipErr := c.decoder.SkipValue(); skipErr != nil {
				//: refused.
				return skipErr
			}
			continue
		}
		//: elements are separated by commas.
		if !first {
			c.out = append(c.out, ',')
		}
		first = false
		//: an element has no name to judge; only its declaration.
		if copyErr := c.value(declared.item(), false); copyErr != nil {
			//: refused.
			return copyErr
		}
	}
	//: the closing bracket itself.
	return c.close(']')
}

// open writes an opening brace or bracket and owes its closer.
func (c *copier) open(opener byte) {
	c.out = append(c.out, opener)
	c.depth++
}

// close reads the closing token and writes the closer that was owed.
func (c *copier) close(closer byte) error {
	//: the closing brace or bracket, which the loop only peeked at.
	if _, readErr := c.decoder.ReadToken(); readErr != nil {
		//: refused.
		return readErr
	}
	c.depth--
	c.out = append(c.out, closer)
	//: closed.
	return nil
}

// separator returns the byte a member or element after the first costs.
func (c *copier) separator(first bool) int {
	//: the first one needs no comma.
	if first {
		//: nothing.
		return 0
	}
	//: one comma.
	return separatorBytes
}

// fitsMember reports whether a member named name fits: its separator, its
// quoted name, the colon, and the smallest value a cut could still write.
func (c *copier) fitsMember(first bool, name string) bool {
	//: the name is written in full or not at all.
	return c.separator(first)+quotedLength(name)+separatorBytes+minValueBytes <= c.room()
}

// member writes a member's separator, name and colon. The caller checked
// that they fit.
func (c *copier) member(first bool, name string) {
	//: members are separated by commas.
	if !first {
		c.out = append(c.out, ',')
	}
	c.out = appendQuoted(c.out, name)
	c.out = append(c.out, ':')
}

// scalar writes a token whole, or the truncated value when it does not fit.
// The caller guarantees room for the truncated value.
func (c *copier) scalar(token string) {
	//: whole.
	if len(token) <= c.room() {
		c.out = append(c.out, token...)
		return
	}
	//: a number cannot be cut, so the value becomes the marker.
	c.out = append(c.out, truncatedValue...)
	c.cut = true
}

// text writes a string whole, or as long a prefix as fits followed by the
// Ellipsis. The caller guarantees room for the truncated value.
func (c *copier) text(value string) {
	room := c.room()
	//: whole, which is the ordinary case.
	if quotedLength(value) <= room {
		c.out = appendQuoted(c.out, value)
		return
	}
	//: the prefix budget: the room less the quotes and the Ellipsis.
	budget := room - quoteBytes - len(Ellipsis)
	c.out = append(c.out, '"')
	//: rune by rune, so an escape is never split.
	for _, character := range value {
		escaped := escapedLength(character)
		//: the next rune would not fit.
		if escaped > budget {
			break
		}
		c.out = appendEscaped(c.out, character)
		budget -= escaped
	}
	c.out = append(c.out, Ellipsis...)
	c.out = append(c.out, '"')
	c.cut = true
}

// quotedLength returns the length of value as a JSON string.
func quotedLength(value string) int {
	length := quoteBytes
	//: rune by rune, as appendQuoted writes it.
	for _, character := range value {
		length += escapedLength(character)
	}
	//: with its quotes.
	return length
}

// appendQuoted appends value as a JSON string.
func appendQuoted(out []byte, value string) []byte {
	out = append(out, '"')
	//: rune by rune; invalid UTF-8 ranges as U+FFFD, which is what is shown.
	for _, character := range value {
		out = appendEscaped(out, character)
	}
	//: closed.
	return append(out, '"')
}

// escapedLength returns how many bytes appendEscaped writes for character.
func escapedLength(character rune) int {
	//: the two characters JSON always escapes, and the short control escapes.
	if _, short := shortEscapes[character]; short {
		//: a backslash and a letter.
		return len(`\n`)
	}
	//: every other control character, and the two separators.
	if needsUnicodeEscape(character) {
		//: \u and four hex digits.
		return len(`\u0000`)
	}
	//: everything else is written as its UTF-8, one to four bytes.
	return utf8.RuneLen(character)
}

// needsUnicodeEscape reports whether character is written as \uXXXX: a control
// character without a short escape, or one of the two separators.
func needsUnicodeEscape(character rune) bool {
	//: below the space, or a separator JavaScript would end a line on.
	return character < ' ' || character == lineSeparator || character == paragraphSeparator
}

// appendEscaped appends one character of a JSON string, escaped where RFC
// 8259 requires it or a JavaScript consumer would break.
func appendEscaped(out []byte, character rune) []byte {
	//: a quote, a backslash, or a control character with a short escape.
	if letter, short := shortEscapes[character]; short {
		//: a backslash and one letter.
		return append(out, '\\', letter)
	}
	//: the remaining control characters and the two separators.
	if needsUnicodeEscape(character) {
		//: four hex digits, most significant first.
		return append(out, '\\', 'u', hexDigits[character>>12&0xf], hexDigits[character>>8&0xf],
			hexDigits[character>>4&0xf], hexDigits[character&0xf])
	}
	//: as UTF-8.
	return utf8.AppendRune(out, character)
}
