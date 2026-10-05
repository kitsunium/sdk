package toml

import (
	"math"
	"strconv"
)

// Bits per digit of the integers with a radix prefix.
const (
	// hexBits is one hexadecimal digit.
	hexBits uint = 4
	// octalBits is one octal digit.
	octalBits uint = 3
	// binaryBits is one binary digit.
	binaryBits uint = 1
)

// decimalBase is the base of a decimal integer.
const decimalBase byte = 10

// floatBufferBytes sizes the stack buffer a float's digits are copied into
// without their underscores; a longer float takes the heap.
const floatBufferBytes int = 64

// The bounds of Clinger's fast path: a significand of at most 53 bits scaled
// by a power of ten that float64 holds exactly gives the correctly rounded
// result in one multiplication or division.
const (
	// maxExactSignificand is 2^53.
	maxExactSignificand uint64 = 1 << 53
	// maxExactPow10 is the largest power of ten float64 holds exactly.
	maxExactPow10 int = 22
	// maxSignificandDigits keeps the significand from overflowing uint64.
	maxSignificandDigits int = 19
	// maxExponentDigits bounds an exponent before it is handed to strconv.
	maxExponentDigits int = 4
)

// exactPow10 holds the powers of ten float64 represents exactly.
var exactPow10 = [...]float64{
	1e0, 1e1, 1e2, 1e3, 1e4, 1e5, 1e6, 1e7, 1e8, 1e9, 1e10, 1e11,
	1e12, 1e13, 1e14, 1e15, 1e16, 1e17, 1e18, 1e19, 1e20, 1e21, 1e22,
}

// numberValue reads an integer or a float.
func (p *parser) numberValue(parent int32, part keyPart) error {
	at := p.pos
	k, bits, err := p.number()
	//: a refusal inside the number.
	if err != nil {
		//: refused.
		return err
	}
	n := leaf(k, part, at)
	n.num = bits
	n.text = span{start: int32(at), end: int32(p.pos)}
	p.add(parent, n)
	//: the number.
	return nil
}

// number reads a number from p.pos and returns its kind and its value's bits.
func (p *parser) number() (k kind, bits uint64, err error) {
	start := p.pos
	signed := p.data[p.pos] == '+' || p.data[p.pos] == '-'
	//: an optional sign.
	if signed {
		p.pos++
	}
	//: a sign and nothing after it.
	if p.pos >= len(p.data) {
		//: refused.
		return 0, 0, p.fail(start, problemBadNumber)
	}
	c := p.data[p.pos]
	switch {
	//: inf and nan, signed or not.
	case c == 'i' || c == 'n':
		return p.specialFloat(start)
	//: 0x, 0o and 0b take no sign.
	case !signed && p.hasRadixPrefix():
		return p.radixInteger()
	//: a decimal integer or a float.
	default:
		return p.decimal(start)
	}
}

// hasRadixPrefix reports whether 0x, 0o or 0b starts at p.pos.
func (p *parser) hasRadixPrefix() bool {
	//: a zero, then a radix mark.
	return p.data[p.pos] == '0' && p.pos+1 < len(p.data) && radixBits(p.data[p.pos+1]) != 0
}

// specialFloat reads inf or nan, after an optional sign starting at start.
func (p *parser) specialFloat(start int) (kind, uint64, error) {
	negative := p.data[start] == '-'
	switch {
	//: an infinity takes its sign.
	case p.literal(wordInf):
		//: ±Inf.
		return kindFloat, math.Float64bits(math.Inf(signOf(negative))), nil
	//: a NaN's sign is not a value.
	case p.literal(wordNaN):
		//: NaN.
		return kindFloat, math.Float64bits(math.NaN()), nil
	//: any other word.
	default:
		return 0, 0, p.fail(start, problemBadNumber)
	}
}

// signOf returns -1 for a negative sign and 1 otherwise.
func signOf(negative bool) int {
	//: math.Inf takes the sign as an int.
	if negative {
		//: negative.
		return -1
	}
	//: positive.
	return 1
}

// radixBits returns the bits per digit the radix mark c announces, or 0 when c
// is no radix mark. The marks are lowercase only (§Integer).
func radixBits(c byte) uint {
	switch c {
	//: 0x.
	case 'x':
		return hexBits
	//: 0o.
	case 'o':
		return octalBits
	//: 0b.
	case 'b':
		return binaryBits
	//: no radix.
	default:
		return 0
	}
}

// radixClass returns the byte class of the digits of the radix whose digits
// carry bits bits.
func radixClass(bits uint) uint16 {
	switch bits {
	//: hexadecimal.
	case hexBits:
		return classHex
	//: octal.
	case octalBits:
		return classOctal
	//: binary.
	default:
		return classBinary
	}
}

// radixInteger reads a hexadecimal, octal or binary integer. Its value must
// fit a signed 64-bit integer: TOML's integers are signed, and the spelling
// carries no sign (§Integer).
func (p *parser) radixInteger() (kind, uint64, error) {
	at := p.pos
	bits := radixBits(p.data[p.pos+1])
	p.pos += 2
	digitsStart := p.pos
	//: at least one digit, underscores only between digits.
	if err := p.digits(radixClass(bits)); err != nil {
		//: refused.
		return 0, 0, err
	}
	var value uint64
	//: each digit, most significant first.
	for _, c := range p.data[digitsStart:p.pos] {
		//: underscores separate digits and carry no value.
		if c == '_' {
			continue
		}
		//: one more digit would pass the largest int64.
		if value > math.MaxInt64>>bits {
			//: refused.
			return 0, 0, p.fail(at, problemIntegerRange)
		}
		value = value<<bits | uint64(hexValue(c))
	}
	//: the integer.
	return kindInteger, value, nil
}

// digits advances over a non-empty run of digits of class cls in which every
// underscore stands between two digits.
func (p *parser) digits(cls uint16) error {
	//: a run starts with a digit.
	if p.pos >= len(p.data) || byteClass[p.data[p.pos]]&cls == 0 {
		//: refused.
		return p.fail(p.pos, problemBadNumber)
	}
	p.pos++
	//: digits, or an underscore and a digit.
	for p.pos < len(p.data) {
		c := p.data[p.pos]
		//: a digit.
		if byteClass[c]&cls != 0 {
			p.pos++
			continue
		}
		//: anything but an underscore ends the run.
		if c != '_' {
			//: done.
			return nil
		}
		//: an underscore must be followed by a digit.
		if p.pos+1 >= len(p.data) || byteClass[p.data[p.pos+1]]&cls == 0 {
			//: refused.
			return p.fail(p.pos, problemBadUnderscore)
		}
		p.pos += 2
	}
	//: the run reached the end of the document.
	return nil
}

// decimal reads a decimal integer or a float whose sign, if any, started at
// start.
func (p *parser) decimal(start int) (kind, uint64, error) {
	intStart := p.pos
	//: the integer part.
	if err := p.digits(classDigit); err != nil {
		//: refused.
		return 0, 0, err
	}
	//: a leading zero is refused, except for zero itself.
	if p.data[intStart] == '0' && p.pos-intStart > 1 {
		//: refused.
		return 0, 0, p.fail(intStart, problemLeadingZero)
	}
	isFloat, err := p.fraction()
	//: a malformed fraction or exponent.
	if err != nil {
		//: refused.
		return 0, 0, err
	}
	token := p.data[start:p.pos]
	//: a float.
	if isFloat {
		//: converted.
		return p.floatOf(start, token)
	}
	//: an integer.
	return p.integerOf(start, token)
}

// fraction reads the optional fractional part and exponent of a float, and
// reports whether there was either.
func (p *parser) fraction() (isFloat bool, err error) {
	//: a dot is followed by at least one digit.
	if p.consume(charDot) {
		//: the fractional digits.
		if err := p.digits(classDigit); err != nil {
			//: refused.
			return false, err
		}
		isFloat = true
	}
	//: no exponent.
	if p.pos >= len(p.data) || (p.data[p.pos] != 'e' && p.data[p.pos] != 'E') {
		//: a float with a fraction, or an integer.
		return isFloat, nil
	}
	p.pos++
	//: the exponent's optional sign.
	if p.pos < len(p.data) && (p.data[p.pos] == '+' || p.data[p.pos] == '-') {
		p.pos++
	}
	//: the exponent's digits; a leading zero is allowed there.
	return true, p.digits(classDigit)
}

// integerOf converts a decimal integer token, refusing one past the range of
// a signed 64-bit integer (§Integer: "an error must be thrown").
func (p *parser) integerOf(at int, token []byte) (kind, uint64, error) {
	negative := token[0] == '-'
	var limit uint64 = math.MaxInt64
	//: the negative range is one larger.
	if negative {
		limit++
	}
	var value uint64
	//: each digit; signs and underscores carry no value.
	for _, c := range token {
		//: not a digit.
		if byteClass[c]&classDigit == 0 {
			continue
		}
		digit := uint64(c - '0')
		//: one more digit would pass the limit.
		if value > (limit-digit)/uint64(decimalBase) {
			//: refused.
			return 0, 0, p.fail(at, problemIntegerRange)
		}
		value = value*uint64(decimalBase) + digit
	}
	//: the negative of a magnitude up to 2^63 is an int64.
	if negative {
		value = -value
	}
	//: the integer's bits.
	return kindInteger, value, nil
}

// floatOf converts a float token whose grammar has been checked. A value too
// large for float64 is refused; one too small rounds to zero, as IEEE 754
// does.
func (p *parser) floatOf(at int, token []byte) (kind, uint64, error) {
	var buf [floatBufferBytes]byte
	clean := buf[:0]
	//: underscores carry no value.
	for _, c := range token {
		//: kept.
		if c != '_' {
			clean = append(clean, c)
		}
	}
	//: most floats a person writes are exact in one operation.
	if f, ok := fastFloat(clean); ok {
		//: the float.
		return kindFloat, math.Float64bits(f), nil
	}
	f, err := strconv.ParseFloat(string(clean), 64)
	//: out of range: ±Inf is not what the document wrote.
	if err != nil {
		//: refused.
		return 0, 0, p.fail(at, problemFloatRange)
	}
	//: the float.
	return kindFloat, math.Float64bits(f), nil
}

// fastFloat converts a decimal float with Clinger's exact method when it
// applies: a significand of at most 53 bits and a power of ten float64 holds
// exactly. It reports false otherwise, and the caller defers to strconv.
func fastFloat(b []byte) (float64, bool) {
	negative := b[0] == '-'
	//: the sign is not a digit.
	if b[0] == '-' || b[0] == '+' {
		b = b[1:]
	}
	significand, exponent, rest, ok := significandOf(b)
	//: too many digits, or more than a significand.
	if !ok {
		//: deferred.
		return 0, false
	}
	exp, ok := exponentOf(rest)
	exponent += exp
	//: outside the exact range.
	if !ok || significand > maxExactSignificand || exponent < -maxExactPow10 || exponent > maxExactPow10 {
		//: deferred.
		return 0, false
	}
	f := float64(significand)
	//: one exact operation.
	if exponent < 0 {
		f /= exactPow10[-exponent]
	} else {
		f *= exactPow10[exponent]
	}
	//: the sign last, so -0.0 keeps its sign.
	if negative {
		f = -f
	}
	//: the correctly rounded value.
	return f, true
}

// significandOf reads the digits and the fractional digits of a float into a
// significand, and returns the power of ten the fraction implies and what
// follows.
func significandOf(b []byte) (significand uint64, exponent int, rest []byte, ok bool) {
	digits := 0
	afterDot := false
	i := 0
	//: digits and one dot.
	for ; i < len(b); i++ {
		c := b[i]
		//: the dot only moves the exponent.
		if c == '.' {
			afterDot = true
			continue
		}
		//: the exponent starts.
		if c == 'e' || c == 'E' {
			break
		}
		//: too many digits for the fast path.
		if digits == maxSignificandDigits {
			//: deferred.
			return 0, 0, nil, false
		}
		significand = significand*uint64(decimalBase) + uint64(c-'0')
		digits++
		//: each fractional digit divides by ten.
		if afterDot {
			exponent--
		}
	}
	//: the significand and what follows it.
	return significand, exponent, b[i:], true
}

// exponentOf reads an exponent, "e" or "E" then a signed integer, or nothing.
func exponentOf(b []byte) (int, bool) {
	//: no exponent.
	if len(b) == 0 {
		//: ten to the zero.
		return 0, true
	}
	b = b[1:]
	sign := 1
	//: an optional sign.
	if b[0] == '+' || b[0] == '-' {
		//: negative.
		if b[0] == '-' {
			sign = -1
		}
		b = b[1:]
	}
	//: a long exponent is strconv's to judge.
	if len(b) > maxExponentDigits {
		//: deferred.
		return 0, false
	}
	value := 0
	//: the exponent's digits.
	for _, c := range b {
		value = value*int(decimalBase) + int(c-'0')
	}
	//: the signed exponent.
	return sign * value, true
}
