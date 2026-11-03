package jsonenc

import (
	"math"
	"strconv"
)

// The cut-offs and the shape of encoding/json's float spelling (encode.go,
// floatEncoder): plain notation inside [plainMin, plainMax), exponent notation
// outside it, and an exponent of one digit written without its leading zero.
const (
	// plainMin is the smallest magnitude written in plain notation.
	plainMin float64 = 1e-6
	// plainMax is the first magnitude written in exponent notation again.
	plainMax float64 = 1e21
	// shortest asks strconv for the fewest digits that read back to the value.
	shortest int = -1
	// float64Bits is the size strconv rounds to: the value is a float64.
	float64Bits int = 64
	// shortExponent is the length of the tail "e-0d" whose zero is dropped.
	shortExponent int = 4
)

// AppendFloat appends f to dst as the JSON number encoding/json writes for
// a float64 and reports true; for NaN, +Inf and -Inf, which JSON cannot
// write, it appends nothing and reports false, so the caller spells its
// own policy for them. See the package comment for the exact spelling.
func AppendFloat(dst []byte, f float64) (number []byte, ok bool) {
	//: NaN and the infinities have no JSON number: the caller decides.
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return dst, false
	}
	verb := byte('f')
	abs := math.Abs(f)
	//: outside [1e-6, 1e21) the plain form would be long: ES6 writes an exponent.
	if abs != 0 && (abs < plainMin || abs >= plainMax) {
		verb = 'e'
	}
	number = strconv.AppendFloat(dst, f, verb, shortest, float64Bits)
	//: only an exponent form can end in e-0d.
	if verb == 'e' {
		number = trimExponentZero(number)
	}
	return number, true
}

// trimExponentZero rewrites a trailing "e-0d" as "e-d", as encoding/json does:
// strconv pads a negative exponent to two digits and ES6 does not. A positive
// exponent is never short here — 1e21 is the smallest written with one — so
// only the negative form is looked for.
func trimExponentZero(b []byte) []byte {
	n := len(b)
	//: e-0d: move the digit over the zero and drop the last byte.
	if n >= shortExponent && b[n-4] == 'e' && b[n-3] == '-' && b[n-2] == '0' {
		b[n-2] = b[n-1]
		return b[:n-1]
	}
	return b
}
