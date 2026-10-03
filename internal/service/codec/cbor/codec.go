// Package cbor is the SDK's CBOR codec (RFC 8949), written on the standard
// library alone. It encodes any Go value the reflection rules below can
// read, decodes into any Go value they can fill, and streams one data item
// per Encode or Decode.
//
// # What is written
//
// Integers, lengths and tag numbers take their shortest head; a float64 is a
// double and a float32 a single, NaN is written 0xf97e00 and the infinities
// as half-precision floats; a string is a text string, a []byte or [N]byte a
// byte string; a nil slice, map, pointer or interface is null; a struct is a
// map keyed by field (or an array with toarray); a time.Time is its integer
// Unix seconds, null when zero; a big.Int an integer or a bignum (tag 2 or 3).
// Map pairs are sorted by encoded key (RFC 8949 §4.2.1), so equal values
// encode to equal bytes. Nothing is written that the decoder would refuse:
// not a string that is not UTF-8, not a map with two keys that encode alike,
// nothing nested deeper than 32 arrays, maps and tags.
//
// # What is read
//
// Exactly one well-formed, valid data item — definite and indefinite
// lengths, half, single and double floats, every major type — checked in
// full, against the bounds (a million elements per array or pairs per map,
// thirty-two levels, a million chunks per string), before anything is
// decoded: malformed input never touches the target. A text string must be
// UTF-8 wherever it sits; tags 0 to 3 must enclose the type RFC 8949 gives
// them. A tag the codec does not interpret is transparent.
//
// # Struct tags
//
// A field's key is the name in its `cbor` tag, or in its `json` tag when it
// has no `cbor` tag, or its Go name; the options are omitempty, omitzero and
// keyasint, and a blank field `_ struct{}` tagged `cbor:",toarray"` encodes
// a struct as an array. Embedded structs follow encoding/json's rules. A
// type implementing encoding.BinaryMarshaler is written as a byte string,
// and one implementing encoding.BinaryUnmarshaler reads one; a type with
// MarshalCBOR or UnmarshalCBOR writes or reads its own item.
package cbor

import (
	"bytes"
	"io"
	"slices"

	"github.com/kitsunium/sdk/internal/core/codec"
	"github.com/kitsunium/sdk/internal/core/codec/scratch"
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
