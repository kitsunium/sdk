// Package token — JSON string rendering that cannot fail.
//
// encoding/json's Marshal returns an error no string or []string can ever
// produce, and discarding it at a dozen call sites is how a real failure
// somewhere else eventually gets discarded too. Rendering the two shapes this
// package actually emits by hand removes the error channel instead of ignoring
// it — and removes a reflective encode from every mint.
package token

import "strings"

const (
	// firstPrintable is the lowest byte JSON may carry unescaped in a string;
	// everything below it needs the \u00XX form (RFC 8259 §7).
	firstPrintable byte = 0x20
	// lowNibbleMask selects the low four bits of a byte.
	lowNibbleMask byte = 0x0F
	// nibbleBits is how far to shift a byte to reach its high nibble.
	nibbleBits byte = 4
	// decimalDigits is the number of decimal digits, i.e. where hex letters
	// start when rendering a nibble.
	decimalDigits byte = 10
	// quoteOverhead is the two delimiting quotes a JSON string literal adds
	// around its content.
	quoteOverhead int = 2
)

// quoteJSONString renders s as a JSON string literal.
//
// Every value it renders today comes from the SDK's own configuration or from
// a claim this package already decoded — never straight off the wire. It
// escapes anyway, because a string concatenated into JSON is exactly the shape
// that stops being safe the day somebody makes one of its inputs dynamic.
func quoteJSONString(s string) string {
	var builder strings.Builder
	//: two quotes plus, in the common case, the string itself.
	builder.Grow(len(s) + quoteOverhead)
	builder.WriteByte('"')
	//: escape the two structural characters plus anything below space.
	for i := range len(s) {
		writeJSONStringByte(&builder, s[i])
	}
	builder.WriteByte('"')
	//: a valid JSON string literal.
	return builder.String()
}

// quoteJSONStrings renders values as a JSON array of string literals.
func quoteJSONStrings(values []string) string {
	var builder strings.Builder
	builder.WriteByte('[')
	//: comma-separated, in the caller's order.
	for i, value := range values {
		//: every element but the first needs its separator.
		if i > 0 {
			builder.WriteByte(',')
		}
		builder.WriteString(quoteJSONString(value))
	}
	builder.WriteByte(']')
	//: a valid JSON array literal.
	return builder.String()
}

// writeJSONStringByte appends one byte of string content in escaped form.
func writeJSONStringByte(builder *strings.Builder, char byte) {
	//: three cases: the two structural characters, control bytes, everything
	//: else. Multi-byte UTF-8 passes through untouched, which is correct —
	//: JSON strings are UTF-8 and its continuation bytes are all >= 0x80.
	switch {
	//: a quote or a backslash is escaped with a backslash.
	case char == '"' || char == '\\':
		builder.WriteByte('\\')
		builder.WriteByte(char)
	//: a control byte needs the six-character \u00XX form.
	case char < firstPrintable:
		builder.WriteString(`\u00`)
		builder.WriteByte(hexDigit(char >> nibbleBits))
		builder.WriteByte(hexDigit(char & lowNibbleMask))
	//: anything else is its own encoding.
	default:
		builder.WriteByte(char)
	}
}

// hexDigit renders one nibble as a lowercase hex digit.
func hexDigit(nibble byte) byte {
	//: 0-9 then a-f.
	if nibble < decimalDigits {
		//: decimal digit.
		return '0' + nibble
	}
	//: lowercase hex letter.
	return 'a' + nibble - decimalDigits
}
