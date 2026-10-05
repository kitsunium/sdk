package bson

import (
	"bytes"
	"slices"

	"github.com/kitsunium/sdk/internal/core/data/codec"
	corebson "github.com/kitsunium/sdk/internal/core/data/codec/bson"
	"github.com/kitsunium/sdk/internal/core/data/codec/scratch"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// maxBSONBytes caps the Unmarshal input for untrusted payloads (CWE-400). The
// validator never allocates from a declared length it has not checked, so
// the cap bounds what one call can make the decoder allocate; 10 MiB covers
// realistic documents and matches the other codecs.
const maxBSONBytes int = 10 << 20

var (
	// Codec is the registered BSON singleton.
	Codec codec.Codec = codec.Register(&bsonCodec{})

	// mimeTypes hoisted so MIMETypes does not allocate the literal per call.
	mimeTypes = []string{"application/bson"}

	// extensions hoisted for the same reason.
	extensions = []string{".bson"}
)

// bsonCodec is the concrete Codec implementation for BSON. Stateless.
type bsonCodec struct{}

// New returns the BSON codec singleton.
func New() codec.Codec {
	//: stateless — one singleton serves the whole process.
	return Codec
}

// Name returns the canonical Format identifier.
func (*bsonCodec) Name() string {
	//: the registered Format string.
	return "bson"
}

// MIMETypes returns a defensive copy of the MIME alias list.
func (*bsonCodec) MIMETypes() []string {
	//: callers must not mutate the package-level table.
	return slices.Clone(mimeTypes)
}

// Extensions returns a defensive copy of the file-extension list.
func (*bsonCodec) Extensions() []string {
	//: callers must not mutate the package-level table.
	return slices.Clone(extensions)
}

// Marshal encodes v as a BSON document into a slice the caller owns. v must
// encode as a document at the top level; anything else surfaces
// BSON_MARSHAL_FAILED, and nesting past 100 levels — a cyclic value included —
// BSON_DEPTH_EXCEEDED.
func (*bsonCodec) Marshal(v any) (encoded []byte, err error) {
	buf := scratch.AcquireBuffer()
	defer scratch.ReleaseBuffer(buf)
	out, err := appendDocument(buf.AvailableBuffer(), v)
	//: nothing half-written reaches the caller.
	if err != nil {
		//: the typed refusal.
		return nil, err
	}
	encoded = slices.Clone(out)
	//: a document that outgrew the pooled buffer leaves its larger backing
	//: array to the pool instead, when the pool may keep one that large.
	if cap(out) > buf.Cap() && cap(out) <= scratch.MaxRetainedBufBytes {
		*buf = *bytes.NewBuffer(out[:0])
	}
	//: one exact-size copy for the caller.
	return encoded, nil
}

// Unmarshal decodes the BSON document data into v, which must be a non-nil
// pointer or a non-nil map. The input is checked whole before v is touched.
func (*bsonCodec) Unmarshal(data []byte, v any) error {
	//: CWE-400 defence — refuse oversized inputs before anything is read.
	if len(data) > maxBSONBytes {
		//: surface the size sentinel.
		return errs.Wrap(nil, errs.WrapParams{
			Code:    corebson.CodeBSONSizeExceeded,
			Reason:  "BSON_SIZE_EXCEEDED",
			Public:  "BSON input exceeds size limit",
			Private: "service/data/codec/bson.Unmarshal: len(data) > maxBSONBytes",
		})
	}
	//: one well-formed document, or nothing is decoded.
	if err := validateRoot(data); err != nil {
		//: malformed, or nested too deep.
		return err
	}
	//: decode into the target.
	return decodeInto(data, v)
}

// Append encodes v as BSON and appends the document onto dst. Implements the
// optional codec.Appender: the encoder writes straight into dst, so a
// destination with room allocates nothing for the document itself. A failure
// returns dst with its original length.
func (*bsonCodec) Append(dst []byte, v any) (appended []byte, err error) {
	//: the encoder restores dst's length itself on failure.
	return appendDocument(dst, v)
}
