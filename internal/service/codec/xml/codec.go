// Package xml wraps encoding/xml as a codec.Codec implementation registered
// under Format("xml"). Blank-importing this package is enough to make XML
// resolvable via the core/codec registry.
package xml

import (
	stdxml "encoding/xml"
	"io"
	"slices"

	"github.com/kitsunium/sdk/internal/core/codec"
	"github.com/kitsunium/sdk/internal/core/codec/scratch"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// Package-level state: the codec singleton plus the hoisted MIME /
// extension tables.
var (
	//: register the singleton and expose it as a typed package var.
	Codec codec.Codec = codec.Register(&xmlCodec{})

	//: the canonical IETF-registered type is application/xml.
	mimeTypes = []string{"application/xml", "text/xml"}

	//: canonical extension.
	extensions = []string{".xml"}
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
	//: rent an already-Reset buffer from the shared codec pool.
	buf := scratch.AcquireBuffer()
	//: encode through the stdlib encoder so xml.Header semantics + the
	//: marshaller dispatch stay byte-identical to xml.Marshal.
	enc := stdxml.NewEncoder(buf)
	//: encode then flush so we capture every byte before returning.
	if xerr := enc.Encode(v); xerr != nil {
		//: drop the buffer back to the pool if not oversized.
		scratch.ReleaseBuffer(buf)
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
		scratch.ReleaseBuffer(buf)
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
	scratch.ReleaseBuffer(buf)
	//: success — bytes are the caller's now.
	return out, nil
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
// records into a recycled buffer. Encodes directly into the pooled
// *bytes.Buffer + appends onto dst — saves the slices.Clone the
// Marshal-delegation shape paid.
func (*xmlCodec) Append(dst []byte, v any) (appended []byte, err error) {
	//: rent an already-Reset buffer from the shared codec pool.
	buf := scratch.AcquireBuffer()
	//: stdxml.Encoder has no Reset(w) — fresh one per call.
	enc := stdxml.NewEncoder(buf)
	//: encode into the pooled buffer.
	if xerr := enc.Encode(v); xerr != nil {
		//: cap-discard release; dst stays pristine.
		scratch.ReleaseBuffer(buf)
		//: wrap the library error for reason-based matching.
		return dst, errs.Wrap(xerr, errs.WrapParams{
			Code:    CodeXMLMarshalFailed,
			Reason:  "MARSHAL_FAILED",
			Public:  "XML encoding failed",
			Private: "service/codec/xml.Append: encoding/xml returned an error",
		})
	}
	//: flush trailing tokens; same Close semantics as Marshal.
	if cerr := enc.Close(); cerr != nil {
		//: cap-discard release; dst stays pristine on close failure too.
		scratch.ReleaseBuffer(buf)
		//: surface the close failure as a marshal error.
		return dst, errs.Wrap(cerr, errs.WrapParams{
			Code:    CodeXMLMarshalFailed,
			Reason:  "MARSHAL_FAILED",
			Public:  "XML encoding failed",
			Private: "service/codec/xml.Append: encoder.Close returned an error",
		})
	}
	//: append the encoded bytes onto the caller's buffer (1 copy total).
	dst = append(dst, buf.Bytes()...)
	//: cap-discard release.
	scratch.ReleaseBuffer(buf)
	//: success — bytes are the caller's now.
	return dst, nil
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
