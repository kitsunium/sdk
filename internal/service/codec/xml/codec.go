// Package xml wraps encoding/xml as a codec.Codec implementation registered
// under Format("xml"). Blank-importing this package is enough to make XML
// resolvable via the core/codec registry.
package xml

import (
	stdxml "encoding/xml"
	"io"
	"slices"

	"github.com/kitsunium/sdk/internal/core/codec"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// Codec is the XML singleton, registered with core/codec at package load.
// Binding the registration result to a named var is more idiomatic than
// `var _ = codec.Register(...)` and keeps us clear of init() (KTN-FUNC-NOINIT).
var Codec codec.Codec = codec.Register(&xmlCodec{})

// Package-level lookup tables, hoisted to satisfy KTN-VAR-CONSTSLICE.
var (
	//: the canonical IETF-registered type is application/xml.
	mimeTypes = []string{"application/xml", "text/xml"}

	//: canonical extension.
	extensions = []string{".xml"}
)

// xmlCodec is the concrete Codec implementation for XML.
type xmlCodec struct{}

// New returns an XML codec instance.
//
// Returns:
//   - codec.Codec: a fresh stateless codec.
func New() (c codec.Codec) {
	//: stateless — one singleton is enough for the whole process.
	return Codec
}

// Name implements codec.Codec.
//
// Returns:
//   - string: always "xml".
func (*xmlCodec) Name() (name string) {
	//: canonical identifier.
	return "xml"
}

// MIMETypes lists every MIME alias.
//
// Returns:
//   - []string: canonical MIME first.
func (*xmlCodec) MIMETypes() (mimes []string) {
	//: return a copy so callers cannot mutate the shared slice.
	return slices.Clone(mimeTypes)
}

// Extensions lists every file extension.
//
// Returns:
//   - []string: canonical extension first.
func (*xmlCodec) Extensions() (exts []string) {
	//: return a copy so callers cannot mutate the shared slice.
	return slices.Clone(extensions)
}

// Marshal serialises v as XML bytes.
//
// Params:
//   - v: value encoding/xml supports.
//
// Returns:
//   - []byte: encoded XML.
//   - error: MarshalFailed wrapping the stdlib cause on failure.
func (*xmlCodec) Marshal(v any) (data []byte, err error) {
	//: delegate.
	out, xerr := stdxml.Marshal(v)
	//: success fast-path.
	if xerr == nil {
		//: return the encoded bytes verbatim.
		return out, nil
	}
	//: wrap for reason-based matching.
	return nil, errs.Wrap(xerr, errs.WrapParams{
		Code:    CodeXMLMarshalFailed,
		Reason:  "MARSHAL_FAILED",
		Public:  "XML encoding failed",
		Private: "service/codec/xml.Marshal: encoding/xml returned an error",
	})
}

// Unmarshal parses data as XML into v.
//
// Params:
//   - data: XML bytes.
//   - v: pointer to the destination value.
//
// Returns:
//   - error: UnmarshalFailed wrapping the stdlib cause on failure.
func (*xmlCodec) Unmarshal(data []byte, v any) (err error) {
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

// NewEncoder wraps w in a streaming codec.Encoder.
//
// Params:
//   - w: destination writer.
//
// Returns:
//   - codec.Encoder: a streaming XML encoder bound to w.
func (*xmlCodec) NewEncoder(w io.Writer) (enc codec.Encoder) {
	//: wrap the stdlib encoder.
	return &xmlEncoder{inner: stdxml.NewEncoder(w)}
}

// NewDecoder wraps r in a streaming codec.Decoder.
//
// Params:
//   - r: source reader.
//
// Returns:
//   - codec.Decoder: a streaming XML decoder bound to r.
func (*xmlCodec) NewDecoder(r io.Reader) (dec codec.Decoder) {
	//: wrap the stdlib decoder.
	return &xmlDecoder{inner: stdxml.NewDecoder(r)}
}
