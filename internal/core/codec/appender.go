// Package codec: appender.go declares the optional Appender extension that
// hot-path encoders (NDJSON, JSON, text) implement so callers can write into
// a caller-supplied buffer without paying for an intermediate allocation.
//
// Codecs that satisfy Codec but do NOT implement Appender remain valid; the
// logger and other consumers detect support with a runtime type assertion
// and fall back to Marshal + copy when the optional interface is absent.
package codec

// Appender is the optional extension implemented by codecs that can encode
// directly into a caller-supplied byte slice instead of allocating their own.
// Hot paths (logger record formatting, NDJSON streams, batch sinks) consume
// this interface to avoid the buffer-copy cost imposed by Marshal.
//
// Implementations MUST:
//   - Append the encoded representation of v onto dst (typically via the
//     standard append(dst, …) idiom).
//   - Return the (possibly re-allocated) buffer back to the caller.
//   - Be safe for concurrent use by multiple goroutines.
//
// The returned buffer's length grows by the number of bytes written; the
// underlying array MAY be a fresh allocation when dst's capacity is too
// small. Callers retain ownership of dst and SHOULD reuse it across calls.
type Appender interface {
	Codec
	// Append encodes v and appends the bytes onto dst. The returned slice
	// MUST contain the prior contents of dst followed by the encoded form.
	//
	// Params:
	//   - dst: caller-supplied buffer; encoded bytes are appended onto it.
	//   - v: value to encode; the codec's accepted shape is format-specific.
	//
	// Returns:
	//   - []byte: the (possibly re-allocated) buffer with encoded bytes.
	//   - error: format-specific failure; nil on success.
	Append(dst []byte, v any) (out []byte, err error)
}
