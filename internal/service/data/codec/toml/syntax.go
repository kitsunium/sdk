// Package toml — the lexical tables the parser reads bytes through, and the
// fixed sentences a refusal is made of.
package toml

// Byte classes, one bit each: a byte may belong to several.
const (
	// classPrintable is the tab and printable ASCII, 0x20 to 0x7E: what a
	// string keeps verbatim, once its delimiter and escape are handled.
	classPrintable uint16 = 1 << iota
	// classBareKey is A-Z a-z 0-9 - _ (§Keys).
	classBareKey
	// classDigit is 0-9.
	classDigit
	// classHex is 0-9 a-f A-F.
	classHex
	// classOctal is 0-7.
	classOctal
	// classBinary is 0 and 1.
	classBinary
	// classNumberStart is what a number, a date or a time starts with:
	// a digit, a sign, or the i and n of inf and nan.
	classNumberStart
)

// The words a value may be.
const (
	// wordTrue is the true boolean.
	wordTrue string = "true"
	// wordFalse is the false boolean.
	wordFalse string = "false"
	// wordInf is an infinite float.
	wordInf string = "inf"
	// wordNaN is a float that is not a number.
	wordNaN string = "nan"
)

// escapeCharacter is the value of TOML v1.1.0's \e.
const escapeCharacter byte = 0x1b

// byteValues is how many values a byte has: the size of every table indexed
// by one.
const byteValues int = 256

// The fields a refusal carries.
const (
	// fieldProblem names what is wrong, in a fixed sentence.
	fieldProblem string = "problem"
	// fieldLine is the 1-based line of the problem.
	fieldLine string = "line"
	// fieldColumn is the 1-based column of the problem, in characters.
	fieldColumn string = "column"
	// fieldKey is the dotted key of the value a target could not hold.
	fieldKey string = "key"
	// fieldType is the Go type that could not hold a value, or that has no
	// TOML representation.
	fieldType string = "type"
	// fieldTOML is the kind of TOML value a target could not hold.
	fieldTOML string = "toml"
	// fieldLimit is the bound a document or a value went past.
	fieldLimit string = "limit"
)

// The problems a parse refuses a document for. Each is a fixed sentence: a
// refusal never carries a byte of the document, whose values may be secrets.
const (
	// problemExpectedNewline: something follows a complete expression.
	problemExpectedNewline string = "expected a newline or a comment after the expression"
	// problemExpectedKey: a key is required here.
	problemExpectedKey string = "expected a key"
	// problemExpectedEquals: a key with no equals sign after it.
	problemExpectedEquals string = "expected '=' after the key"
	// problemExpectedValue: nothing that starts a value.
	problemExpectedValue string = "expected a value"
	// problemExpectedComma: an array element followed by neither ',' nor ']'.
	problemExpectedComma string = "expected ',' or ']' after an array element"
	// problemExpectedPairComma: an inline-table pair followed by neither ','
	// nor '}'.
	problemExpectedPairComma string = "expected ',' or '}' after an inline table key-value"
	// problemUnclosedHeader: a table header that does not close.
	problemUnclosedHeader string = "expected ']' to close the table header"
	// problemKeyTwice: a key defined a second time.
	problemKeyTwice string = "the key is already defined"
	// problemTableTwice: a [header] repeated.
	problemTableTwice string = "the table is already defined"
	// problemTableDotted: a [header] for a table dotted keys defined.
	problemTableDotted string = "the table was defined by dotted keys and cannot be defined again"
	// problemTableIsArray: a [header] for an array of tables.
	problemTableIsArray string = "the table is already an array of tables"
	// problemNotArrayOfTables: a [[header]] for something else.
	problemNotArrayOfTables string = "the key is already defined and is not an array of tables"
	// problemExtendValue: a header passing through a value.
	problemExtendValue string = "the key is a value, a static array or an inline table, and cannot be extended"
	// problemDottedExtends: a dotted key reaching into a table defined
	// elsewhere.
	problemDottedExtends string = "a dotted key cannot extend a table defined by a header, a value or an inline table"
	// problemControlInComment: a control character in a comment.
	problemControlInComment string = "a comment contains a control character"
	// problemControlInString: a newline or a control character in a string
	// that may not hold it.
	problemControlInString string = "a string contains a control character or a newline it may not hold"
	// problemInvalidUTF8: bytes that are not UTF-8.
	problemInvalidUTF8 string = "the document is not valid UTF-8"
	// problemUnterminatedString: the document ends inside a string.
	problemUnterminatedString string = "the string is not terminated"
	// problemBadEscape: an unknown escape, or one naming no Unicode scalar
	// value.
	problemBadEscape string = "invalid escape sequence"
	// problemTooManyQuotes: more than five quotes closing a multi-line string.
	problemTooManyQuotes string = "too many quotes at the end of a multi-line string"
	// problemBadNumber: a malformed integer or float.
	problemBadNumber string = "malformed number"
	// problemBadUnderscore: an underscore not between two digits.
	problemBadUnderscore string = "an underscore in a number must stand between two digits"
	// problemLeadingZero: a decimal number with a leading zero.
	problemLeadingZero string = "a decimal number cannot have a leading zero"
	// problemIntegerRange: an integer outside int64.
	problemIntegerRange string = "the integer does not fit a signed 64-bit integer"
	// problemFloatRange: a float too large for float64.
	problemFloatRange string = "the float is too large for a 64-bit float"
	// problemBadDate: a malformed date or a day that does not exist.
	problemBadDate string = "malformed or impossible date"
	// problemBadTime: a malformed time or an impossible hour, minute or
	// second.
	problemBadTime string = "malformed or impossible time"
	// problemBadOffset: a malformed time-zone offset.
	problemBadOffset string = "malformed time-zone offset"
	// problemTooDeep: tables and arrays nested past maxDepth.
	problemTooDeep string = "tables and arrays are nested too deep"
	// problemTooLarge: a document past maxDocumentBytes.
	problemTooLarge string = "the document is larger than the limit"
)

// The lexical tables.
var (
	// byteClass maps every byte to its classes.
	byteClass = buildByteClass()

	// simpleEscapes maps the character after a backslash to the byte it
	// stands for, or to 0 when it starts no one-character escape (§String).
	// \e is TOML v1.1.0's, accepted because the previous library accepted it.
	simpleEscapes = [byteValues]byte{
		'b':  '\b',
		't':  '\t',
		'n':  '\n',
		'f':  '\f',
		'r':  '\r',
		'"':  '"',
		'\\': '\\',
		'e':  escapeCharacter,
	}
)

// buildByteClass computes byteClass.
func buildByteClass() [byteValues]uint16 {
	var table [byteValues]uint16
	table[charTab] |= classPrintable
	//: printable ASCII.
	for c := charFirstPrintable; c < charDelete; c++ {
		table[c] |= classPrintable
	}
	markRange(&table, '0', '9', classBareKey|classDigit|classHex|classNumberStart)
	markRange(&table, 'a', 'z', classBareKey)
	markRange(&table, 'A', 'Z', classBareKey)
	markRange(&table, 'a', 'f', classHex)
	markRange(&table, 'A', 'F', classHex)
	markRange(&table, '0', '7', classOctal)
	markRange(&table, '0', '1', classBinary)
	table['-'] |= classBareKey | classNumberStart
	table['_'] |= classBareKey
	table['+'] |= classNumberStart
	table['i'] |= classNumberStart
	table['n'] |= classNumberStart
	//: the table.
	return table
}

// markRange adds cls to every byte from first to last.
func markRange(table *[byteValues]uint16, first, last byte, cls uint16) {
	//: inclusive on both ends.
	for c := first; c <= last; c++ {
		table[c] |= cls
	}
}
