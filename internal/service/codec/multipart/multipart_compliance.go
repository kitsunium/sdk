// Package multipart — compile-time proof that the concrete types satisfy the
// contracts this package advertises. Kept in its own *_compliance.go file so
// the assertions live where a reader looks for them and nowhere else.
package multipart

import "github.com/kitsunium/sdk/internal/core/codec"

// The registered codec satisfies the base contract plus the two optional
// extensions consumers assert on, and the encoder exposes the delimiter a
// streaming caller needs for its Content-Type header.
var (
	_ codec.Codec          = (*multipartCodec)(nil)
	_ codec.StreamingCodec = (*multipartCodec)(nil)
	_ codec.Appender       = (*multipartCodec)(nil)
	_ BoundaryCodec        = (*multipartCodec)(nil)
	_ codec.Encoder        = (*multipartEncoder)(nil)
	_ codec.Decoder        = (*multipartDecoder)(nil)
	_ BoundaryProvider     = (*multipartEncoder)(nil)
)
