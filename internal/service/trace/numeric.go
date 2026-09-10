// Package trace — the numeric rendering parameters shared by the encoder and
// the samplers, carved out so neither file re-declares them.
package trace

import "strconv"

const (
	// decimalBase is base ten, named so the no-magic-number rule is satisfied
	// at every strconv call site.
	decimalBase int = 10
	// floatBitSize is the width every float in this package has.
	floatBitSize int = 64
	// floatFmt is strconv's shortest-round-trip format verb.
	floatFmt byte = 'g'
	// floatPrec asks strconv for the shortest representation that parses back
	// to the same value.
	floatPrec int = -1
)

// formatFloat renders a finite double shortest-round-trip. Every form strconv
// produces for it (123, 1.5, 1e+21, -1.5e-08) is a valid JSON number, which is
// why the encoder can hand the result straight to a payload.
func formatFloat(value float64) string {
	//: shortest representation that round-trips.
	return strconv.FormatFloat(value, floatFmt, floatPrec, floatBitSize)
}
