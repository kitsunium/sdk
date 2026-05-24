// Package toml wraps github.com/pelletier/go-toml/v2 as a codec.Codec
// implementation. The upstream library offers streaming Encoder/Decoder
// pairs, so this codec implements StreamingCodec as well.
package toml

import (
	"bytes"
	"io"
	"slices"
	"sync"

	gotoml "github.com/pelletier/go-toml/v2"

	"github.com/kitsunium/sdk/internal/core/codec"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// maxRetainedBufBytes caps the size of *bytes.Buffer instances re-pooled
// by Marshal / Append. A one-off oversized payload would otherwise pin
// a large buffer for the lifetime of the pool's GC window. 256 KiB is
// the project-wide threshold (codec-perf-extreme initiative).
const maxRetainedBufBytes int = 256 << 10

// Package-level state: the codec singleton plus the hoisted MIME /
// extension tables + the Marshal-side buffer pool.
var (
	//: register the singleton and expose it as a typed package var.
	Codec codec.Codec = codec.Register(&tomlCodec{})

	//: MIME table hoisted.
	mimeTypes = []string{"application/toml"}

	//: extension table hoisted for the same reason.
	extensions = []string{".toml"}

	//: bufferPool reuses *bytes.Buffer across Marshal / Append calls.
	//: pelletier/go-toml/v2 has no Encoder.Reset(w); pool only the
	//: buffer and spin up a fresh Encoder per call.
	bufferPool = sync.Pool{
		New: func() any { return new(bytes.Buffer) },
	}
)

// tomlCodec is the concrete Codec implementation for TOML.
type tomlCodec struct{}

// New returns a TOML codec instance.
func New() codec.Codec {
	//: stateless — one singleton is enough for the whole process.
	return Codec
}

// Name implements codec.Codec.
func (*tomlCodec) Name() string {
	//: canonical identifier.
	return "toml"
}

// MIMETypes lists every MIME alias.
func (*tomlCodec) MIMETypes() []string {
	//: hand back the package-level slice.
	return slices.Clone(mimeTypes)
}

// Extensions lists every file extension.
func (*tomlCodec) Extensions() []string {
	//: hand back the package-level slice.
	return slices.Clone(extensions)
}

// Marshal serialises v as TOML bytes. Routes through a pooled
// *bytes.Buffer + gotoml.NewEncoder so the per-call bytes.Buffer
// allocation gotoml.Marshal pays internally is amortised across calls.
func (*tomlCodec) Marshal(v any) (encoded []byte, err error) {
	//: rent the output buffer; pool guarantees a *bytes.Buffer.
	buf, ok := bufferPool.Get().(*bytes.Buffer)
	//: pool invariant guard — never expected to fail at runtime.
	if !ok {
		//: invariant broken — fail loud at the call site.
		panic("service/codec/toml: bufferPool yielded non-*bytes.Buffer")
	}
	//: start clean — pool may return a partially-filled buffer.
	buf.Reset()
	//: pelletier Encoder has no Reset — fresh one per call.
	enc := gotoml.NewEncoder(buf)
	//: encode into the pooled buffer.
	if merr := enc.Encode(v); merr != nil {
		//: drop the buffer back to the pool if not oversized.
		releaseBuffer(buf)
		//: wrap the library error for reason-based matching.
		return nil, errs.Wrap(merr, errs.WrapParams{
			Code:    CodeTOMLMarshalFailed,
			Reason:  "MARSHAL_FAILED",
			Public:  "TOML encoding failed",
			Private: "service/codec/toml.Marshal: pelletier/go-toml/v2 returned an error",
		})
	}
	//: detach: clone the buffer's bytes so the returned slice does not
	//: alias the pooled buffer (next caller would overwrite it).
	out := slices.Clone(buf.Bytes())
	//: cap-discard release.
	releaseBuffer(buf)
	//: success — bytes are the caller's now.
	return out, nil
}

// releaseBuffer returns buf to the pool unless its capacity exceeds the
// cap-discard threshold.
func releaseBuffer(buf *bytes.Buffer) {
	//: oversized buffers would pin large allocations for the lifetime
	//: of the pool's GC window — drop them instead.
	if buf.Cap() > maxRetainedBufBytes {
		//: orphan the buffer; the GC will reclaim it.
		return
	}
	//: pool expects a clean buffer.
	buf.Reset()
	//: return for the next caller.
	bufferPool.Put(buf)
}

// Unmarshal parses data as TOML into v.
func (*tomlCodec) Unmarshal(data []byte, v any) error {
	//: delegate to the library for the actual decoding.
	uerr := gotoml.Unmarshal(data, v)
	//: success fast-path.
	if uerr == nil {
		//: nothing to wrap.
		return nil
	}
	//: wrap the library error.
	return errs.Wrap(uerr, errs.WrapParams{
		Code:    CodeTOMLUnmarshalFailed,
		Reason:  "UNMARSHAL_FAILED",
		Public:  "TOML decoding failed",
		Private: "service/codec/toml.Unmarshal: pelletier/go-toml/v2 returned an error",
	})
}

// Append encodes v as TOML and appends the bytes to dst. Implements the
// optional codec.Appender interface so hot-path callers can stream
// records into a recycled buffer.
func (c *tomlCodec) Append(dst []byte, v any) (appended []byte, err error) {
	//: delegate to Marshal so the wrap/error contract has a single source.
	encoded, merr := c.Marshal(v)
	//: surface any encoding failure without touching dst.
	if merr != nil {
		//: return the untouched buffer plus the wrapped error.
		return dst, merr
	}
	//: append the encoded bytes onto the caller's buffer.
	return append(dst, encoded...), nil
}

// NewEncoder wraps w in a streaming codec.Encoder.
func (*tomlCodec) NewEncoder(w io.Writer) codec.Encoder {
	//: wrap the pelletier encoder.
	return &tomlEncoder{inner: gotoml.NewEncoder(w)}
}

// NewDecoder wraps r in a streaming codec.Decoder.
func (*tomlCodec) NewDecoder(r io.Reader) codec.Decoder {
	//: wrap the pelletier decoder.
	return &tomlDecoder{inner: gotoml.NewDecoder(r)}
}
