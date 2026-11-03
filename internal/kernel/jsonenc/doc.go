// Package jsonenc appends JSON to a byte slice: a string literal, an integer,
// a float, and an object whose members are written in the order they are named. It is a
// kernel primitive — stdlib-only, and generic down to the signatures: a slice
// and a value, with no logger, no token and no codec in any of them. It
// registers no codec and reads no value by reflection; encoding/json stays the
// codec, and codec/json.Marshal does not change. This package is for the
// emitters that already write JSON by hand, so that the SDK has one string
// escaper and not four.
//
// # What a string becomes
//
// [AppendString] writes s between quotes the way encoding/json writes it, with
// one difference stated after the table:
//
//	"  and  \         a backslash before each
//	\b \f \n \r \t    their short escapes
//	other < 0x20      \u00XX, lowercase hex
//	U+2028, U+2029    \u2028, \u2029, as encoding/json does, so that a
//	                  document pasted into a script is not cut at a line end
//	invalid UTF-8     each bad byte becomes U+FFFD, written as its three
//	                  bytes, so the output is always valid UTF-8
//	everything else   as it is, including DEL (0x7f)
//
// There is NO HTML escaping: '<', '>' and '&' are written as they are, as
// json.Encoder does after SetEscapeHTML(false) and as json.Marshal does not.
// A caller that embeds the result in HTML must escape it itself. That is the
// one difference from json.Marshal, which escapes them as \u003c, \u003e and
// \u0026, and the fuzz test holds the rest to it: every input decodes to the
// string json.Marshal's output decodes to, and valid input is byte for byte what
// a json.Encoder with HTML escaping off writes.
//
// # How it is fast
//
// The string is searched a word at a time for the next byte that needs
// attention — a quote, a backslash, a byte below 0x20 or a byte from 0x80 up —
// by the first-match search of internal/kernel/swar, and everything before it
// is copied in one append. A string of ASCII with nothing to escape, which is
// most of them, costs one search and one copy. A byte from 0x80 up is where the
// slow path starts: the run of non-ASCII is decoded rune by rune, validated,
// and copied whole unless it holds a bad byte or a line separator.
//
// # What a float becomes
//
// [AppendFloat] writes a finite float64 as encoding/json writes it — the ES6
// number-to-string rule: the shortest decimal that reads back to the same
// value, in plain notation from 1e-6 up to (not including) 1e21 and in
// exponent notation outside it, with an exponent of one digit written
// without its leading zero (1e-7, not 1e-07). So 1e6 is 1000000 and not
// 1e+06, as strconv's 'g' would write it. NaN and ±Inf have no JSON number:
// AppendFloat appends nothing for them and reports false, and the caller
// writes what its own format says — null, a quoted name, a refusal.
//
// # Objects
//
// [BeginObject] opens an object on a slice, and each member method returns the
// object again, so a call chain reads as the document and writes members in
// exactly the order it names them: there is no sorting, no map and no
// duplicate check. A key is escaped like any string. [Object.End] closes it
// and returns the slice. An object is a value of two words and allocates
// nothing; the slice it grows is the caller's.
//
// # Cost
//
// Nothing here allocates once the slice has the capacity, and BENCH.md has the
// figures against encoding/json and the byte loops this replaced.
package jsonenc
