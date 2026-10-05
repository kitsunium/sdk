package toml

import (
	"io"
	"slices"

	"github.com/kitsunium/sdk/internal/core/data/codec"
	"github.com/kitsunium/sdk/internal/core/data/codec/scratch"
)

// Package-level state: the codec singleton plus the hoisted MIME /
// extension tables.
var (
	//: register the singleton and expose it as a typed package var.
	Codec codec.Codec = codec.Register(&tomlCodec{})

	//: MIME table hoisted.
	mimeTypes = []string{"application/toml"}

	//: extension table hoisted for the same reason.
	extensions = []string{".toml"}
)

// tomlCodec is the concrete Codec implementation for TOML.
type tomlCodec struct{}

// New returns a TOML codec instance.
func New() codec.Codec {
	//: stateless — one singleton is enough for the whole process.
	return Codec
}

// Name implements codec.Codec.
func (*tomlCodec) Name() string {
	//: canonical identifier.
	return "toml"
}

// MIMETypes lists every MIME alias.
func (*tomlCodec) MIMETypes() []string {
	//: hand back the package-level slice.
	return slices.Clone(mimeTypes)
}

// Extensions lists every file extension.
func (*tomlCodec) Extensions() []string {
	//: hand back the package-level slice.
	return slices.Clone(extensions)
}

// Marshal serialises v, a map or a struct, as a TOML document. The document is
// written into a buffer from the shared codec pool and returned as a copy of
// exactly its length.
func (*tomlCodec) Marshal(v any) (encoded []byte, err error) {
	//: rent an already-Reset buffer from the shared codec pool.
	buf := scratch.AcquireBuffer()
	out, err := encodeDocument(buf.AvailableBuffer(), v)
	//: a value TOML cannot represent.
	if err != nil {
		scratch.ReleaseBuffer(buf)
		//: MARSHAL_FAILED.
		return nil, err
	}
	encoded = slices.Clone(out)
	//: a document that outgrew the pooled buffer leaves its size behind, so
	//: the next one of that size does not grow from scratch again.
	if cap(out) > buf.Cap() {
		buf.Write(out)
	}
	scratch.ReleaseBuffer(buf)
	//: success — bytes are the caller's now.
	return encoded, nil
}

// Unmarshal parses data as a TOML document into v, a non-nil pointer.
func (*tomlCodec) Unmarshal(data []byte, v any) error {
	//: parse, then decode; every failure is UNMARSHAL_FAILED.
	return unmarshal(data, v)
}

// Append encodes v as a TOML document directly onto dst. Implements the
// optional codec.Appender interface; on failure dst is returned with its
// length unchanged.
func (*tomlCodec) Append(dst []byte, v any) (appended []byte, err error) {
	//: the encoder appends in place.
	return encodeDocument(dst, v)
}

// NewEncoder returns a streaming codec.Encoder writing one document to w per
// Encode call.
func (*tomlCodec) NewEncoder(w io.Writer) codec.Encoder {
	//: the encoder owns no state between documents.
	return &tomlEncoder{w: w}
}

// NewDecoder returns a streaming codec.Decoder reading r as one document.
func (*tomlCodec) NewDecoder(r io.Reader) codec.Decoder {
	//: the decoder reads r whole on its first Decode.
	return &tomlDecoder{r: r}
}
