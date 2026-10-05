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

// Int64 is a signed 64-bit integer rendered as a DECIMAL STRING.
//
// That is the proto3 JSON mapping OTLP inherits — "64-bit integer numbers in
// JSON-encoded payloads are encoded as decimal strings" — and the reason is
// range, not taste: a JSON number is a double in most parsers, so an int64
// past 2^53 loses its low bits on the way through.
type Int64 int64

// Uint64 is an unsigned 64-bit integer rendered as a decimal string, for the
// same reason Int64 is: fixed64 and uint64 both map to a string. Every
// timestamp in an OTLP payload uses it, and every one of them is past 2^53.
type Uint64 uint64

// Double is an IEEE-754 double rendered as a JSON number, or as one of the
// three quoted spellings proto3 JSON gives a non-finite value.
//
// encoding/json refuses NaN and ±Inf outright ("json: unsupported value"), so
// without this type a single NaN would fail the whole export. The model allows
// the value — a gauge is whatever was sampled — and the specification says how
// to spell it, so the encoder spells it.
type Double float64

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
