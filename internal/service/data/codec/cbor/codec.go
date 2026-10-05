package cbor

import (
	"bytes"
	"io"
	"slices"

	"github.com/kitsunium/sdk/internal/core/data/codec"
	"github.com/kitsunium/sdk/internal/core/data/codec/scratch"
)

// Package-level state: the codec singleton plus the hoisted MIME /
// extension tables.
var (
	//: register the singleton and expose it as a typed package var.
	Codec codec.Codec = codec.Register(&cborCodec{})

	//: MIME table hoisted.
	mimeTypes = []string{"application/cbor"}

	//: extension table hoisted for the same reason.
	extensions = []string{".cbor"}
)

// cborCodec is the concrete Codec implementation for CBOR.
type cborCodec struct{}

// New returns a CBOR codec instance.
func New() codec.Codec {
	//: stateless — one singleton is enough for the whole process.
	return Codec
}

// Name implements codec.Codec.
func (*cborCodec) Name() string {
	//: canonical identifier.
	return "cbor"
}

// MIMETypes lists every MIME alias.
func (*cborCodec) MIMETypes() []string {
	//: hand back a copy of the package-level slice.
	return slices.Clone(mimeTypes)
}

// Extensions lists every file extension.
func (*cborCodec) Extensions() []string {
	//: hand back a copy of the package-level slice.
	return slices.Clone(extensions)
}

// Marshal serialises v as CBOR bytes. The encoding is built in a pooled
// scratch buffer and copied once into a slice of exactly its length.
func (*cborCodec) Marshal(v any) (encoded []byte, err error) {
	buf := scratch.AcquireBuffer()
	b, err := appendValue(buf.AvailableBuffer(), v, walkDepth{})
	//: the result is the caller's, sized to the encoding.
	if err == nil {
		encoded = bytes.Clone(b)
	}
	keepScratch(buf, b)
	//: MARSHAL_FAILED, or the cause's own code when it is an SDK error.
	return encoded, err
}

// Unmarshal parses data as exactly one CBOR data item into v, which must be
// a non-nil pointer. The whole of data is validated before v is touched.
func (*cborCodec) Unmarshal(data []byte, v any) error {
	//: nothing at all is not a data item.
	if len(data) == 0 {
		//: refused.
		return malformed(0, "the input is empty")
	}
	n, err := validateItem(data, maxCBORNestedLevels)
	//: not well-formed, not valid, or past a bound.
	if err != nil {
		//: refused before decoding.
		return err
	}
	//: one item and nothing after it.
	if n != len(data) {
		//: refused before decoding.
		return malformed(n, "bytes follow the data item")
	}
	//: the validated item into v.
	return unmarshalItem(data, v)
}

// Append encodes v and appends the bytes to dst. Implements the optional
// codec.Appender interface: the encoding is written straight into dst, with
// no intermediate buffer. On failure dst is returned at its original length.
func (*cborCodec) Append(dst []byte, v any) (appended []byte, err error) {
	out, err := appendValue(dst, v, walkDepth{})
	//: a failed encoding leaves dst as it was.
	if err != nil {
		//: the prior contents only.
		return dst, err
	}
	//: dst followed by the encoding.
	return out, nil
}

// NewEncoder wraps w in a streaming codec.Encoder writing one data item per
// Encode.
func (*cborCodec) NewEncoder(w io.Writer) codec.Encoder {
	//: one Write per item.
	return &cborEncoder{w: w}
}

// NewDecoder wraps r in a streaming codec.Decoder reading one data item per
// Decode, validated under the same bounds as Unmarshal.
func (*cborCodec) NewDecoder(r io.Reader) codec.Decoder {
	//: the validator resumes across reads.
	return &cborDecoder{r: r, walk: validator{limit: maxCBORNestedLevels}}
}

// keepScratch returns buf to the shared pool, keeping the larger backing
// array the encoding may have grown into; the pool's cap-discard rule then
// drops it when it is oversized, as it drops any buffer.
func keepScratch(buf *bytes.Buffer, encoded []byte) {
	//: the encoding outgrew the buffer: let the buffer adopt the new array.
	if cap(encoded) > buf.Cap() {
		*buf = *bytes.NewBuffer(encoded[:0])
	}
	scratch.ReleaseBuffer(buf)
}
