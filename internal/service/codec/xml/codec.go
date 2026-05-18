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

// Package-level state: the codec singleton plus the hoisted MIME /
// extension tables (hoisted).
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

// Marshal serialises v as XML bytes.
func (*xmlCodec) Marshal(v any) (encoded []byte, err error) {
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
