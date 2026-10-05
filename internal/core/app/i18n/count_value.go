package i18n

import (
	"math"

	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// maxFractionDigits bounds [Decimal]'s second argument. Ten fraction digits
// would already exceed float64's honest precision for most integer parts, and
// the operand f is an integer of that many digits — so the bound is where the
// arithmetic stops being able to tell the truth, not a round number.
const maxFractionDigits int = 9

// decimalBase is ten, because that is what a fraction DIGIT is worth.
const decimalBase uint64 = 10

// pow10 indexes 10^n for n in 0..maxFractionDigits.
//
// It is built by multiplication rather than written out, for the reason the
// table exists at all: math.Pow returns a float64 whose nearest representable
// value for 10^7 is not exactly 10000000, and a hand-written literal table is
// ten numbers a reviewer has to count the zeros in.
var pow10 = buildPow10()

// buildPow10 fills the power table once, at package initialisation.
func buildPow10() [maxFractionDigits + 1]uint64 {
	//: one entry per admissible fraction-digit count.
	var table [maxFractionDigits + 1]uint64
	//: 10^0.
	value := uint64(1)
	//: each entry is the previous one times ten.
	for i := range table {
		//: store, then advance.
		table[i] = value
		//: the next power.
		value *= decimalBase
	}
	//: the exact powers.
	return table
}

// Int returns the [CountValue] of an exact integer, displayed with no fraction
// digits. It cannot fail.
//
// The sign is discarded, because CLDR's operands are defined on the absolute
// value. It is discarded here rather than in each of thirteen rules, so a rule
// cannot forget.
func Int(n int64) CountValue {
	//: negative values need the absolute value without overflowing at
	//: math.MinInt64, where -n is not representable.
	if n < 0 {
		//: two's complement: -(n+1) is always representable, then add one.
		return CountValue{integerPart: uint64(-(n + 1)) + 1}
	}
	//: non-negative converts directly.
	return CountValue{integerPart: uint64(n)}
}

// Decimal returns the [CountValue] of value displayed with exactly
// fractionDigits fraction digits, or [InvalidCount].
//
// fractionDigits is the DISPLAY precision, not a property of value: passing
// (1, 2) describes "1.00", which is `other` in English, while passing (1, 0)
// describes "1", which is `one`. That is the operand v, and it is the caller's
// to state because only the caller knows how the number will be written.
//
// Rounding carries, exactly as a formatter's would: Decimal(0.999, 2)
// describes "1.00" and reports an integer part of 1, not 0. Getting that wrong
// would make the SDK disagree with the string beside it on the page.
func Decimal(value float64, fractionDigits int) (count CountValue, err error) {
	//: a value with no finite decimal expansion has no operands.
	if math.IsNaN(value) || math.IsInf(value, 0) {
		//: refuse rather than describe it as zero.
		return CountValue{}, errs.Wrap(InvalidCount, errs.WrapParams{}, errs.String("detail", "value is NaN or infinite"))
	}
	//: the operand v has a hard bound where the arithmetic stops being exact.
	if fractionDigits < 0 || fractionDigits > maxFractionDigits {
		//: name the bound, never the value — the ADR 0046 rule, applied here.
		return CountValue{}, errs.Wrap(InvalidCount, errs.WrapParams{},
			errs.Int("fraction_digits", fractionDigits), errs.Int("max_fraction_digits", maxFractionDigits))
	}
	//: CLDR operands are defined on the absolute value.
	abs := math.Abs(value)
	//: the integer part must fit an int64 before it can fit a uint64 safely.
	if abs >= math.MaxInt64 {
		//: refuse rather than wrap around into a small integer part.
		return CountValue{}, errs.Wrap(InvalidCount, errs.WrapParams{}, errs.String("detail", "integer part exceeds int64"))
	}
	//: split into the two halves CLDR names i and f.
	return splitDecimal(abs, fractionDigits), nil
}

// splitDecimal derives the operands i, f and v from a finite, non-negative
// value and a display precision. It is separate from [Decimal] so the refusals
// and the arithmetic read as two steps.
func splitDecimal(abs float64, fractionDigits int) CountValue {
	//: the integer part is the truncation, matching what a formatter prints.
	whole := math.Trunc(abs)
	//: scale the remainder to an integer of exactly fractionDigits digits.
	scale := pow10[fractionDigits]
	//: round half away from zero, which is what a decimal formatter does.
	fraction := uint64(math.Round((abs - whole) * float64(scale)))
	//: the integer part, before any carry.
	integer := uint64(whole)
	//: rounding can carry into the integer part: 0.999 at two digits is 1.00.
	if fraction >= scale {
		//: carry, exactly as the printed string would.
		integer++
		//: and the fraction digits become all zeros.
		fraction -= scale
	}
	//: the three operands.
	return CountValue{
		integerPart:           integer,
		fractionValue:         fraction,
		visibleFractionDigits: uint8(fractionDigits), //nolint:gosec // bounded by maxFractionDigits above.
	}
}

// IntegerPart returns CLDR operand i: the integer digits of the absolute
// value.
func (c CountValue) IntegerPart() uint64 {
	//: stored directly.
	return c.integerPart
}

// FractionValue returns CLDR operand f: the visible fraction digits read as an
// integer, trailing zeros included. Decimal(1.50, 2) reports 50.
func (c CountValue) FractionValue() uint64 {
	//: stored directly.
	return c.fractionValue
}

// VisibleFractionDigits returns CLDR operand v: how many fraction digits the
// reader sees. Decimal(1, 2) reports 2 even though the value is an integer.
func (c CountValue) VisibleFractionDigits() int {
	//: bounded by maxFractionDigits at construction.
	return int(c.visibleFractionDigits)
}

// isIntegerValued is CountValue.IsIntegerValued's body: decl_gen.go writes CountValue.IsIntegerValued, from the
// design, as one call of it.
func (c CountValue) isIntegerValued() bool {
	//: n == i exactly when the fraction digits carry no value.
	return c.fractionValue == 0
}
