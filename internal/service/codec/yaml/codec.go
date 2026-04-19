// Package yaml wraps gopkg.in/yaml.v3 as a codec.Codec implementation.
// Streaming is supported: yaml.v3 exposes Encoder/Decoder types that can
// serialise a sequence of documents separated by `---` markers.
package yaml

import (
	"io"
	"slices"

	goyaml "gopkg.in/yaml.v3"

	"github.com/kitsunium/sdk/internal/core/codec"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// Package-level state: registers the YAML codec at package load time.
var (
	_ = codec.Register(&yamlCodec{})

	//: MIME table hoisted to satisfy KTN-VAR-CONSTSLICE.
	mimeTypes = []string{"application/yaml", "text/yaml", "application/x-yaml"}

	//: extension table hoisted for the same reason.
	extensions = []string{".yaml", ".yml"}
)

// yamlCodec is the concrete Codec implementation for YAML.
type yamlCodec struct{}

// New returns a YAML codec instance.
//
// Returns:
//   - codec.Codec: a fresh stateless codec.
func New() (c codec.Codec) {
	//: stateless singleton.
	return &yamlCodec{}
}

// Name implements codec.Codec.
//
// Returns:
//   - string: always "yaml".
func (*yamlCodec) Name() (name string) {
	//: canonical identifier.
	return "yaml"
}

// MIMETypes lists every MIME alias.
//
// Returns:
//   - []string: canonical MIME first.
func (*yamlCodec) MIMETypes() (mimes []string) {
	//: hand back the package-level slice.
	return slices.Clone(mimeTypes)
}

// Extensions lists every file extension.
//
// Returns:
//   - []string: canonical extension first.
func (*yamlCodec) Extensions() (exts []string) {
	//: hand back the package-level slice.
	return slices.Clone(extensions)
}

// Marshal serialises v as YAML bytes.
//
// Params:
//   - v: value yaml.v3 supports (struct, map, slice, primitive).
//
// Returns:
//   - []byte: the encoded YAML document.
//   - error: MarshalFailed wrapping the yaml.v3 cause on failure.
func (*yamlCodec) Marshal(v any) (data []byte, err error) {
	//: delegate to yaml.v3 for the actual encoding.
	out, merr := goyaml.Marshal(v)
	//: success fast-path.
	if merr == nil {
		//: return the encoded bytes verbatim.
		return out, nil
	}
	//: wrap the library error for reason-based matching.
	return nil, errs.Wrap(merr, errs.WrapParams{
		Code:    CodeYAMLMarshalFailed,
		Reason:  "MARSHAL_FAILED",
		Public:  "YAML encoding failed",
		Private: "service/codec/yaml.Marshal: gopkg.in/yaml.v3 returned an error",
	})
}

// Unmarshal parses data as YAML into v.
//
// Params:
//   - data: YAML bytes.
//   - v: pointer to the destination value.
//
// Returns:
//   - error: UnmarshalFailed wrapping the yaml.v3 cause on failure.
func (*yamlCodec) Unmarshal(data []byte, v any) (err error) {
	//: delegate to yaml.v3 for the actual decoding.
	uerr := goyaml.Unmarshal(data, v)
	//: success fast-path.
	if uerr == nil {
		//: nothing to wrap.
		return nil
	}
	//: wrap the library error.
	return errs.Wrap(uerr, errs.WrapParams{
		Code:    CodeYAMLUnmarshalFailed,
		Reason:  "UNMARSHAL_FAILED",
		Public:  "YAML decoding failed",
		Private: "service/codec/yaml.Unmarshal: gopkg.in/yaml.v3 returned an error",
	})
}

// NewEncoder wraps w in a streaming codec.Encoder.
//
// Params:
//   - w: destination writer.
//
// Returns:
//   - codec.Encoder: a streaming YAML encoder bound to w.
func (*yamlCodec) NewEncoder(w io.Writer) (enc codec.Encoder) {
	//: wrap the yaml.v3 encoder to expose our Close contract.
	return &yamlEncoder{inner: goyaml.NewEncoder(w)}
}

// NewDecoder wraps r in a streaming codec.Decoder.
//
// Params:
//   - r: source reader.
//
// Returns:
//   - codec.Decoder: a streaming YAML decoder bound to r.
func (*yamlCodec) NewDecoder(r io.Reader) (dec codec.Decoder) {
	//: wrap the yaml.v3 decoder to expose More on our interface.
	return &yamlDecoder{inner: goyaml.NewDecoder(r)}
}
