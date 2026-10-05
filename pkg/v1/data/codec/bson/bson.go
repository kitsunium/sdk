//go:generate gomarkdoc --output README.md --repository.url https://github.com/kitsunium/sdk --repository.default-branch main --repository.path /pkg/v1/data/codec/bson .

// Package bson is BSON, MongoDB's binary document format, implemented with the
// standard library alone. Importing it registers the BSON codec with the SDK's
// codec registry — and no other codec (ADR 0134) — and names the Go types the
// BSON values Go has no type of its own for decode into:
//
//	import "github.com/kitsunium/sdk/pkg/v1/data/codec/bson"
//
//	data, err := bson.Marshal(bson.D{{Key: "name", Value: "kitsune"}, {Key: "tails", Value: 9}})
//	var doc map[string]any
//	err = bson.Unmarshal(data, &doc) // doc["tails"] is an int32
//
// Everything that dispatches through the registry by format name reads and
// writes BSON as well: the codec package's Marshal and Unmarshal under
// [Format], config.FSSource, i18n.LoadFS. Importing it beside
// github.com/kitsunium/sdk/pkg/v1/data/codec is harmless: the format is registered
// once.
//
// # Mapping
//
// The top level is a document: a struct, a map, a [D]; anything else is
// refused. Struct fields take the tag bson:"name,omitempty,minsize,truncate,inline"
// and default to the field name in lower case. int, int8, int16, int32, uint8
// and uint16 are written as int32 (int as int64 when it does not fit), int64,
// uint, uint32 and uint64 as int64, floats as double, []byte as a generic
// binary, time.Time as a UTC datetime in milliseconds, a nil slice, map or
// pointer as null, and map keys in sorted order, so the same value always
// encodes to the same bytes. A type can write and read itself through
// MarshalBSON() ([]byte, error) and UnmarshalBSON([]byte) error.
//
// Decoding into an interface gives float64, int32, int64, string, bool, nil,
// [A] for an array, [DateTime], [Binary], [ObjectID], [Decimal128] and the
// other value types below — and, for a document, a [D] when decoding into
// *any, a map[string]any when decoding into a map[string]any. Numbers decode
// into any numeric field they fit, a double into an integer only when whole
// unless the field is tagged truncate.
//
// # Safety
//
// Unmarshal checks the whole input before it writes anything: one document,
// at most 10 MiB, every declared length within the bytes there are, every
// string UTF-8, nested at most 100 levels. A malformed document is refused
// whole. Marshal refuses what BSON cannot hold — a string that is not UTF-8, a
// uint64 past the int64 range, a cyclic value — instead of writing something
// a reader would refuse.
//
// # Errors
//
// Failures carry the range 0.3.36.*; match them with errs.HasCode:
//
//	CodeMarshalFailed     0.3.36.1  a value with no BSON form, or a top level that is not a document
//	CodeUnmarshalFailed   0.3.36.2  malformed input, or a value the target cannot hold
//	CodeSizeExceeded      0.3.36.3  an input past 10 MiB
//	CodeDepthExceeded     0.3.36.4  nesting past 100 levels, either way
//	CodeValueInvalid      0.3.36.5  ObjectIDFromHex, ParseDecimal128 and the JSON forms refusing their input
//
// No error message quotes a key or a value from the input.
package bson

import (
	corebson "github.com/kitsunium/sdk/internal/core/data/codec/bson"
	svcbson "github.com/kitsunium/sdk/internal/service/data/codec/bson"
)

// Format is the name BSON is registered under. It is an untyped constant, so
// it goes wherever a format name is taken — the codec package's Marshal,
// config.FSSource's string, i18n.LoadFS's codec.Format — without a conversion.
const Format = "bson"

// The sentinels, for errors.Is, and the nil ObjectID.
var (
	// MarshalFailed marks a value Marshal or Append cannot encode.
	MarshalFailed = corebson.MarshalFailed
	// UnmarshalFailed marks input Unmarshal refuses.
	UnmarshalFailed = corebson.UnmarshalFailed
	// SizeExceeded marks an Unmarshal input past 10 MiB.
	SizeExceeded = corebson.SizeExceeded
	// DepthExceeded marks nesting past 100 levels.
	DepthExceeded = corebson.DepthExceeded
	// ValueInvalid marks a value type's constructor or parser refusing its input.
	ValueInvalid = corebson.ValueInvalid
	// NilObjectID is the zero ObjectID.
	NilObjectID ObjectID
)

// Marshal encodes v, which must encode as a document, into a slice the caller
// owns.
func Marshal(v any) ([]byte, error) {
	//: the registered codec's own encoder.
	return svcbson.New().Marshal(v)
}

// Append encodes v and appends the document to dst; a dst with room
// allocates nothing. On failure dst is returned with its original length.
func Append(dst []byte, v any) ([]byte, error) {
	a, ok := svcbson.New().(interface {
		Append(dst []byte, v any) ([]byte, error)
	})
	//: the codec has always implemented the Appender extension.
	if !ok {
		//: unreachable; refused rather than assumed.
		return dst, MarshalFailed
	}
	//: the encoder writes straight into dst.
	return a.Append(dst, v)
}

// Unmarshal decodes one BSON document into v, a non-nil pointer or map. The
// input is checked whole before v is written.
func Unmarshal(data []byte, v any) error {
	//: the registered codec's own decoder.
	return svcbson.New().Unmarshal(data, v)
}
