// Package cbor is the SDK's CBOR codec (RFC 8949), written on the standard
// library alone. It encodes any Go value the reflection rules below can
// read, decodes into any Go value they can fill, and streams one data item
// per Encode or Decode.
//
// # What is written
//
// Integers, lengths and tag numbers take their shortest head; a float64 is a
// double and a float32 a single, NaN is written 0xf97e00 and the infinities
// as half-precision floats; a string is a text string, a []byte or [N]byte a
// byte string; a nil slice, map, pointer or interface is null; a struct is a
// map keyed by field (or an array with toarray); a time.Time is its integer
// Unix seconds, null when zero; a big.Int an integer or a bignum (tag 2 or 3).
// Map pairs are sorted by encoded key (RFC 8949 §4.2.1), so equal values
// encode to equal bytes. Nothing is written that the decoder would refuse:
// not a string that is not UTF-8, not a map with two keys that encode alike,
// nothing nested deeper than 32 arrays, maps and tags.
//
// # What is read
//
// Exactly one well-formed, valid data item — definite and indefinite
// lengths, half, single and double floats, every major type — checked in
// full, against the bounds (a million elements per array or pairs per map,
// thirty-two levels, a million chunks per string), before anything is
// decoded: malformed input never touches the target. A text string must be
// UTF-8 wherever it sits; tags 0 to 3 must enclose the type RFC 8949 gives
// them. A tag the codec does not interpret is transparent.
//
// # Struct tags
//
// A field's key is the name in its `cbor` tag, or in its `json` tag when it
// has no `cbor` tag, or its Go name; the options are omitempty, omitzero and
// keyasint, and a blank field `_ struct{}` tagged `cbor:",toarray"` encodes
// a struct as an array. Embedded structs follow encoding/json's rules. A
// type implementing encoding.BinaryMarshaler is written as a byte string,
// and one implementing encoding.BinaryUnmarshaler reads one; a type with
// MarshalCBOR or UnmarshalCBOR writes or reads its own item.
//
// Package cbor — the decoder's walk over input the validator has accepted.
//
// Because the item was validated first, every length, count and offset the
// walk reads is already known to fit the input and the bounds, so nothing is
// allocated on the word of an unchecked count. The walk still checks every
// read against the input: an inconsistency is an error, never a panic.
//
// An item the target cannot hold — a string for an int, an integer that
// overflows its field — does not stop the walk: the first such failure is
// kept, the item is skipped, and the rest is decoded, as fxamacker/cbor and
// encoding/json both do.
//
// Package cbor — decoding into an untyped target. Each data item becomes its
// default Go value, built without reflection:
//
//	unsigned integer          uint64
//	negative integer          int64, or big.Int below math.MinInt64
//	byte string               []byte
//	text string               string
//	array                     []any
//	map, every key text       map[string]any
//	map, any other key        map[any]any
//	true, false               bool
//	null, undefined           nil
//	half, single, double      float64
//	tag 0 or 1                time.Time
//	tag 2 or 3 (bignum)       big.Int
//	any other tag             its content, as though untagged
//
// An unassigned simple value has no Go value and is refused, as RFC 8949
// §5.4 allows a decoder to; so is a map key Go cannot hash — a byte string,
// an array, a map or a bignum — since it cannot be a key of a map[any]any.
//
// Package cbor — decoding into byte slices and arrays, slices, arrays and
// maps. A slice is resized to the array it receives and the elements it
// reuses are zeroed first, so nothing of its previous content survives; an
// array keeps its length, dropping extra elements and zeroing missing ones;
// a map keeps its entries and gains the decoded ones.
//
// Package cbor — decode plans: how a data item is stored into a value of one
// Go type, resolved once per type and cached, the mirror of encode plans.
//
// Package cbor — decoding into scalar targets: booleans, integers of every
// width, floats, strings, time.Time and big.Int. An integer is stored where
// it fits and refused where it would overflow; a bignum (tag 2 or 3) counts
// as an integer; a float is never stored into an integer.
//
// Package cbor — decoding into structs. A map's text keys find their field
// exactly, then ignoring case; its integer keys find a keyasint field; a key
// no field has is skipped with its value, and a key seen twice keeps its
// first value — fxamacker/cbor's rules. A toarray struct takes an array of
// exactly as many elements as it has fields.
//
// Package cbor — the streaming decoder: one data item per Decode, read from
// the stream as it arrives. The validator keeps its place across reads, so
// each byte is validated once however the item is split, and an item is only
// decoded once it has arrived whole and passed. The decoder buffers one item
// at a time; bound the reader (io.LimitReader) when its peer is untrusted.
//
// Package cbor — the encoder's entry point and its scalars. Encoding appends
// to a byte slice and never builds an intermediate value: the common
// dynamic types an untyped document holds are matched by a type switch, and
// every other type goes through the plan resolved once for it.
//
// What the encoder writes is fixed: integers, lengths and tag numbers in
// their shortest head; a float64 as a double, a float32 as a single, NaN as
// 0xf97e00 and the infinities as half-precision; a time.Time as its integer
// Unix seconds (null when zero); a nil slice, map or pointer as null; map
// pairs in the bytewise order of their encoded keys (RFC 8949 §4.2.1), so
// equal values encode to equal bytes. It writes nothing its own decoder would
// refuse: no string that is not UTF-8, no two equal keys in a map, nothing
// nested deeper than maxCBORNestedLevels.
//
// Package cbor — the kind encoders of pointers, interfaces, byte strings,
// arrays and the types CBOR cannot carry.
//
// Package cbor — maps on the encoding side. Pairs are written in the bytewise
// lexicographic order of their encoded keys, the order RFC 8949 §4.2.1 gives
// deterministic encoding, so a map encodes to the same bytes on every run;
// two keys that encode alike — 1 and uint(1) in a map[any]any, two NaNs —
// are refused rather than written as an invalid map.
//
// Package cbor — encode plans: how the values of one Go type are encoded,
// resolved once per type and cached. A plan holds the kind encoder that
// writes a value and answers omitempty, the omitzero question, and the plans
// of the types it is made of — so walking a value is a chain of direct calls
// with no lookup.
//
// Package cbor — the kind encoders of the scalars: booleans, integers of
// every width, floats and strings, each answering omitempty for its kind.
//
// Package cbor — the kind encoders of the types with an encoding of their
// own: time.Time as Unix seconds, a type's MarshalCBOR verbatim once checked,
// and a BinaryMarshaler as a byte string.
//
// Package cbor — structs on the encoding side: a map keyed by field name (or
// by integer, keyasint), or an array (toarray); omitempty and omitzero.
//
// Package cbor — the streaming encoder: one data item per Encode, written in
// one Write once it is completely encoded, so a value that cannot be encoded
// writes nothing.
//
// Package cbor — the constructors every failure goes through. Each one
// carries one of the two codes internal/core/data/codec/cbor declares for
// this package (ADR 0160) and its reason, and a Private line that says what
// was refused — a type, a bound, an offset — and never a value read from the
// input.
//
// Package cbor — struct fields: which fields of a struct are on the wire,
// under which key, and in which order. The visibility and embedding rules
// are encoding/json's; the key comes from the `cbor` tag, or from the `json`
// tag when a field has no `cbor` tag — the same resolution fxamacker/cbor
// applied, so a struct keeps its encoding across the change of engine.
//
// Package cbor — hosts the compile-time interface assertions, keeping them
// out of the production source so the runtime binary carries no
// diagnostic-only declarations.
//
// Package cbor — the validator: one pass over the bytes, before anything is
// decoded, that accepts exactly one well-formed and valid data item within
// the bounds. A document it refuses never touches the caller's value, and no
// length, count or depth read from the input is acted on until it has passed.
//
// Package cbor — the RFC 8949 vocabulary: the eight major types, the
// additional information of an initial byte, the head every data item opens
// with, and the IEEE 754 half-precision float a decoder must read.
package cbor
