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

// maxYAMLBytes caps the byte size Unmarshal accepts from untrusted input.
// gopkg.in/yaml.v3 caps alias-expansion internally (since v3.0.0), but a
// single very large YAML document still forces the whole buffer into
// memory before any structural check. 10 MiB comfortably covers every
// realistic configuration document while preventing memory-exhaustion
// DoS on attacker-controlled payloads (CWE-400 / CWE-776). Streaming
// callers that legitimately need larger inputs use NewDecoder with
// their own io.LimitReader sizing.
const maxYAMLBytes int = 10 << 20

// Package-level state: the codec singleton plus the hoisted MIME /
// extension tables (hoisted).
var (
	//: register the singleton and expose it as a typed package var.
	Codec codec.Codec = codec.Register(&yamlCodec{})

	//: MIME table hoisted.
	mimeTypes = []string{"application/yaml", "text/yaml", "application/x-yaml"}

	//: extension table hoisted for the same reason.
	extensions = []string{".yaml", ".yml"}
)

// yamlCodec is the concrete Codec implementation for YAML.
type yamlCodec struct{}

// New returns a YAML codec instance.
func New() codec.Codec {
	//: stateless — one singleton is enough for the whole process.
	return Codec
}

// Name implements codec.Codec.
func (*yamlCodec) Name() string {
	//: canonical identifier.
	return "yaml"
}

// MIMETypes lists every MIME alias.
func (*yamlCodec) MIMETypes() []string {
	//: hand back the package-level slice.
	return slices.Clone(mimeTypes)
}

// Extensions lists every file extension.
func (*yamlCodec) Extensions() []string {
	//: hand back the package-level slice.
	return slices.Clone(extensions)
}

// Marshal serialises v as YAML bytes.
func (*yamlCodec) Marshal(v any) (encoded []byte, err error) {
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
func (*yamlCodec) Unmarshal(data []byte, v any) error {
	//: cap input size so attacker-controlled payloads cannot exhaust RAM; yaml.v3 caps alias expansion internally but has no upstream byte
	//: budget, so 10 MiB is the project-wide safe default.
	if len(data) > maxYAMLBytes {
		//: surface an UNMARSHAL_FAILED with a diagnostic Private message.
		return errs.Wrap(nil, errs.WrapParams{
			Code:    CodeYAMLUnmarshalFailed,
			Reason:  "UNMARSHAL_FAILED",
			Public:  "YAML input exceeds size limit",
			Private: "service/codec/yaml.Unmarshal: len(data) exceeds maxYAMLBytes",
		}, errs.Int("len", len(data)), errs.Int("cap", maxYAMLBytes))
	}
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
func (*yamlCodec) NewEncoder(w io.Writer) codec.Encoder {
	//: wrap the yaml.v3 encoder to expose our Close contract.
	return &yamlEncoder{inner: goyaml.NewEncoder(w)}
}

// NewDecoder wraps r in a streaming codec.Decoder.
func (*yamlCodec) NewDecoder(r io.Reader) codec.Decoder {
	//: wrap the yaml.v3 decoder to expose More on our interface.
	return &yamlDecoder{inner: goyaml.NewDecoder(r)}
}
