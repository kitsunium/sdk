package yaml

import (
	"errors"
	"math"
	"strconv"
)

// The bases of the core schema's prefixed integers, and the value digitValue
// gives a character that is a digit of none.
const (
	// octalBase is the base of a 0o integer.
	octalBase int = 8
	// hexBase is the base of a 0x integer.
	hexBase int = 16
	// notADigit is digitValue's answer for a character that is no digit: it
	// is past every base used here.
	notADigit int = 16
)

// The core schema's types, and the two readings the subset refuses.
const (
	// resolvedString is everything the other kinds do not match.
	resolvedString resolvedKind = iota
	// resolvedNull is ~, null, Null, NULL, or nothing.
	resolvedNull
	// resolvedBool is true or false, in three cases each — never yes/no/on/off.
	resolvedBool
	// resolvedInt is a decimal, 0o octal or 0x hexadecimal integer that fits
	// an int64.
	resolvedInt
	// resolvedUint is an integer past math.MaxInt64 that fits a uint64.
	resolvedUint
	// resolvedFloat is a decimal float, .inf or .nan.
	resolvedFloat
	// resolvedLeadingZero is a decimal integer with a leading zero, which
	// YAML 1.1 reads as octal and YAML 1.2 as decimal.
	resolvedLeadingZero
	// resolvedOutOfRange is an integer or a float no 64-bit Go value holds.
	resolvedOutOfRange
)

// resolvedKind is what the core schema reads a plain scalar as.
type resolvedKind uint8

// scalarValue is a plain scalar's reading by the core schema.
type scalarValue struct {
	// f is a float's value.
	f float64
	// i is an integer's value when it fits an int64.
	i int64
	// u is an integer's value when it fits only a uint64.
	u uint64
	// kind is the reading.
	kind resolvedKind
	// b is a boolean's value.
	b bool
}

// resolvePlain reads a plain scalar's text by the YAML 1.2 core schema
// (YAML 1.2.2 §10.3.2).
func resolvePlain(text string) scalarValue {
	//: the empty node is null.
	if text == "" {
		//: null.
		return scalarValue{kind: resolvedNull}
	}
	switch text[0] {
	//: ~, null, Null, NULL.
	case '~', 'n', 'N':
		//: one of the spellings, or a string.
		if isNullText(text) {
			//: null.
			return scalarValue{kind: resolvedNull}
		}
	//: true and false, in three cases.
	case 't', 'T', 'f', 'F':
		switch text {
		//: true.
		case "true", "True", "TRUE":
			//: a boolean.
			return scalarValue{kind: resolvedBool, b: true}
		//: false.
		case "false", "False", "FALSE":
			//: a boolean.
			return scalarValue{kind: resolvedBool}
		}
	//: a number, possibly.
	case '-', '+', '.', '0', '1', '2', '3', '4', '5', '6', '7', '8', '9':
		//: an integer or a float, or a string after all.
		return resolveNumber(text)
	}
	//: a string.
	return scalarValue{kind: resolvedString}
}

// resolveNumber reads text, which starts like a number, as an integer, a
// float, or — when it matches neither grammar — a string.
func resolveNumber(text string) scalarValue {
	//: .inf, -.inf, .nan in their three cases.
	if f, ok := specialFloat(text); ok {
		//: a float.
		return scalarValue{kind: resolvedFloat, f: f}
	}
	//: 0o or 0x followed by digits of that base.
	if digits, base, ok := prefixedInteger(text); ok {
		//: an integer, or a string.
		return resolveBase(digits, base)
	}
	//: a decimal integer.
	if isDecimalInteger(text) {
		//: its value, or its refusal.
		return resolveDecimal(text)
	}
	//: a decimal float.
	if isDecimalFloat(text) {
		//: its value, or out of range.
		return resolveFloat(text)
	}
	//: a string that only looked like a number.
	return scalarValue{kind: resolvedString}
}

// specialFloat reads the core schema's spellings of infinity and NaN.
func specialFloat(text string) (float64, bool) {
	switch text {
	//: positive infinity.
	case ".inf", ".Inf", ".INF", "+.inf", "+.Inf", "+.INF":
		//: +Inf.
		return math.Inf(1), true
	//: negative infinity.
	case "-.inf", "-.Inf", "-.INF":
		//: -Inf.
		return math.Inf(-1), true
	//: not a number.
	case ".nan", ".NaN", ".NAN":
		//: NaN.
		return math.NaN(), true
	//: anything else.
	default:
		//: not special.
		return 0, false
	}
}

// prefixedInteger splits a 0o or 0x integer into its digits and its base.
func prefixedInteger(text string) (digits string, base int, ok bool) {
	//: a prefix and at least one digit.
	if len(text) <= len("0x") || text[0] != '0' {
		//: not prefixed.
		return "", 0, false
	}
	switch text[1] {
	//: octal.
	case 'o':
		//: base 8.
		return text[2:], octalBase, true
	//: hexadecimal.
	case 'x':
		//: base 16.
		return text[2:], hexBase, true
	//: anything else.
	default:
		//: not prefixed.
		return "", 0, false
	}
}

// resolveFloat reads a decimal float, refusing one past float64.
func resolveFloat(text string) scalarValue {
	f, err := strconv.ParseFloat(text, 64)
	//: past float64.
	if errors.Is(err, strconv.ErrRange) && math.IsInf(f, 0) {
		//: no Go float holds it.
		return scalarValue{kind: resolvedOutOfRange}
	}
	//: a float.
	return scalarValue{kind: resolvedFloat, f: f}
}

// resolveBase reads digits as an unsigned integer in base, or a string when
// they are not all digits of that base.
func resolveBase(digits string, base int) scalarValue {
	//: every character a digit of the base.
	for i := range len(digits) {
		//: a character outside the base: a string.
		if digitValue(digits[i]) >= base {
			//: not a number.
			return scalarValue{kind: resolvedString}
		}
	}
	u, err := strconv.ParseUint(digits, base, 64)
	//: past uint64.
	if err != nil {
		//: no Go integer holds it.
		return scalarValue{kind: resolvedOutOfRange}
	}
	//: an int64 when it fits.
	if u <= math.MaxInt64 {
		//: signed.
		return scalarValue{kind: resolvedInt, i: int64(u)}
	}
	//: only a uint64 holds it.
	return scalarValue{kind: resolvedUint, u: u}
}

// resolveDecimal reads a decimal integer: refused when a leading zero makes
// YAML 1.1 read it as octal, out of range past 64 bits.
func resolveDecimal(text string) scalarValue {
	digits := text
	//: the sign is not a digit.
	if text[0] == '-' || text[0] == '+' {
		digits = text[1:]
	}
	//: 0644: octal to YAML 1.1, decimal to YAML 1.2.
	if len(digits) > 1 && digits[0] == '0' {
		//: ambiguous.
		return scalarValue{kind: resolvedLeadingZero}
	}
	i, err := strconv.ParseInt(text, 10, 64)
	//: an int64.
	if err == nil {
		//: signed.
		return scalarValue{kind: resolvedInt, i: i}
	}
	//: a positive integer past int64 may still fit a uint64.
	if text[0] != '-' {
		u, uerr := strconv.ParseUint(digits, 10, 64)
		//: it does.
		if uerr == nil {
			//: unsigned.
			return scalarValue{kind: resolvedUint, u: u}
		}
	}
	//: no Go integer holds it.
	return scalarValue{kind: resolvedOutOfRange}
}

// digitValue returns the value of the hexadecimal digit c, or 16 when c is
// not one.
func digitValue(c byte) int {
	switch {
	//: 0-9.
	case c >= '0' && c <= '9':
		//: its value.
		return int(c - '0')
	//: a-f.
	case c >= 'a' && c <= 'f':
		//: its value.
		return int(c-'a') + 10
	//: A-F.
	case c >= 'A' && c <= 'F':
		//: its value.
		return int(c-'A') + 10
	//: not a digit.
	default:
		//: past every base used here.
		return notADigit
	}
}

// isDigit reports whether c is a decimal digit.
func isDigit(c byte) bool {
	//: 0-9.
	return c >= '0' && c <= '9'
}

// isDecimalInteger reports whether text matches [-+]?[0-9]+.
func isDecimalInteger(text string) bool {
	i := 0
	//: an optional sign.
	if text[0] == '-' || text[0] == '+' {
		i++
	}
	//: at least one digit.
	if i == len(text) {
		//: a sign alone.
		return false
	}
	//: only digits.
	for ; i < len(text); i++ {
		//: anything else.
		if !isDigit(text[i]) {
			//: not an integer.
			return false
		}
	}
	//: an integer.
	return true
}

// isDecimalFloat reports whether text matches the core schema's float:
// [-+]?(\.[0-9]+|[0-9]+(\.[0-9]*)?)([eE][-+]?[0-9]+)? with a dot or an exponent.
func isDecimalFloat(text string) bool {
	i := skipSign(text, 0)
	whole := countDigits(text, i)
	i += whole
	fraction, dot := 0, false
	//: a fraction.
	if i < len(text) && text[i] == '.' {
		dot = true
		fraction = countDigits(text, i+1)
		i += 1 + fraction
	}
	//: digits on one side of the dot at least.
	if whole+fraction == 0 {
		//: not a number.
		return false
	}
	end, exponent, ok := scanExponent(text, i)
	//: the whole text, with a dot or an exponent.
	return ok && end == len(text) && (dot || exponent)
}

// skipSign returns i past an optional sign at i.
func skipSign(text string, i int) int {
	//: a sign.
	if i < len(text) && (text[i] == '-' || text[i] == '+') {
		//: past it.
		return i + 1
	}
	//: no sign.
	return i
}

// scanExponent reads an optional exponent at i: it returns the offset past it,
// whether there was one, and false when an "e" has no digits.
func scanExponent(text string, i int) (end int, present, ok bool) {
	//: no exponent.
	if i >= len(text) || (text[i] != 'e' && text[i] != 'E') {
		//: nothing read.
		return i, false, true
	}
	i = skipSign(text, i+1)
	digits := countDigits(text, i)
	//: an exponent needs digits.
	return i + digits, true, digits > 0
}

// countDigits returns how many decimal digits text holds from i on.
func countDigits(text string, i int) int {
	n := 0
	//: consecutive digits.
	for i+n < len(text) && isDigit(text[i+n]) {
		n++
	}
	//: the run's length.
	return n
}

// isYAML11Bool reports whether text is one of the spellings YAML 1.1 reads as
// a boolean and YAML 1.2 as a string — the words the subset never takes for a
// boolean, and the encoder always quotes.
func isYAML11Bool(text string) bool {
	switch text {
	//: the YAML 1.1 booleans the core schema does not have.
	case "y", "Y", "yes", "Yes", "YES", "n", "N", "no", "No", "NO",
		"on", "On", "ON", "off", "Off", "OFF":
		//: yes.
		return true
	//: anything else.
	default:
		//: no.
		return false
	}
}

// readsAsNonString reports whether some YAML reader would take the plain
// scalar text for something other than a string: the core schema, YAML 1.1
// (booleans, underscores, base 2, sexagesimal), or a timestamp. The encoder
// quotes every such string, so what it writes reads back as text in every
// reader.
func readsAsNonString(text string) bool {
	//: the core schema — null, a boolean, a number — or a YAML 1.1 boolean.
	if resolvePlain(text).kind != resolvedString || isYAML11Bool(text) {
		//: yes.
		return true
	}
	//: a number in some dialect, or a timestamp.
	return looksNumeric(text) || looksLikeTimestamp(text)
}

// looksNumeric reports whether text starts like a number and holds nothing a
// number in any YAML dialect could not — digits, signs, a dot, underscores,
// colons (YAML 1.1's base 60), and the letters of 0x, 0o, 0b and exponents.
// Underscores are skipped where the number starts, as yaml.v3 drops every
// underscore before it parses a number: +_0 is 0 to it, +._5 is 0.5.
func looksNumeric(text string) bool {
	i := skipSign(text, 0)
	//: a digit, or a dot and a digit, underscores aside.
	if !startsLikeNumber(text, i) {
		//: does not start like a number.
		return false
	}
	//: every character could belong to a number.
	for ; i < len(text); i++ {
		//: a character no number holds.
		if !isNumberChar(text[i]) {
			//: a string.
			return false
		}
	}
	//: it may read as a number somewhere.
	return true
}

// startsLikeNumber reports whether text has a digit, or a dot and a digit, at
// i — once the underscores before each are skipped.
func startsLikeNumber(text string, i int) bool {
	i = skipUnderscores(text, i)
	//: nothing left.
	if i >= len(text) {
		//: no.
		return false
	}
	//: a digit.
	if isDigit(text[i]) {
		//: yes.
		return true
	}
	next := skipUnderscores(text, i+1)
	//: a dot before a digit.
	return text[i] == '.' && next < len(text) && isDigit(text[next])
}

// skipUnderscores returns i past every underscore at i.
func skipUnderscores(text string, i int) int {
	//: consecutive underscores.
	for i < len(text) && text[i] == '_' {
		i++
	}
	//: the first other character, or the end.
	return i
}

// isNumberChar reports whether c can appear in a number of some YAML dialect.
func isNumberChar(c byte) bool {
	//: a digit, or a hexadecimal letter.
	if digitValue(c) < notADigit {
		//: yes.
		return true
	}
	switch c {
	//: the rest of the alphabet numbers use.
	case '_', '.', ':', '+', '-', 'x', 'X', 'o', 'O':
		//: yes.
		return true
	//: anything else.
	default:
		//: no.
		return false
	}
}

// looksLikeTimestamp reports whether text starts like a YAML 1.1 timestamp:
// four digits and a dash.
func looksLikeTimestamp(text string) bool {
	//: YYYY- at least.
	return len(text) >= 5 && isDigit(text[0]) && isDigit(text[1]) && isDigit(text[2]) && isDigit(text[3]) && text[4] == '-'
}
