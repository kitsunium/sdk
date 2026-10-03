// Package msgpack — compile-time proof that every type here satisfies the
// contract it is handed out as, and that time.Time is the IsZero type the
// omitempty rule consults first.
package msgpack

import (
	"time"

	"github.com/kitsunium/sdk/internal/core/data/codec"
)

// Compile-time conformance.
var (
	// the codec is a StreamingCodec and an Appender.
	_ codec.StreamingCodec = (*msgpackCodec)(nil)
	// the codec appends onto a caller's buffer.
	_ codec.Appender = (*msgpackCodec)(nil)
	// the streaming encoder.
	_ codec.Encoder = (*msgpackEncoder)(nil)
	// the streaming decoder.
	_ codec.Decoder = (*msgpackDecoder)(nil)
	// what a string decodes to in an error-typed target.
	_ error = (*decodedError)(nil)
	// time.Time is the IsZero type omitempty meets most: a zero time is left out.
	_ isZeroer = time.Time{}
)
