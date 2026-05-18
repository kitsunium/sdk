// Package codec — declares the sentinel *errs.Error values the
// facade emits when dispatch fails.
package codec

import "github.com/kitsunium/sdk/internal/kernel/errs"

var (
	// UnknownFormat is returned when Marshal / Unmarshal / NewEncoder /
	// NewDecoder receives a Format that no codec has registered.
	UnknownFormat = errs.Define(CodeUnknownFormat, "UNKNOWN_FORMAT",
		"codec format is not registered",
		"pkg/v1/codec: Lookup returned false for the requested Format")

	// CodecUnavailable is returned when the registry entry exists but the
	// codec itself cannot be used (reserved for future gating).
	CodecUnavailable = errs.Define(CodeCodecUnavailable, "CODEC_UNAVAILABLE",
		"codec is registered but unavailable",
		"pkg/v1/codec: registered codec cannot serve the request")

	// StreamingUnsupported is returned when NewEncoder / NewDecoder is
	// called on a codec that does not implement StreamingCodec.
	StreamingUnsupported = errs.Define(CodeStreamingUnsupported, "STREAMING_UNSUPPORTED",
		"codec does not support streaming",
		"pkg/v1/codec: target codec is not a core/codec.StreamingCodec")
)
