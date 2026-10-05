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
