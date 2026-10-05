package trace

import "strconv"

const (
	// floatBitSize is the width every float in this package has.
	floatBitSize int = 64
	// floatFmt is strconv's shortest-round-trip format verb.
	floatFmt byte = 'g'
	// floatPrec asks strconv for the shortest representation that parses back
	// to the same value.
	floatPrec int = -1
)

// formatFloat renders a finite double shortest-round-trip (123, 1.5, 1e+21,
// -1.5e-08) — the spelling a refused ratio takes in its error field.
func formatFloat(value float64) string {
	//: shortest representation that round-trips.
	return strconv.FormatFloat(value, floatFmt, floatPrec, floatBitSize)
}
