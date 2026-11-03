package json

import "github.com/kitsunium/sdk/internal/kernel/jsonenc"

// Object is a JSON object being written by [BeginObject]: members land in
// the order they are named, and nothing allocates but the caller's slice.
type Object = jsonenc.Object

// AppendString appends s to dst as a JSON string literal, quotes included.
// See the package comment for the escapes: invalid UTF-8 becomes U+FFFD,
// U+2028 and U+2029 are escaped, and '<', '>' and '&' are not.
func AppendString(dst []byte, s string) []byte {
	return jsonenc.AppendString(dst, s)
}

// AppendEscaped appends the body of the JSON string literal of s — what
// [AppendString] writes between its quotes — so a caller can join several
// pieces into one literal.
func AppendEscaped(dst []byte, s string) []byte {
	return jsonenc.AppendEscaped(dst, s)
}

// AppendInt appends v to dst in decimal, as a JSON number.
func AppendInt(dst []byte, v int64) []byte {
	return jsonenc.AppendInt(dst, v)
}

// AppendUint appends v to dst in decimal, as a JSON number. A value past
// 2^53 is exact here and is not in a JavaScript reader.
func AppendUint(dst []byte, v uint64) []byte {
	return jsonenc.AppendUint(dst, v)
}

// AppendFloat appends f to dst as the JSON number encoding/json writes for a
// float64 (1000000, not 1e+06; 1e-7, not 1e-07) and reports true; for NaN
// and ±Inf it appends nothing and reports false, so the caller writes its
// own policy for them.
func AppendFloat(dst []byte, f float64) (number []byte, ok bool) {
	return jsonenc.AppendFloat(dst, f)
}

// BeginObject appends the opening brace of an object to dst and returns the
// [Object] that writes its members after it, in the order they are named.
func BeginObject(dst []byte) Object {
	return jsonenc.BeginObject(dst)
}
