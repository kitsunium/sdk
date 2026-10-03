// Package yaml wraps gopkg.in/yaml.v3 as a codec.Codec implementation
// registered under the Format "yaml-full". It is the whole of YAML 1.2 as
// yaml.v3 reads it — anchors and aliases, tags, merge keys, complex keys,
// multi-document streams — for the program that needs a construct the SDK's
// own codec refuses by name.
//
// It is a module of its own under third-party/ (ADR 0157), NOT
// internal/service/data/codec, because the SDK's own "yaml" Format is a native,
// standard-library-only reader of a named subset (internal/service/data/codec/yaml)
// and the public module stays free of gopkg.in/yaml.v3. It is opt-in: a consumer blank-imports this package
// to register "yaml-full"; pkg/v1/data/codec does NOT pull it.
//
// It claims NO MIME type and NO file extension. ".yaml", ".yml" and
// application/yaml stay the native codec's, whatever else a program imports, so
// importing this package never changes what an extension lookup returns; a
// caller reaches the full reader by naming "yaml-full".
//
// Streaming is supported: yaml.v3 exposes Encoder/Decoder types that serialise
// a sequence of documents separated by `---` markers.
package yaml

import (
	"io"
	"slices"

	goyaml "gopkg.in/yaml.v3"

	"github.com/kitsunium/sdk/internal/core/data/codec"
	"github.com/kitsunium/sdk/internal/core/data/codec/scratch"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// Format is the name this codec is registered under. It differs from the
// native codec's "yaml" because the registry refuses a second codec under one
// name, and because the two are not the same reader.
const Format string = "yaml-full"

// maxYAMLBytes caps the byte size Unmarshal accepts from untrusted input.
// gopkg.in/yaml.v3 caps alias-expansion internally (since v3.0.0), but a
// single very large YAML document still forces the whole buffer into
// memory before any structural check. 10 MiB comfortably covers every
// realistic configuration document while preventing memory-exhaustion
// DoS on attacker-controlled payloads (CWE-400 / CWE-776). Streaming
// callers that legitimately need larger inputs use NewDecoder with
// their own io.LimitReader sizing.
const maxYAMLBytes int = 10 << 20

// yamlEncoderIndent is the number of spaces yaml.v3 inserts per
// nesting level on the wire. yaml.v3 defaults to 4; the YAML spec
// accepts any value ≥ 1 so 2 is wire-compatible with every decoder
// and shrinks output by ~50% on deeply-nested configs.
const yamlEncoderIndent int = 2

// Codec is the registered full-YAML singleton.
var Codec codec.Codec = codec.Register(&yamlCodec{})

// yamlCodec is the concrete Codec implementation for YAML through yaml.v3.
type yamlCodec struct{}

// New returns the full-YAML codec singleton.
func New() codec.Codec {
	//: stateless — one singleton is enough for the whole process.
	return Codec
}

// Name implements codec.Codec.
func (*yamlCodec) Name() string {
	//: canonical identifier, distinct from the native codec's.
	return Format
}

// MIMETypes returns no MIME type: application/yaml resolves to the native
// codec, whatever else is imported.
func (*yamlCodec) MIMETypes() []string {
	//: nothing to claim.
	return nil
}

// Extensions returns no extension: .yaml and .yml resolve to the native codec,
// whatever else is imported.
func (*yamlCodec) Extensions() []string {
	//: nothing to claim.
	return nil
}

// marshalFailed wraps a yaml.v3 encode error under this package's code.
func marshalFailed(cause error, private string) error {
	//: the library error stays the cause, for its own diagnostic text.
	return errs.Wrap(cause, errs.WrapParams{
		Code:    CodeYAMLFullMarshalFailed,
		Reason:  "YAML_FULL_MARSHAL_FAILED",
		Public:  "YAML encoding failed",
		Private: private,
	})
}

// unmarshalFailed wraps a yaml.v3 decode error under this package's code.
func unmarshalFailed(cause error, private string, fields ...errs.FieldValue) error {
	//: the library error stays the cause, for its own diagnostic text.
	return errs.Wrap(cause, errs.WrapParams{
		Code:    CodeYAMLFullUnmarshalFailed,
		Reason:  "YAML_FULL_UNMARSHAL_FAILED",
		Public:  "YAML decoding failed",
		Private: private,
	}, fields...)
}

// encode writes v into buf through a fresh yaml.v3 encoder, closing it so the
// document is complete.
func encode(buf io.Writer, v any, site string) error {
	//: yaml.v3 Encoder has no Reset(w) — fresh one per call.
	enc := goyaml.NewEncoder(buf)
	//: 2-space indent is wire-compatible (YAML spec accepts any ≥1)
	//: and shrinks output by ~50% on nested configs vs the lib default of 4.
	enc.SetIndent(yamlEncoderIndent)
	//: encode into the buffer.
	if merr := enc.Encode(v); merr != nil {
		//: wrap the library error for code-based matching.
		return marshalFailed(merr, "third-party/codec/yaml."+site+": gopkg.in/yaml.v3 returned an error")
	}
	//: yaml.v3 requires Close to flush pending state before the bytes are complete.
	if cerr := enc.Close(); cerr != nil {
		//: surface the close failure as a marshal error.
		return marshalFailed(cerr, "third-party/codec/yaml."+site+": encoder.Close returned an error")
	}
	//: the document is complete.
	return nil
}

// Marshal serialises v as YAML bytes through a pooled *bytes.Buffer.
func (*yamlCodec) Marshal(v any) (encoded []byte, err error) {
	//: rent an already-Reset buffer from the shared codec pool.
	buf := scratch.AcquireBuffer()
	//: encode into the pooled buffer.
	if eerr := encode(buf, v, "Marshal"); eerr != nil {
		//: drop the buffer back to the pool.
		scratch.ReleaseBuffer(buf)
		//: the wrapped library error.
		return nil, eerr
	}
	//: detach: the returned slice must not alias the pooled buffer.
	out := slices.Clone(buf.Bytes())
	//: cap-discard release.
	scratch.ReleaseBuffer(buf)
	//: success — bytes are the caller's now.
	return out, nil
}

// Unmarshal parses data as YAML into v.
func (*yamlCodec) Unmarshal(data []byte, v any) error {
	//: cap input size so attacker-controlled payloads cannot exhaust RAM.
	if len(data) > maxYAMLBytes {
		//: refuse before the library reads a byte.
		return unmarshalFailed(nil, "third-party/codec/yaml.Unmarshal: len(data) exceeds maxYAMLBytes",
			errs.Int("len", len(data)), errs.Int("cap", maxYAMLBytes))
	}
	//: delegate to yaml.v3 for the actual decoding.
	uerr := goyaml.Unmarshal(data, v)
	//: success fast-path.
	if uerr == nil {
		//: nothing to wrap.
		return nil
	}
	//: wrap the library error.
	return unmarshalFailed(uerr, "third-party/codec/yaml.Unmarshal: gopkg.in/yaml.v3 returned an error")
}

// Append encodes v as YAML and appends the bytes to dst, leaving dst untouched
// on error.
func (*yamlCodec) Append(dst []byte, v any) (appended []byte, err error) {
	//: rent an already-Reset buffer from the shared codec pool.
	buf := scratch.AcquireBuffer()
	//: encode into the pooled buffer.
	if eerr := encode(buf, v, "Append"); eerr != nil {
		//: cap-discard release; dst pristine.
		scratch.ReleaseBuffer(buf)
		//: the wrapped library error.
		return dst, eerr
	}
	//: append the encoded bytes onto the caller's buffer (1 copy total).
	dst = append(dst, buf.Bytes()...)
	//: cap-discard release.
	scratch.ReleaseBuffer(buf)
	//: success — bytes are the caller's now.
	return dst, nil
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
