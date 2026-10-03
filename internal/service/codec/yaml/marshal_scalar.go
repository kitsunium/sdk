// Package yaml — encoding: how a scalar is written so every reader reads it back.
package yaml

import (
	"bytes"
	"math"
	"strconv"
	"strings"
	"unicode/utf8"
)

// The numbers the encoder formats with.
const (
	// decimalBase is the base integers are written in.
	decimalBase int = 10
	// float64Bits is the precision a float64 is written at.
	float64Bits int = 64
	// float32Bits is the precision a float32 is read at.
	float32Bits int = 32
)

// literalIndicator is the indentation indicator a literal block carries when
// its first line starts with a blank or a line break: the content always sits
// one step deeper than its parent, so the indicator is always the step.
const literalIndicator byte = '0' + byte(indentStep)

// The characters that never appear verbatim in a scalar the encoder writes.
const (
	// runeDelete is DEL, U+007F.
	runeDelete rune = 0x7F
	// c1First is the first C1 control character, U+0080.
	c1First rune = 0x80
	// c1Last is the last C1 control character, U+009F.
	c1Last rune = 0x9F
	// runeByteOrderMark is U+FEFF.
	runeByteOrderMark rune = 0xFEFF
	// runeNonCharacterFFFE is U+FFFE, a non-character.
	runeNonCharacterFFFE rune = 0xFFFE
	// runeNonCharacterFFFF is U+FFFF, a non-character.
	runeNonCharacterFFFF rune = 0xFFFF
)

// The hexadecimal escapes the encoder writes.
const (
	// hexDigitsUpper are the digits an escape is written with.
	hexDigitsUpper string = "0123456789ABCDEF"
	// maxByteEscape is the largest code point written as \xXX.
	maxByteEscape rune = 0xFF
	// nibbleBits is the width of one hexadecimal digit.
	nibbleBits int = 4
	// nibbleMask keeps one hexadecimal digit.
	nibbleMask rune = 0xF
)

// plainUnsafeFirst lists the characters a plain scalar the encoder writes
// never starts with: every indicator, and the blanks a reader would strip.
const plainUnsafeFirst string = "-?:,[]{}#&*!|>'\"%@` \t"

// flowUnsafe lists the characters the encoder never writes in a plain scalar
// inside a flow collection.
const flowUnsafe string = ",[]{}:#"

// shortEscapes maps the characters a double-quoted scalar escapes by name to
// their escape.
var shortEscapes = map[rune]string{
	'"': `\"`, '\\': `\\`, 0: `\0`, '\a': `\a`, '\b': `\b`, '\t': `\t`,
	'\n': `\n`, '\v': `\v`, '\f': `\f`, '\r': `\r`, runeEscape: `\e`,
	runeNextLine: `\N`, runeLineSeparator: `\L`, runeParagraphSeparator: `\P`,
}

// validText reports whether s is UTF-8, which every YAML scalar is.
func validText(s string) bool {
	//: arbitrary bytes have no representation.
	return utf8.ValidString(s)
}

// printable reports whether r may appear verbatim in any scalar the encoder
// writes.
func printable(r rune) bool {
	//: neither a control character nor a character some reader mistakes.
	return !isControl(r) && !isMisread(r)
}

// isControl reports whether r is a C0 or C1 control character, or DEL.
func isControl(r rune) bool {
	//: below the space, DEL, or the C1 block.
	return r < ' ' || r == runeDelete || (r >= c1First && r <= c1Last)
}

// isMisread reports whether r is a character a reader may take for something
// else: a line break to YAML 1.1, a byte order mark, a non-character.
func isMisread(r rune) bool {
	//: U+2028, U+2029, U+FEFF, U+FFFE, U+FFFF.
	return r == runeLineSeparator || r == runeParagraphSeparator || r == runeByteOrderMark ||
		r == runeNonCharacterFFFE || r == runeNonCharacterFFFF
}

// plainSafe reports whether s can be written as a plain scalar and read back
// as the same string by every reader: it is not empty, its edges and its
// inside hold nothing a reader takes for an indicator, every character is
// printable and none is a tab, no reader takes it for anything but a string,
// and it is neither a document marker nor the merge key — which yaml.v3 reads
// as a merge and the subset refuses as a key. flow refuses the flow
// indicators too.
func plainSafe(s string, flow bool) bool {
	//: every condition.
	return s != "" && s != mergeKey && plainEdgesSafe(s) && !plainInsideUnsafe(s, flow) &&
		allPlainPrintable(s) && !readsAsNonString(s) && !startsWithMarker(s)
}

// plainEdgesSafe reports whether s neither starts with an indicator or a
// blank nor ends with a blank or a ":".
func plainEdgesSafe(s string) bool {
	last := s[len(s)-1]
	//: a safe first character and a safe last one.
	return strings.IndexByte(plainUnsafeFirst, s[0]) < 0 && last != ' ' && last != ':'
}

// plainInsideUnsafe reports whether s holds a value indicator or a comment —
// and, in flow context, a flow indicator, a ":" or a "#" anywhere.
func plainInsideUnsafe(s string, flow bool) bool {
	//: ": ", " #", or a flow character.
	return strings.Contains(s, ": ") || strings.Contains(s, " #") || (flow && strings.ContainsAny(s, flowUnsafe))
}

// allPlainPrintable reports whether every character of s is printable and
// none is a tab.
func allPlainPrintable(s string) bool {
	//: no character to escape.
	return !strings.ContainsFunc(s, func(r rune) bool { return r == '\t' || !printable(r) })
}

// startsWithMarker reports whether s starts like a document marker.
func startsWithMarker(s string) bool {
	//: "---" or "...".
	return strings.HasPrefix(s, "---") || strings.HasPrefix(s, "...")
}

// appendDoubleQuoted writes s as a double-quoted scalar, escaping what must
// be: the quote, the backslash, and every character printable rejects.
func appendDoubleQuoted(buf *bytes.Buffer, s string) {
	buf.WriteByte('"')
	//: every character.
	for _, r := range s {
		appendQuotedRune(buf, r)
	}
	buf.WriteByte('"')
}

// appendQuotedRune writes r inside a double-quoted scalar: its short escape,
// a hexadecimal escape, or itself.
func appendQuotedRune(buf *bytes.Buffer, r rune) {
	//: an escape by name.
	if escape, ok := shortEscapes[r]; ok {
		buf.WriteString(escape)
		return
	}
	//: a character with no short escape.
	if !printable(r) {
		appendHexEscape(buf, r)
		return
	}
	buf.WriteRune(r)
}

// appendHexEscape writes r as \xXX when it fits a byte, \uXXXX otherwise. A
// character printable rejects is always within the first plane.
func appendHexEscape(buf *bytes.Buffer, r rune) {
	//: two digits.
	if r <= maxByteEscape {
		buf.WriteString(`\x`)
		appendHexDigits(buf, r, hexByteDigits)
		return
	}
	buf.WriteString(`\u`)
	appendHexDigits(buf, r, hexBMPDigits)
}

// appendHexDigits writes the low digits hexadecimal digits of r.
func appendHexDigits(buf *bytes.Buffer, r rune, digits int) {
	//: the most significant digit first.
	for i := digits - 1; i >= 0; i-- {
		buf.WriteByte(hexDigitsUpper[(r>>(nibbleBits*i))&nibbleMask])
	}
}

// appendFloat writes f so that every reader reads it back as a float: .inf,
// -.inf and .nan for the special values, and a decimal that always carries a
// dot — 1.0 rather than 1, 1.0e+21 rather than 1e+21 — since YAML 1.1 needs
// the dot and the core schema reads a dotless decimal as an integer.
func appendFloat(dst []byte, f float64, bits int) []byte {
	//: the special values.
	if special, ok := specialFloatText(f); ok {
		//: their spelling.
		return append(dst, special...)
	}
	start := len(dst)
	dst = strconv.AppendFloat(dst, f, 'g', -1, bits)
	text := dst[start:]
	//: a dot already.
	if bytes.IndexByte(text, '.') >= 0 {
		//: as formatted.
		return dst
	}
	exponent := bytes.IndexAny(text, "eE")
	//: no exponent: a whole number, given its ".0".
	if exponent < 0 {
		//: 1 becomes 1.0.
		return append(dst, ".0"...)
	}
	//: 1e+21 becomes 1.0e+21.
	at := start + exponent
	dst = append(dst, ".0"...)
	copy(dst[at+len(".0"):], dst[at:len(dst)-len(".0")])
	copy(dst[at:], ".0")
	//: with its dot.
	return dst
}

// specialFloatText returns the core schema's spelling of an infinity or NaN.
func specialFloatText(f float64) (string, bool) {
	switch {
	//: positive infinity.
	case math.IsInf(f, 1):
		//: .inf.
		return ".inf", true
	//: negative infinity.
	case math.IsInf(f, -1):
		//: -.inf.
		return "-.inf", true
	//: not a number.
	case math.IsNaN(f):
		//: .nan.
		return ".nan", true
	//: a finite float.
	default:
		//: not special.
		return "", false
	}
}
