// Package xml wraps encoding/xml as a codec.Codec implementation registered
// under Format("xml"). Blank-importing this package is enough to make XML
// resolvable via the core/codec registry.
package xml

import (
	"bytes"
	stdxml "encoding/xml"
	"io"
	"slices"
	"sync"

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
	Codec codec.Codec = codec.Register(&xmlCodec{})

	//: the canonical IETF-registered type is application/xml.
	mimeTypes = []string{"application/xml", "text/xml"}

	//: canonical extension.
	extensions = []string{".xml"}

	//: bufferPool reuses *bytes.Buffer across Marshal / Append calls.
	//: stdlib encoding/xml.Marshal allocates a fresh bytes.Buffer per
	//: call — pooling at our layer eliminates that allocation +
	//: amortises the geometric grow cascade across consecutive calls.
	bufferPool = sync.Pool{
		New: func() any { return new(bytes.Buffer) },
	}
)

// xmlCodec is the concrete Codec implementation for XML.
type xmlCodec struct{}

// New returns an XML codec instance.
func New() codec.Codec {
	//: stateless — one singleton is enough for the whole process.
	return Codec
}

// Name implements codec.Codec.
func (*xmlCodec) Name() string {
	//: canonical identifier.
	return "xml"
}

// MIMETypes lists every MIME alias.
func (*xmlCodec) MIMETypes() []string {
	//: return a copy so callers cannot mutate the shared slice.
	return slices.Clone(mimeTypes)
}

// Extensions lists every file extension.
func (*xmlCodec) Extensions() []string {
	//: return a copy so callers cannot mutate the shared slice.
	return slices.Clone(extensions)
}

// Marshal serialises v as XML bytes. Routes through a pooled
// *bytes.Buffer + xml.NewEncoder so the per-call buffer allocation
// stdxml.Marshal pays internally is amortised across calls.
func (*xmlCodec) Marshal(v any) (encoded []byte, err error) {
	//: rent the output buffer; pool guarantees a *bytes.Buffer.
	buf, ok := bufferPool.Get().(*bytes.Buffer)
	//: pool invariant guard — never expected to fail at runtime.
	if !ok {
		//: invariant broken — fail loud at the call site.
		panic("service/codec/xml: bufferPool yielded non-*bytes.Buffer")
	}
	//: start clean — pool may return a partially-filled buffer.
	buf.Reset()
	//: encode through the stdlib encoder so xml.Header semantics + the
	//: marshaller dispatch stay byte-identical to xml.Marshal.
	enc := stdxml.NewEncoder(buf)
	//: encode then flush so we capture every byte before returning.
	if xerr := enc.Encode(v); xerr != nil {
		//: drop the buffer back to the pool if not oversized.
		releaseBuffer(buf)
		//: wrap for reason-based matching.
		return nil, errs.Wrap(xerr, errs.WrapParams{
			Code:    CodeXMLMarshalFailed,
			Reason:  "MARSHAL_FAILED",
			Public:  "XML encoding failed",
			Private: "service/codec/xml.Marshal: encoding/xml returned an error",
		})
	}
	//: stdlib Encoder buffers internally; explicit Close flushes any
	//: trailing tokens. Close cannot fail after a successful Encode on
	//: a *bytes.Buffer writer (no I/O) but check defensively.
	if cerr := enc.Close(); cerr != nil {
		//: drop the buffer back to the pool if not oversized.
		releaseBuffer(buf)
		//: surface the close failure as a marshal error.
		return nil, errs.Wrap(cerr, errs.WrapParams{
			Code:    CodeXMLMarshalFailed,
			Reason:  "MARSHAL_FAILED",
			Public:  "XML encoding failed",
			Private: "service/codec/xml.Marshal: encoder.Close returned an error",
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

// Unmarshal parses data as XML into v.
func (*xmlCodec) Unmarshal(data []byte, v any) error {
	//: delegate.
	xerr := stdxml.Unmarshal(data, v)
	//: success fast-path.
	if xerr == nil {
		//: nothing to wrap.
		return nil
	}
	//: wrap.
	return errs.Wrap(xerr, errs.WrapParams{
		Code:    CodeXMLUnmarshalFailed,
		Reason:  "UNMARSHAL_FAILED",
		Public:  "XML decoding failed",
		Private: "service/codec/xml.Unmarshal: encoding/xml returned an error",
	})
}

// Append encodes v as XML and appends the bytes to dst. Implements the
// optional codec.Appender interface so hot-path callers can stream
// records into a recycled buffer.
func (c *xmlCodec) Append(dst []byte, v any) (appended []byte, err error) {
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
func (*xmlCodec) NewEncoder(w io.Writer) codec.Encoder {
	//: wrap the stdlib encoder.
	return &xmlEncoder{inner: stdxml.NewEncoder(w)}
}

// NewDecoder wraps r in a streaming codec.Decoder.
func (*xmlCodec) NewDecoder(r io.Reader) codec.Decoder {
	//: wrap the stdlib decoder.
	return &xmlDecoder{inner: stdxml.NewDecoder(r)}
}
