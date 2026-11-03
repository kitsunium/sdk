package xml

import (
	stdxml "encoding/xml"
	"io"
	"slices"

	"github.com/kitsunium/sdk/internal/core/data/codec"
	"github.com/kitsunium/sdk/internal/core/data/codec/scratch"
	corexml "github.com/kitsunium/sdk/internal/core/data/codec/xml"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// The registered singleton. Its aliases are literals in MIMETypes and
// Extensions, not package tables: Register reads them through the Codec
// interface, which orders no table before this initialiser (see
// core/data/codec.Register).
var (
	//: register the singleton and expose it as a typed package var.
	Codec codec.Codec = codec.Register(&xmlCodec{})
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
	//: the canonical IETF-registered type is application/xml.
	//: a fresh literal the caller owns; no package variable is read when Register calls this.
	return []string{"application/xml", "text/xml"}
}

// Extensions lists every file extension.
func (*xmlCodec) Extensions() []string {
	//: canonical extension.
	//: a fresh literal the caller owns; no package variable is read when Register calls this.
	return []string{".xml"}
}

// Marshal serialises v as XML bytes. Routes through a pooled
// *bytes.Buffer + xml.NewEncoder so the per-call buffer allocation
// stdxml.Marshal pays internally is amortised across calls.
func (*xmlCodec) Marshal(v any) (encoded []byte, err error) {
	//: a channel, a function, an unsafe pointer: no wire format holds one (#1101).
	if verr := codec.CheckValue(v); verr != nil {
		return nil, verr
	}
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
			Code:    corexml.CodeXMLMarshalFailed,
			Reason:  "MARSHAL_FAILED",
			Public:  "XML encoding failed",
			Private: "service/data/codec/xml.Marshal: encoding/xml returned an error",
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
			Code:    corexml.CodeXMLMarshalFailed,
			Reason:  "MARSHAL_FAILED",
			Public:  "XML encoding failed",
			Private: "service/data/codec/xml.Marshal: encoder.Close returned an error",
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
	//: a nil or non-pointer target is the caller's error, refused first (#1101).
	if terr := codec.CheckTarget(v); terr != nil {
		return terr
	}
	//: no bytes is no document in this format (#1101).
	if len(data) == 0 {
		return codec.EmptyInput
	}
	//: delegate.
	xerr := stdxml.Unmarshal(data, v)
	//: success fast-path.
	if xerr == nil {
		//: nothing to wrap.
		return nil
	}
	//: wrap.
	return errs.Wrap(xerr, errs.WrapParams{
		Code:    corexml.CodeXMLUnmarshalFailed,
		Reason:  "UNMARSHAL_FAILED",
		Public:  "XML decoding failed",
		Private: "service/data/codec/xml.Unmarshal: encoding/xml returned an error",
	}, position(xerr)...)
}

// Append encodes v as XML and appends the bytes to dst. Implements the
// optional codec.Appender interface so hot-path callers can stream
// records into a recycled buffer. The encoder writes through a pooled
// scratch.AppendWriter over dst, so the bytes land in the caller's buffer
// with no intermediate copy; on failure dst is returned as it was given.
func (*xmlCodec) Append(dst []byte, v any) (appended []byte, err error) {
	//: a pooled writer appending to dst.
	w := scratch.AcquireAppendWriter(dst)
	//: stdxml.Encoder has no Reset(w) — fresh one per call.
	enc := stdxml.NewEncoder(w)
	//: encode, then flush trailing tokens — same Close semantics as Marshal.
	xerr, private := enc.Encode(v), "service/data/codec/xml.Append: encoding/xml returned an error"
	//: Close only after a successful Encode, as before.
	if xerr == nil {
		//: a close failure is a marshal failure too.
		xerr, private = enc.Close(), "service/data/codec/xml.Append: encoder.Close returned an error"
	}
	//: the grown slice, read before the writer goes back to the pool.
	out := w.Bytes()
	//: the writer forgets the slice; out stays the caller's.
	scratch.ReleaseAppendWriter(w)
	//: failure — dst as it was given, and the wrapped error.
	if xerr != nil {
		//: wrap the library error for reason-based matching.
		return dst, errs.Wrap(xerr, errs.WrapParams{
			Code:    corexml.CodeXMLMarshalFailed,
			Reason:  "MARSHAL_FAILED",
			Public:  "XML encoding failed",
			Private: private,
		})
	}
	//: success — bytes are the caller's now.
	return out, nil
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
