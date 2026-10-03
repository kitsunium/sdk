// Package yaml is the SDK's YAML codec: a native, standard-library-only
// reader and writer of a NAMED SUBSET of YAML 1.2.2, sized for configuration.
//
// # The subset
//
// Read: block mappings and sequences (by indentation, including a sequence
// at its key's own indentation and the compact "- key: value" form), flow
// mappings and sequences across lines, plain, single-quoted and double-quoted
// scalars with every YAML escape, literal (|) and folded (>) block scalars
// with their chomping and indentation indicators, comments, and one document
// with an optional "---" start and "..." end. Plain scalars resolve by the
// YAML 1.2 core schema: null and ~, true and false — never yes, no, on or off
// — decimal, 0o octal and 0x hexadecimal integers, floats with .inf and .nan.
//
// Refused BY NAME, each with its own code and the line and column it starts
// at: anchors (&), aliases (*), tags (! and !!), merge keys (<<), a second
// document, complex keys (?, or a collection as a key), directives (%YAML,
// %TAG), and a mapping holding the same key twice. An integer written with a
// leading zero (0644) is octal to YAML 1.1 and decimal to YAML 1.2; it reads
// as text into a string, and is refused by name wherever its value matters.
//
// A refusal never quotes the document: it carries a line, a column, a
// constant detail and, for a value its Go target cannot hold, the Go type.
//
// # Bounds
//
// A document holds at most 10 MiB, 1 048 576 nodes and 100 levels of nesting;
// an implicit key at most 1024 characters, as YAML itself bounds it. Nothing
// the decoder is given can make it panic.
//
// # Go values
//
// Struct fields use the yaml tag as gopkg.in/yaml.v3 does: a field's key is
// its tag's name or its own name in lower case, "-" leaves it out, and the
// flags are omitempty, flow (write the collection inline) and inline (merge a
// struct's fields, or collect the remaining keys into a map with string
// keys). An unknown key is ignored. A type may implement MarshalYAML() (any,
// error), UnmarshalYAML(func(any) error) error, encoding.TextMarshaler and
// encoding.TextUnmarshaler; time.Time and time.Duration are written and read
// as text. An untyped target receives nil, bool, int, int64 or uint64,
// float64, string, []any and map[string]any — a mapping keyed by each key's
// text.
//
// # Writing
//
// The encoder writes deterministic block style, indented by two spaces: struct
// fields in declaration order, map keys sorted. A string is written plain only
// when every YAML reader — the core schema, YAML 1.1, a timestamp parser —
// reads it back as the same string; otherwise it is double-quoted, and a
// multi-line string is a literal block. A float always carries its dot.
//
// # Streams
//
// Unmarshal reads exactly one document and refuses a second by name, where
// gopkg.in/yaml.v3 silently ignored it. NewDecoder reads a stream of
// "---"-separated documents, each one read by the same subset and bounded at
// 10 MiB; NewEncoder writes one.
//
// The whole of YAML — every construct this package refuses — remains
// available through the opt-in third-party/codec/yaml package, registered as
// "yaml-full".
package yaml

import (
	"io"
	"slices"

	"github.com/kitsunium/sdk/internal/core/codec"
	"github.com/kitsunium/sdk/internal/core/codec/scratch"
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
		return errs.Wrap(UnmarshalFailed, errs.WrapParams{},
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
