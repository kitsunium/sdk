// Package bson is the BSON codec: a native implementation of the BSON 1.1
// specification (bsonspec.org) behind the universal core/data/codec.Codec
// dispatch, written with the standard library alone.
//
// BSON is a document format: the top-level value MUST encode as a document — a
// struct, a map, a D — so Marshal of a top-level scalar or array surfaces
// BSON_MARSHAL_FAILED. Go values map onto BSON as the MongoDB Go driver's v1
// default registry mapped them, struct tags included
// (bson:"name,omitempty,minsize,truncate,inline"), and the BSON types Go has
// no type for decode into this package's own: ObjectID, DateTime, Decimal128,
// Binary, Regex, Timestamp, D, M, A and the rest.
//
// Unmarshal checks the whole input before it touches the target — every
// declared length against the bytes remaining, every terminator, every type
// byte, every string's UTF-8, a nesting depth of at most 100 — so a malformed
// document is refused whole and never half-decoded. The codec is not a
// StreamingCodec; it implements the optional Appender.
//
// Package bson — Decimal128, BSON's IEEE 754-2008 128-bit decimal in the
// binary integer decimal (BID) encoding, and its string forms as the BSON
// decimal128 specification defines them. Conversion is exact or refused:
// nothing here rounds.
//
// Package bson — the decoder's entry point, its dispatch on the target's plan,
// and the element walk. It runs over a document the validator accepted, and
// still checks every length it slices by, so input that changes under it
// yields an error and never a panic.
//
// Package bson — decoding into an interface: the Go value each BSON type
// becomes when nothing else says what it should be. The table is the previous
// library's, its types replaced by this package's: a double is a float64, an
// int32 an int32, an int64 an int64, a string a string, a boolean a bool, a
// null nil, an array an A, a datetime a DateTime, a binary a Binary, and so
// on. A document becomes the ancestor type when one is set — the type of the
// nearest enclosing map[string]any, M or slice of E — and a D otherwise, so a
// document decoded into *any is a D all the way down while one decoded into a
// map[string]any is maps all the way down.
//
// Package bson — decoding into the containers: slices, arrays, D, maps and
// structs. Each follows the previous library's rules: a slice reuses its
// backing array, an array keeps the elements a short value does not reach, a
// map is added to rather than replaced, a struct keeps the fields the document
// does not name.
//
// Package bson — decoding into scalar targets. The conversions accepted are
// the previous library's: a number decodes into any numeric target it fits, a
// boolean into a number and a number into a boolean, null and undefined into
// the zero value; anything else is refused rather than guessed.
//
// Package bson — decoding into the codec's own value types and into the
// stdlib types it knows: time.Time, url.URL and json.Number. A null or an
// undefined is the zero value of every one of them.
//
// Package bson — the encoder's entry point and dispatch. It appends one
// document to a byte slice, reflection planned once per type, the common
// interface values taken by a type switch instead. Each element's type byte is
// written as a placeholder and set once the value has said what it encodes as;
// each document's length is reserved and filled in when the document ends.
//
// Package bson — encoding of the containers: arrays, ordered documents,
// structs and maps. A map's entries are written in the map's own order and
// then put in ascending key order, so the same map always encodes to the same
// bytes.
//
// Package bson — the encoder's primitive writes: the fixed-width numbers, the
// strings, the binaries, each little-endian as BSON stores them.
//
// Package bson — encoding of the codec's own value types and of the stdlib
// types the codec knows: time.Time, url.URL and json.Number.
//
// Package bson — the constructors that give each failure its own log-only
// detail. The codes and the sentinels they wrap are declared in
// internal/core/data/codec/bson (ADR 0160).
//
// Package bson — ObjectID, BSON's twelve-byte identifier, and its textual
// forms: 24 lowercase hexadecimal digits in text and in JSON, the form MongoDB
// tooling prints.
//
// Package bson — the per-type plans: how a Go type maps onto BSON, worked out
// once from reflection and cached by reflect.Type, so an encode or a decode
// only reads what was decided. A plan is built under one mutex and published
// complete; reading a published plan takes no lock.
//
// Package bson — the Go types for the BSON values Go has no type of its own
// for. Decoding into an interface produces them, encoding writes them back as
// the BSON type they stand for, and they carry the semantics the codec's
// previous library gave its own equivalents, so a value that crossed a
// map[string]any before keeps meaning the same thing.
//
// Package bson — the structural validator. Unmarshal runs it over the whole
// input before the target is touched, so a malformed document never leaves a
// half-decoded value behind, and the decoder that follows reads bytes whose
// every length, terminator, type byte and string has been checked. Every
// declared length is compared with the bytes actually remaining before
// anything is sliced or allocated from it.
//
// Package bson — the BSON 1.1 wire vocabulary (bsonspec.org): the element type
// bytes, the binary subtypes, the bounds every reader and writer shares, and
// the little-endian reads they are built from.
package bson
