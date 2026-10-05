package otlp

import (
	"math"
	"strconv"
)

// The three spellings proto3 JSON gives a non-finite double. A double is "a
// number or one of the special string values 'NaN', 'Infinity', and
// '-Infinity'", so a NaN gauge reading or a NaN-valued attribute is
// expressible rather than fatal — unlike encoding/json's own float path, which
// refuses it outright.
const (
	jsonNaN         string = `"NaN"`
	jsonPosInfinity string = `"Infinity"`
	jsonNegInfinity string = `"-Infinity"`
)

// The strconv parameters every rendering here uses: base ten, and the
// shortest-round-trip spelling of a 64-bit double.
const (
	decimalBase  int  = 10
	floatFmt     byte = 'g'
	floatPrec    int  = -1
	floatBitSize int  = 64
)

// intBufferSize pre-sizes a quoted 64-bit decimal: 20 digits, a sign, two
// quotes, rounded up.
const intBufferSize int = 24

// MarshalJSON renders the integer as a quoted decimal string.
func (v Int64) MarshalJSON() (encoded []byte, err error) {
	//: quote, digits, quote — no escaping is possible inside a decimal.
	out := make([]byte, 0, intBufferSize)
	out = append(out, '"')
	out = strconv.AppendInt(out, int64(v), decimalBase)
	//: json.Marshal never sees an error from a decimal rendering.
	return append(out, '"'), nil
}

// MarshalJSON renders the integer as a quoted decimal string.
func (v Uint64) MarshalJSON() (encoded []byte, err error) {
	//: quote, digits, quote.
	out := make([]byte, 0, intBufferSize)
	out = append(out, '"')
	out = strconv.AppendUint(out, uint64(v), decimalBase)
	//: json.Marshal never sees an error from a decimal rendering.
	return append(out, '"'), nil
}

// MarshalJSON renders the double shortest-round-trip, or names it when it is
// not finite.
func (v Double) MarshalJSON() (encoded []byte, err error) {
	//: a value that is not a number has a name rather than a rendering.
	value := float64(v)
	//: NaN first — it fails every ordered comparison below.
	if math.IsNaN(value) {
		//: the schema's spelling, quoted.
		return []byte(jsonNaN), nil
	}
	//: +Inf.
	if math.IsInf(value, 1) {
		//: the schema's spelling, quoted.
		return []byte(jsonPosInfinity), nil
	}
	//: -Inf.
	if math.IsInf(value, -1) {
		//: the schema's spelling, quoted.
		return []byte(jsonNegInfinity), nil
	}
	//: finite: shortest representation that round-trips, which is a JSON
	//: number in every form strconv produces (123, 1.5, 1e+21, -1.5e-08).
	return strconv.AppendFloat(nil, value, floatFmt, floatPrec, floatBitSize), nil
}
