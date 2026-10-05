package yaml

import (
	"io"
	"slices"

	"github.com/kitsunium/sdk/internal/core/data/codec"
	"github.com/kitsunium/sdk/internal/core/data/codec/scratch"
	coreyaml "github.com/kitsunium/sdk/internal/core/data/codec/yaml"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// Package-level state: the codec singleton plus the hoisted MIME and
// extension tables.
var (
	// Codec is the registered YAML singleton.
	Codec codec.Codec = codec.Register(&yamlCodec{})

	// mimeTypes is hoisted so MIMETypes does not allocate the literal per call.
	mimeTypes = []string{"application/yaml", "text/yaml", "application/x-yaml"}

	// extensions is hoisted for the same reason.
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
	//: a copy of the package-level slice.
	return slices.Clone(mimeTypes)
}

// Extensions lists every file extension.
func (*yamlCodec) Extensions() []string {
	//: a copy of the package-level slice.
	return slices.Clone(extensions)
}

// Marshal writes v as one YAML document.
func (*yamlCodec) Marshal(v any) (encoded []byte, err error) {
	//: rent an already-Reset buffer from the shared codec pool.
	buf := scratch.AcquireBuffer()
	enc := encoder{buf: buf}
	//: encode into the pooled buffer.
	if err := enc.encodeDocument(v); err != nil {
		scratch.ReleaseBuffer(buf)
		//: refused.
		return nil, err
	}
	//: detach: the returned slice must not alias the pooled buffer.
	out := slices.Clone(buf.Bytes())
	scratch.ReleaseBuffer(buf)
	//: the caller's bytes.
	return out, nil
}

// Unmarshal reads data as exactly one YAML document of the subset into v,
// which must be a non-nil pointer. An empty document leaves v unchanged.
func (*yamlCodec) Unmarshal(data []byte, v any) error {
	//: the byte bound, before a byte is read.
	if len(data) > maxYAMLBytes {
		//: refused, with the sizes.
		return errs.Wrap(coreyaml.UnmarshalFailed, errs.WrapParams{},
			errs.Int("len", len(data)), errs.Int("cap", maxYAMLBytes),
			errs.String("detail", "the document is larger than the decoder accepts"))
	}
	//: one document, from line 1.
	return decodeDocument(data, v, 1)
}

// Append encodes v as YAML and appends the bytes to dst, leaving dst
// untouched on error. It implements codec.Appender.
func (*yamlCodec) Append(dst []byte, v any) (appended []byte, err error) {
	//: rent an already-Reset buffer from the shared codec pool.
	buf := scratch.AcquireBuffer()
	enc := encoder{buf: buf}
	//: encode into the pooled buffer.
	if err := enc.encodeDocument(v); err != nil {
		scratch.ReleaseBuffer(buf)
		//: dst pristine.
		return dst, err
	}
	//: one copy onto the caller's buffer.
	dst = append(dst, buf.Bytes()...)
	scratch.ReleaseBuffer(buf)
	//: the caller's bytes.
	return dst, nil
}

// NewEncoder returns a codec.Encoder writing one document per Encode to w,
// separated by "---".
func (*yamlCodec) NewEncoder(w io.Writer) codec.Encoder {
	//: nothing is written before the first Encode.
	return &yamlEncoder{w: w}
}

// NewDecoder returns a codec.Decoder reading one document per Decode from r.
func (*yamlCodec) NewDecoder(r io.Reader) codec.Decoder {
	//: documents are read line by line, each bounded.
	return newStreamDecoder(r)
}
