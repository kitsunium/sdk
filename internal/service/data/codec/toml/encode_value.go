// Package toml — values written after a key: scalars, strings in the spelling
// that needs no escape when there is one, arrays and inline tables.
package toml

import (
	"encoding"
	"math"
	"reflect"
	"strconv"
	"time"
	"unicode/utf8"

	coretoml "github.com/kitsunium/sdk/internal/core/data/codec/toml"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// rfc3339Nano is the layout of an offset date-time: RFC 3339 with as many
// fractional digits as the value needs.
const rfc3339Nano string = "2006-01-02T15:04:05.999999999Z07:00"

// indentSymbol indents the elements of a multi-line array.
const indentSymbol string = "  "

// tripleQuote delimits a multi-line basic string.
const tripleQuote string = `"""`

// hexDigits spells the hexadecimal digits of a \u escape.
const hexDigits string = "0123456789ABCDEF"

// The shifts of the four hexadecimal digits of a \u escape of one byte.
const (
	// highNibbleShift reaches the high nibble of a byte.
	highNibbleShift uint = 4
	// lowNibbleMask keeps the low nibble.
	lowNibbleMask byte = 0x0f
)

// appendValue writes v as a TOML value.
func (e *encoder) appendValue(b []byte, v reflect.Value, opts fieldOptions) ([]byte, error) {
	info := infoOf(v.Type())
	//: time.Time and the local types write themselves.
	if info.is(typeSpecial) {
		//: RFC 3339.
		return appendSpecial(b, v), nil
	}
	//: a TextMarshaler is written as the string it returns.
	if info.is(typeTextMarshalerPtr) || (info.is(typeTextMarshaler) && v.Kind() != reflect.String) {
		//: the text, as a string.
		return appendMarshaledText(b, v, info)
	}
	switch v.Kind() {
	//: a pointer or an interface is written as what it holds.
	case reflect.Pointer, reflect.Interface:
		return e.appendIndirect(b, v, opts)
	//: a string.
	case reflect.String:
		return appendStringValue(b, v.String(), opts.multiline)
	//: an array.
	case reflect.Slice, reflect.Array:
		return e.appendArray(b, v, opts.multiline)
	//: a table written where a value goes is an inline table.
	case reflect.Map, reflect.Struct:
		return e.appendInlineTable(b, v)
	//: a boolean or a number.
	default:
		return appendScalar(b, v)
	}
}

// appendIndirect writes what a pointer or an interface holds: a nil pointer
// as its type's zero value, as the previous library wrote it; a nil interface
// is refused, having no type to take a zero value from. Each level goes back
// through appendValue, which may find a TextMarshaler there, and the levels
// are counted, so a pointer that refers to itself is refused rather than
// followed until the stack runs out.
func (e *encoder) appendIndirect(b []byte, v reflect.Value, opts fieldOptions) ([]byte, error) {
	//: a chain no real type has: a pointer that refers to itself.
	if e.hops >= maxDepth {
		//: refused.
		return b, encodeFail(problemPointerLoop, v.Type())
	}
	e.hops++
	defer func() { e.hops-- }()
	//: a non-nil pointer or interface.
	if !v.IsNil() {
		//: what it holds.
		return e.appendValue(b, v.Elem(), opts)
	}
	//: a nil interface has no value and no type.
	if v.Kind() == reflect.Interface {
		//: refused.
		return b, encodeFail(problemNil, v.Type())
	}
	//: a nil pointer is its element's zero value.
	return e.appendValue(b, reflect.Zero(v.Type().Elem()), opts)
}

// appendScalar writes a boolean, an integer or a float.
func appendScalar(b []byte, v reflect.Value) ([]byte, error) {
	switch v.Kind() {
	//: true or false.
	case reflect.Bool:
		return strconv.AppendBool(b, v.Bool()), nil
	//: a signed integer.
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return strconv.AppendInt(b, v.Int(), int(decimalBase)), nil
	//: an unsigned integer, refused above the largest TOML integer.
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return appendUint(b, v)
	//: a float of either width.
	case reflect.Float32, reflect.Float64:
		return appendFloat(b, v.Float(), v.Type().Bits()), nil
	//: a channel, a function, a complex number or an unsafe pointer.
	default:
		return b, encodeFail(problemUnsupported, v.Type())
	}
}

// appendUint writes an unsigned integer, refusing one TOML's signed 64-bit
// integers cannot hold.
func appendUint(b []byte, v reflect.Value) ([]byte, error) {
	u := v.Uint()
	//: above the largest int64.
	if u > math.MaxInt64 {
		//: refused.
		return b, encodeFail(problemUintRange, v.Type())
	}
	//: the integer.
	return strconv.AppendUint(b, u, int(decimalBase)), nil
}

// appendSpecial writes time.Time as an offset date-time and the local types
// as their own text.
func appendSpecial(b []byte, v reflect.Value) []byte {
	switch v.Type() {
	//: an instant.
	case timeType:
		return valueOf[time.Time](v).AppendFormat(b, rfc3339Nano)
	//: a day.
	case localDateType:
		return valueOf[LocalDate](v).appendText(b)
	//: a time of day.
	case localTimeType:
		return valueOf[LocalTime](v).appendText(b)
	//: a date-time.
	default:
		return valueOf[LocalDateTime](v).appendText(b)
	}
}

// valueOf returns the T that v holds, without boxing a copy of it.
func valueOf[T any](v reflect.Value) T {
	value, _ := reflect.TypeAssert[T](v)
	//: the value; the caller checked the type.
	return value
}

// appendMarshaledText writes the text a TextMarshaler returns, as a string.
func appendMarshaledText(b []byte, v reflect.Value, info *typeInfo) ([]byte, error) {
	text, err := marshalText(v, info)
	//: the method failed.
	if err != nil {
		//: refused.
		return b, err
	}
	//: the text, as any string is written.
	return appendStringValue(b, string(text), false)
}

// marshalText calls the MarshalText method of v, through a copy's address when
// only *T has it and v has none.
func marshalText(v reflect.Value, info *typeInfo) ([]byte, error) {
	target := v
	//: the method needs a pointer.
	if info.is(typeTextMarshalerPtr) {
		//: an unaddressable value is copied to get one.
		if !v.CanAddr() {
			target = reflect.New(v.Type()).Elem()
			target.Set(v)
		}
		target = target.Addr()
	}
	m, _ := reflect.TypeAssert[encoding.TextMarshaler](target)
	text, err := m.MarshalText()
	//: the method's own refusal, kept underneath.
	if err != nil {
		//: refused.
		return nil, errs.Wrap(err, errs.WrapParams{
			Code:    coretoml.CodeTOMLMarshalFailed,
			Reason:  coretoml.MarshalFailed.Reason(),
			Public:  coretoml.MarshalFailed.Public(),
			Private: privateTextMarshalFailed,
		}, errs.String(fieldProblem, problemMarshalText), errs.String(fieldType, v.Type().String()))
	}
	//: the text.
	return text, nil
}

// appendStringValue writes a string: multi-line when multiline asks for it
// and the string has a newline, literal when it can be, basic otherwise. A
// string that is not UTF-8 is refused: TOML cannot carry it, and replacing
// its bytes would write a value the caller did not hold.
func appendStringValue(b []byte, s string, multiline bool) ([]byte, error) {
	//: TOML documents are UTF-8.
	if !utf8.ValidString(s) {
		//: refused.
		return b, encodeFail(problemNotUTF8, reflect.TypeFor[string]())
	}
	//: a multi-line string only when there is a line to break.
	if multiline && containsByte(s, charNewline) {
		//: """...""".
		return appendMultilineString(b, s), nil
	}
	//: '...' or "...".
	return appendString(b, s), nil
}

// appendKey writes a key: bare when every byte allows it, quoted otherwise.
func appendKey(b []byte, key string) []byte {
	//: a bare key.
	if isBareKey(key) {
		//: as is.
		return append(b, key...)
	}
	//: quoted.
	return appendString(b, key)
}

// isBareKey reports whether key can be written without quotes.
func isBareKey(key string) bool {
	//: an empty key must be quoted.
	if key == "" {
		//: not bare.
		return false
	}
	//: each byte.
	for i := range len(key) {
		//: a byte a bare key cannot hold.
		if !isBareKeyByte(key[i]) {
			//: not bare.
			return false
		}
	}
	//: bare.
	return true
}

// appendString writes s as a literal string when it can be one — no
// apostrophe, no control character, valid UTF-8 — and as a basic string
// otherwise.
func appendString(b []byte, s string) []byte {
	//: a literal string needs no escape.
	if canBeLiteral(s) {
		b = append(b, charApostrophe)
		b = append(b, s...)
		//: '...'.
		return append(b, charApostrophe)
	}
	//: "...".
	return appendBasicString(b, s)
}

// canBeLiteral reports whether s, which is valid UTF-8, can be written as a
// literal string.
func canBeLiteral(s string) bool {
	//: each byte.
	for i := range len(s) {
		c := s[i]
		//: an apostrophe or a control character, the tab included, needs a basic string.
		if c == charApostrophe || c == charDelete || c < charFirstPrintable {
			//: not literal.
			return false
		}
	}
	//: literal.
	return true
}

// appendBasicString writes s, which is valid UTF-8, as a basic string. Every
// string and key is checked before it gets here, so a byte past ASCII is part
// of a character and is copied as it is.
func appendBasicString(b []byte, s string) []byte {
	b = append(b, charQuote)
	//: each byte.
	for i := range len(s) {
		b = appendEscapedByte(b, s[i])
	}
	//: "...".
	return append(b, charQuote)
}

// appendEscapedByte writes one ASCII byte of a basic string, escaped when it
// must be.
func appendEscapedByte(b []byte, c byte) []byte {
	//: a byte with a one-character escape.
	if esc := basicEscapes[c]; esc != 0 {
		//: backslash and the letter.
		return append(b, charBackslash, esc)
	}
	//: another control character; a byte past ASCII is part of a character.
	if c < charFirstPrintable || c == charDelete {
		//: \u00XX.
		return appendUnicodeEscape(b, c)
	}
	//: printable.
	return append(b, c)
}

// appendUnicodeEscape writes \u00XX for byte c.
func appendUnicodeEscape(b []byte, c byte) []byte {
	//: four uppercase digits, as the previous library wrote them.
	return append(b, charBackslash, 'u', '0', '0', hexDigits[c>>highNibbleShift], hexDigits[c&lowNibbleMask])
}

// basicEscapes maps a byte to the letter of its one-character escape in a
// basic string, or to 0.
var basicEscapes = [byteValues]byte{
	'"':  '"',
	'\\': '\\',
	'\b': 'b',
	'\f': 'f',
	'\n': 'n',
	'\r': 'r',
	'\t': 't',
}

// appendMultilineString writes s as a multi-line basic string: newlines and
// tabs as they are, a run of three or more quotes escaped quote by quote.
func appendMultilineString(b []byte, s string) []byte {
	b = append(b, tripleQuote...)
	b = append(b, charNewline)
	//: each byte.
	for i := 0; i < len(s); {
		c := s[i]
		//: a run of quotes.
		if c == charQuote {
			run := quoteRunLength(s, i)
			b = appendQuoteRun(b, run)
			i += run
			continue
		}
		b = appendMultilineByte(b, c)
		i++
	}
	//: closed.
	return append(b, tripleQuote...)
}

// quoteRunLength returns how many quotes start at s[i].
func quoteRunLength(s string, i int) int {
	j := i
	//: the run.
	for j < len(s) && s[j] == charQuote {
		j++
	}
	//: its length.
	return j - i
}

// appendQuoteRun writes a run of quotes inside a multi-line string: as they
// are when there are fewer than three, escaped otherwise.
func appendQuoteRun(b []byte, run int) []byte {
	//: one or two quotes are content.
	if run < delimiterLength {
		//: as they are.
		for range run {
			b = append(b, charQuote)
		}
		//: written.
		return b
	}
	//: three would close the string: each is escaped.
	for range run {
		b = append(b, charBackslash, charQuote)
	}
	//: written.
	return b
}

// appendMultilineByte writes one byte of a multi-line basic string: newlines
// and tabs as they are, other control characters and the backslash escaped.
func appendMultilineByte(b []byte, c byte) []byte {
	//: a newline or a tab is content.
	if c == charNewline || c == charTab {
		//: as is.
		return append(b, c)
	}
	//: everything else as in a basic string.
	return appendEscapedByte(b, c)
}

// containsByte reports whether s contains c.
func containsByte(s string, c byte) bool {
	//: each byte.
	for i := range len(s) {
		//: found.
		if s[i] == c {
			//: yes.
			return true
		}
	}
	//: no.
	return false
}

// appendArray writes a slice or an array: on one line, or one element per
// line when multiline asks for it. Elements are written with no options: a
// table among them is an inline table.
func (e *encoder) appendArray(b []byte, v reflect.Value, multiline bool) ([]byte, error) {
	//: a cycle, or a structure nested past the cap.
	if e.depth >= maxDepth {
		//: refused.
		return b, encodeFail(problemTooDeepEncode, v.Type())
	}
	e.depth++
	defer func() { e.depth-- }()
	b = append(b, charOpenBracket)
	var err error
	//: each element.
	for i := range v.Len() {
		b = appendSeparator(b, i, multiline)
		b, err = e.appendValue(b, v.Index(i), fieldOptions{})
		//: refused.
		if err != nil {
			//: refused.
			return b, err
		}
	}
	//: a multi-line array closes on its own line.
	if multiline && v.Len() > 0 {
		b = append(b, charNewline)
	}
	//: closed.
	return append(b, charCloseBracket), nil
}

// appendSeparator writes what precedes element i of an array.
func appendSeparator(b []byte, i int, multiline bool) []byte {
	//: a comma after the previous element.
	if i > 0 {
		b = append(b, charComma)
	}
	//: one element per line, indented.
	if multiline {
		b = append(b, charNewline)
		//: indented.
		return append(b, indentSymbol...)
	}
	//: a space after the comma.
	if i > 0 {
		b = append(b, charSpace)
	}
	//: the separator.
	return b
}

// appendInlineTable writes a map or a struct as an inline table, on one line:
// its strings single-line whatever the field's tag, its tables inline too.
func (e *encoder) appendInlineTable(b []byte, v reflect.Value) ([]byte, error) {
	//: a cycle, or a structure nested past the cap.
	if e.depth >= maxDepth {
		//: refused.
		return b, encodeFail(problemTooDeepEncode, v.Type())
	}
	e.depth++
	defer func() { e.depth-- }()
	entries, err := e.collect(v)
	//: a map key that cannot be written.
	if err != nil {
		//: refused.
		return b, err
	}
	defer e.release(entries)
	b = append(b, charOpenBrace)
	//: each key-value.
	for i := range entries {
		//: separated by a comma.
		if i > 0 {
			b = append(b, charComma, charSpace)
		}
		b = appendKey(b, entries[i].key)
		b = append(b, " = "...)
		opts := entries[i].opts
		opts.multiline = false
		b, err = e.appendValue(b, entries[i].value, opts)
		//: refused.
		if err != nil {
			//: refused.
			return b, err
		}
	}
	//: closed.
	return append(b, charCloseBrace), nil
}
