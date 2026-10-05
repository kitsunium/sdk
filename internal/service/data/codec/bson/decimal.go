package bson

import (
	"encoding/json"
	"math/bits"
	"strconv"
)

// Decimal128 layout and range.
const (
	// decimalExponentBias is added to the exponent to store it unsigned.
	decimalExponentBias int = 6176
	// decimalMaxExponent is the largest exponent a coefficient can carry.
	decimalMaxExponent int = 6111
	// decimalMinExponent is the smallest exponent a coefficient can carry.
	decimalMinExponent int = -6176
	// decimalMaxDigits is the most significant digits a coefficient holds.
	decimalMaxDigits int = 34
	// decimalExponentMask keeps the 14 exponent bits once shifted down.
	decimalExponentMask uint64 = 1<<14 - 1
	// decimalCoefficientHighMask keeps the coefficient's 49 high bits.
	decimalCoefficientHighMask uint64 = 1<<49 - 1
	// decimalMaxCoefficientHigh and decimalMaxCoefficientLow are 10^34-1, the
	// largest coefficient. A larger one is non-canonical and reads as zero.
	decimalMaxCoefficientHigh uint64 = 0x0001_ED09_BEAD_87C0
	// decimalMaxCoefficientLow is the low half of 10^34-1.
	decimalMaxCoefficientLow uint64 = 0x378D_8E63_FFFF_FFFF
	// decimalSignBit is the sign, the top bit of the high half.
	decimalSignBit uint64 = 1 << 63
	// decimalNaNBits and decimalInfBits are the combination-field patterns
	// that mark the two specials, in the high half's top bits after the sign.
	decimalNaNBits uint64 = 0x1F << 58
	// decimalInfBits marks an infinity.
	decimalInfBits uint64 = 0x1E << 58
	// decimalChunk is the power of ten the coefficient is printed nine digits
	// at a time by.
	decimalChunk uint64 = 1_000_000_000
	// decimalChunkDigits is how many digits decimalChunk carries.
	decimalChunkDigits int = 9
	// decimalScientificFloor is the smallest adjusted exponent printed without
	// an exponent, as the specification fixes it.
	decimalScientificFloor int = -6
	// decimalLargeForm is the value of the two bits after the sign that mark
	// the large-coefficient form.
	decimalLargeForm uint64 = 3
	// decimalRadix is the base the coefficient is written in.
	decimalRadix uint64 = 10
	// decimalExponentSaturation bounds a scanned exponent's magnitude: past it
	// the value is out of range whatever the coefficient, and saturating keeps
	// the arithmetic inside an int on every platform.
	decimalExponentSaturation int = 100_000_000
)

// newDecimal128 is NewDecimal128's body: decl_gen.go writes NewDecimal128, from the
// design, as one call of it.
func newDecimal128(high, low uint64) Decimal128 {
	//: the two halves verbatim.
	return Decimal128{h: high, l: low}
}

// getBytes is Decimal128.GetBytes's body: decl_gen.go writes Decimal128.GetBytes, from the
// design, as one call of it.
func (d Decimal128) getBytes() (high, low uint64) {
	//: the two halves verbatim.
	return d.h, d.l
}

// isNaN is Decimal128.IsNaN's body: decl_gen.go writes Decimal128.IsNaN, from the
// design, as one call of it.
func (d Decimal128) isNaN() bool {
	//: the five combination bits all set.
	return d.h&decimalNaNBits == decimalNaNBits
}

// IsInf returns +1 for +Infinity, -1 for -Infinity, and 0 otherwise.
func (d Decimal128) IsInf() int {
	//: the infinity pattern, which a NaN does not match.
	if d.h&decimalNaNBits != decimalInfBits {
		//: finite or NaN.
		return 0
	}
	//: the sign decides which.
	if d.h&decimalSignBit != 0 {
		//: negative.
		return -1
	}
	//: positive.
	return 1
}

// isZero is Decimal128.IsZero's body: decl_gen.go writes Decimal128.IsZero, from the
// design, as one call of it.
func (d Decimal128) isZero() bool {
	//: both halves zero.
	return d.h == 0 && d.l == 0
}

// String returns the specification's string form: "NaN", "Infinity",
// "-Infinity", plain notation when the exponent is not positive and the
// adjusted exponent is at least -6, scientific notation otherwise.
func (d Decimal128) String() string {
	//: the specials first.
	if d.IsNaN() {
		//: whatever the sign and payload.
		return "NaN"
	}
	//: infinities carry their sign.
	switch d.IsInf() {
	//: positive.
	case 1:
		return "Infinity"
	//: negative.
	case -1:
		return "-Infinity"
	}
	high, low, exponent := d.finite()
	var digits [40]byte
	//: render the coefficient, then place the point or the exponent.
	return string(formatDecimal(d.h&decimalSignBit != 0, coefficientDigits(&digits, high, low), exponent))
}

// finite splits a finite d into its coefficient and unbiased exponent. A
// coefficient over 10^34-1, and every coefficient of the form whose two bits
// after the sign are both set, is non-canonical and reads as zero.
func (d Decimal128) finite() (high, low uint64, exponent int) {
	//: the large form: exponent two bits lower, coefficient out of range.
	if d.h>>61&decimalLargeForm == decimalLargeForm {
		//: the coefficient is at least 2^113, past 10^34-1.
		return 0, 0, int(d.h>>47&decimalExponentMask) - decimalExponentBias
	}
	exponent = int(d.h>>49&decimalExponentMask) - decimalExponentBias
	high, low = d.h&decimalCoefficientHighMask, d.l
	//: a coefficient past 10^34-1 is non-canonical.
	if high > decimalMaxCoefficientHigh || (high == decimalMaxCoefficientHigh && low > decimalMaxCoefficientLow) {
		//: read as zero, the exponent kept.
		return 0, 0, exponent
	}
	//: canonical.
	return high, low, exponent
}

// coefficientDigits writes the decimal digits of the 113-bit coefficient
// high:low into buf and returns them without leading zeros: "0" for zero.
func coefficientDigits(buf *[40]byte, high, low uint64) []byte {
	i := len(buf)
	//: nine digits per division, least significant first.
	for high != 0 || low != 0 {
		var rem uint64
		//: the high half's quotient and remainder first; the remainder is below
		//: decimalChunk, which is what Div64 requires of its first argument.
		high, rem = bits.Div64(0, high, decimalChunk)
		low, rem = bits.Div64(rem, low, decimalChunk)
		//: the chunk's nine digits, zero-padded.
		for range decimalChunkDigits {
			i--
			buf[i] = byte('0' + rem%decimalRadix)
			rem /= decimalRadix
		}
	}
	//: drop the padding the last chunk wrote.
	for i < len(buf)-1 && buf[i] == '0' {
		i++
	}
	//: a zero coefficient wrote nothing.
	if i == len(buf) {
		//: one zero digit.
		i--
		buf[i] = '0'
	}
	//: the significant digits.
	return buf[i:]
}

// formatDecimal places the decimal point or the exponent in digits.
func formatDecimal(negative bool, digits []byte, exponent int) []byte {
	out := make([]byte, 0, len(digits)+16)
	//: the sign, a negative zero included.
	if negative {
		out = append(out, '-')
	}
	adjusted := exponent + len(digits) - 1
	//: plain notation.
	if exponent <= 0 && adjusted >= decimalScientificFloor {
		//: the point, if any, falls inside or before the digits.
		return appendPlain(out, digits, exponent)
	}
	out = append(out, digits[0])
	//: the fraction after the first digit.
	if len(digits) > 1 {
		out = append(out, '.')
		out = append(out, digits[1:]...)
	}
	out = append(out, 'E')
	//: an explicit plus; AppendInt writes the minus.
	if adjusted >= 0 {
		out = append(out, '+')
	}
	//: the adjusted exponent.
	return strconv.AppendInt(out, int64(adjusted), 10)
}

// appendPlain writes digits scaled by 10^exponent, exponent at most zero.
func appendPlain(out, digits []byte, exponent int) []byte {
	//: an integer.
	if exponent == 0 {
		//: the digits as they are.
		return append(out, digits...)
	}
	point := len(digits) + exponent
	//: the point falls inside the digits.
	if point > 0 {
		out = append(out, digits[:point]...)
		out = append(out, '.')
		//: the fraction.
		return append(out, digits[point:]...)
	}
	out = append(out, '0', '.')
	//: zeros between the point and the first digit.
	for range -point {
		out = append(out, '0')
	}
	//: then the digits.
	return append(out, digits...)
}

// ParseDecimal128 parses the specification's string form: an optional sign, a
// decimal with or without a point, an optional exponent introduced by e or E,
// or one of NaN, Inf and Infinity in any case. A value that a decimal128
// cannot hold exactly — more than 34 significant digits, or an exponent out of
// range — is refused with BSON_VALUE_INVALID rather than rounded; trailing
// zeros are traded for exponent, both ways, as far as that keeps it exact.
func ParseDecimal128(s string) (Decimal128, error) {
	body, negative := cutSign(s)
	//: the specials.
	if special, ok := parseDecimalSpecial(body, negative); ok {
		//: NaN ignores the sign; an infinity carries it.
		return special, nil
	}
	number, ok := scanDecimal(body)
	//: not the grammar.
	if !ok {
		//: refused without quoting the input.
		return Decimal128{}, valueError(nil, "ParseDecimal128: the input is not a decimal number")
	}
	value, exact := number.encode(negative)
	//: out of range, or more digits than the coefficient holds.
	if !exact {
		//: refused rather than rounded.
		return Decimal128{}, valueError(nil, "ParseDecimal128: the number is not exactly representable as a decimal128")
	}
	//: parsed.
	return value, nil
}

// cutSign splits a leading sign off s.
func cutSign(s string) (body string, negative bool) {
	//: no sign.
	if s == "" || (s[0] != '+' && s[0] != '-') {
		//: the string as given.
		return s, false
	}
	//: the rest, and whether the sign was a minus.
	return s[1:], s[0] == '-'
}

// parseDecimalSpecial recognises NaN, Inf and Infinity, in any case.
func parseDecimalSpecial(body string, negative bool) (Decimal128, bool) {
	//: NaN, signless.
	if equalFoldASCII(body, "nan") {
		//: the canonical quiet NaN.
		return Decimal128{h: decimalNaNBits}, true
	}
	//: an infinity.
	if equalFoldASCII(body, "inf") || equalFoldASCII(body, "infinity") {
		//: with its sign.
		if negative {
			//: negative.
			return Decimal128{h: decimalSignBit | decimalInfBits}, true
		}
		//: positive.
		return Decimal128{h: decimalInfBits}, true
	}
	//: not a special.
	return Decimal128{}, false
}

// equalFoldASCII reports whether s equals lower, an ASCII lowercase word, in
// any letter case.
func equalFoldASCII(s, lower string) bool {
	//: different lengths never match.
	if len(s) != len(lower) {
		//: no match.
		return false
	}
	//: byte by byte, upper case folded down.
	for i := range len(s) {
		c := s[i]
		//: fold A-Z.
		if 'A' <= c && c <= 'Z' {
			c += 'a' - 'A'
		}
		//: a mismatch.
		if c != lower[i] {
			//: no match.
			return false
		}
	}
	//: every byte matched.
	return true
}

// decimalNumber is a scanned decimal: its significant digits, without leading
// zeros, and the power of ten they are scaled by.
type decimalNumber struct {
	// digits are the significant digits, "0" for zero.
	digits []byte
	// exponent scales them, already corrected for the fraction digits; it is
	// saturated far outside the range a decimal128 can reach.
	exponent int
}

// scanDecimal reads the grammar digits ['.' digits] [e [sign] digits], where at
// least one digit precedes the exponent.
func scanDecimal(body string) (decimalNumber, bool) {
	coefficient := scanCoefficient(body)
	//: at least one digit, before or after the point.
	if !coefficient.sawDigit {
		//: not a number.
		return decimalNumber{}, false
	}
	exponent, ok := scanExponent(body[coefficient.end:])
	//: a malformed exponent, or trailing text.
	if !ok {
		//: not a number.
		return decimalNumber{}, false
	}
	digits := coefficient.digits
	//: an all-zero coefficient keeps one zero.
	if len(digits) == 0 {
		digits = append(digits, '0')
	}
	//: scanned.
	return decimalNumber{digits: digits, exponent: clampExponent(exponent - coefficient.fraction)}, true
}

// scannedCoefficient is the coefficient part of a decimal string.
type scannedCoefficient struct {
	// digits are the significant digits, leading zeros dropped.
	digits []byte
	// fraction counts the digits after the point.
	fraction int
	// end is where the coefficient stops.
	end int
	// sawDigit reports at least one digit.
	sawDigit bool
}

// scanCoefficient reads digits with at most one point, up to the first other
// byte.
func scanCoefficient(body string) scannedCoefficient {
	out := scannedCoefficient{digits: make([]byte, 0, len(body))}
	sawPoint := false
	//: byte by byte.
	for ; out.end < len(body); out.end++ {
		c := body[out.end]
		//: the one point.
		if c == '.' && !sawPoint {
			sawPoint = true
			continue
		}
		//: anything but a digit ends the coefficient.
		if c < '0' || c > '9' {
			break
		}
		out.add(c, sawPoint)
	}
	//: what was read.
	return out
}

// add records one digit of the coefficient.
func (s *scannedCoefficient) add(c byte, afterPoint bool) {
	s.sawDigit = true
	//: leading zeros carry no significance.
	if len(s.digits) > 0 || c != '0' {
		s.digits = append(s.digits, c)
	}
	//: a digit after the point scales the value down.
	if afterPoint {
		s.fraction++
	}
}

// scanExponent reads an optional exponent: e or E, an optional sign, digits,
// and nothing after them. The magnitude saturates at
// decimalExponentSaturation.
func scanExponent(rest string) (int, bool) {
	//: no exponent at all.
	if rest == "" {
		//: zero.
		return 0, true
	}
	//: the indicator.
	if rest[0] != 'e' && rest[0] != 'E' {
		//: trailing text.
		return 0, false
	}
	body, negative := cutSign(rest[1:])
	//: at least one digit.
	if body == "" {
		//: an incomplete exponent.
		return 0, false
	}
	value := 0
	//: digits only.
	for i := range len(body) {
		c := body[i]
		//: anything but a digit.
		if c < '0' || c > '9' {
			//: malformed.
			return 0, false
		}
		//: saturate instead of overflowing.
		if value < decimalExponentSaturation {
			value = value*10 + int(c-'0')
		}
	}
	//: the sign applies to the whole exponent.
	if negative {
		//: negative.
		return -value, true
	}
	//: positive.
	return value, true
}

// clampExponent saturates e at decimalExponentSaturation either way.
func clampExponent(e int) int {
	//: far above the range.
	if e > decimalExponentSaturation {
		//: still out of range, without growing further.
		return decimalExponentSaturation
	}
	//: far below the range.
	if e < -decimalExponentSaturation {
		//: still out of range, without growing further.
		return -decimalExponentSaturation
	}
	//: as computed.
	return e
}

// encode packs the number into a decimal128, trading trailing zeros for
// exponent while that stays exact. It reports false when no exact encoding
// exists.
func (n decimalNumber) encode(negative bool) (Decimal128, bool) {
	digits, exponent := n.digits, n.exponent
	//: a zero coefficient takes any exponent, clamped into range.
	if len(digits) == 1 && digits[0] == '0' {
		exponent = min(max(exponent, decimalMinExponent), decimalMaxExponent)
		//: zero, with its sign.
		return assembleDecimal(negative, 0, 0, exponent), true
	}
	//: too many digits: drop trailing zeros, raising the exponent.
	for len(digits) > decimalMaxDigits && digits[len(digits)-1] == '0' {
		digits = digits[:len(digits)-1]
		exponent++
	}
	//: still too many significant digits.
	if len(digits) > decimalMaxDigits {
		//: inexact.
		return Decimal128{}, false
	}
	digits, exponent, ok := fitExponent(digits, exponent)
	//: out of range in either direction.
	if !ok {
		//: inexact.
		return Decimal128{}, false
	}
	high, low := digitsToCoefficient(digits)
	//: packed.
	return assembleDecimal(negative, high, low, exponent), true
}

// fitExponent brings exponent into range by appending zeros (an exponent too
// large) or dropping trailing ones (too small), refusing when neither is exact.
func fitExponent(digits []byte, exponent int) ([]byte, int, bool) {
	//: clamping: multiply the coefficient by ten while it has room.
	for exponent > decimalMaxExponent {
		//: no room for another digit.
		if len(digits) >= decimalMaxDigits {
			//: overflow.
			return nil, 0, false
		}
		digits = append(digits, '0')
		exponent--
	}
	//: the subnormal range: divide by ten while that drops only zeros.
	for exponent < decimalMinExponent {
		//: a significant digit would be lost.
		if digits[len(digits)-1] != '0' {
			//: underflow.
			return nil, 0, false
		}
		digits = digits[:len(digits)-1]
		exponent++
	}
	//: in range.
	return digits, exponent, true
}

// digitsToCoefficient converts at most 34 decimal digits to the 113-bit
// coefficient high:low.
func digitsToCoefficient(digits []byte) (high, low uint64) {
	//: high:low = high:low*10 + digit, digit by digit; 34 digits stay below
	//: 2^113, so the high half never overflows.
	for _, c := range digits {
		productHigh, productLow := bits.Mul64(low, decimalRadix)
		var carry uint64
		low, carry = bits.Add64(productLow, uint64(c-'0'), 0)
		high = high*decimalRadix + productHigh + carry
	}
	//: the coefficient.
	return high, low
}

// assembleDecimal packs a sign, a coefficient and an in-range exponent.
func assembleDecimal(negative bool, high, low uint64, exponent int) Decimal128 {
	h := high | (uint64(exponent+decimalExponentBias)&decimalExponentMask)<<49
	//: the sign bit.
	if negative {
		h |= decimalSignBit
	}
	//: packed.
	return Decimal128{h: h, l: low}
}

// MarshalJSON writes d's string form as a JSON string.
func (d Decimal128) MarshalJSON() ([]byte, error) {
	//: the string form, quoted; it holds no character JSON escapes.
	return strconv.AppendQuote(nil, d.String()), nil
}

// UnmarshalJSON reads a JSON string holding the string form, or the
// extended-JSON object {"$numberDecimal": "…"}. A JSON null leaves d unchanged.
func (d *Decimal128) UnmarshalJSON(data []byte) error {
	//: null is "no value".
	if string(data) == "null" {
		//: nothing to set.
		return nil
	}
	text, err := decimalJSONText(data)
	//: neither accepted shape.
	if err != nil {
		//: d is left as it was.
		return err
	}
	parsed, err := ParseDecimal128(text)
	//: not a decimal128.
	if err != nil {
		//: d is left as it was.
		return err
	}
	*d = parsed
	//: set.
	return nil
}

// decimalJSONText extracts the string form from a JSON string or an
// extended-JSON {"$numberDecimal": "…"} object.
func decimalJSONText(data []byte) (string, error) {
	var text string
	//: the plain string form.
	if err := json.Unmarshal(data, &text); err == nil {
		//: found.
		return text, nil
	}
	var wrapped struct {
		// Number is the extended-JSON member.
		Number *string `json:"$numberDecimal"`
	}
	//: the extended-JSON form.
	if err := json.Unmarshal(data, &wrapped); err != nil || wrapped.Number == nil {
		//: neither shape.
		return "", valueError(err, `Decimal128.UnmarshalJSON: the input is neither a JSON string nor {"$numberDecimal": string}`)
	}
	//: found.
	return *wrapped.Number, nil
}
