package protobuf

import "github.com/kitsunium/sdk/internal/core/data/codec"

// Compile-time proof that the concrete types satisfy the contracts this
// package advertises — the streaming pair whose signatures take any because
// codec.Encoder and codec.Decoder do (#905).
var (
	// _ asserts the encoder is a codec.Encoder.
	_ codec.Encoder = (*protobufEncoder)(nil)
	// _ asserts the decoder is a codec.Decoder.
	_ codec.Decoder = (*protobufDecoder)(nil)
	// _ asserts the codec streams.
	_ codec.StreamingCodec = (*protobufCodec)(nil)
)
