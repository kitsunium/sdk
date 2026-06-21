// Package bson wraps go.mongodb.org/mongo-driver/bson as a codec.Codec
// implementation. BSON is a document format: the top-level value MUST be a
// struct or map (BSON cannot represent a bare scalar at the root), so Marshal
// of a top-level scalar surfaces BSON_MARSHAL_FAILED. The library exposes no
// incremental Encoder/Decoder over an io stream in its stable surface, so this
// codec is NOT a StreamingCodec; it does implement the optional Appender.
package bson

import (
	"slices"

	gobson "go.mongodb.org/mongo-driver/bson"

	"github.com/kitsunium/sdk/internal/core/codec"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// maxBSONBytes caps the Unmarshal input for untrusted payloads. mongo-driver's
// decoder reads declared element lengths before validating them, so a crafted
// document can pre-allocate large buffers; size-limiting the input is the
// primary memory-exhaustion defence (CWE-400). 10 MiB covers realistic
// documents and matches the other library-backed codecs.
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

// Marshal encodes v as a BSON document. v must be a struct or map at the top
// level; a scalar surfaces BSON_MARSHAL_FAILED via the library error.
func (*bsonCodec) Marshal(v any) (encoded []byte, err error) {
	//: delegate to the library encoder.
	out, merr := gobson.Marshal(v)
	//: success fast-path.
	if merr == nil {
		//: hand back the freshly-encoded document.
		return out, nil
	}
	//: wrap the library failure with the dotted-quad code.
	return nil, errs.Wrap(merr, errs.WrapParams{
		Code:    CodeBSONMarshalFailed,
		Reason:  "BSON_MARSHAL_FAILED",
		Public:  "BSON encoding failed",
		Private: "service/codec/bson.Marshal: go.mongodb.org/mongo-driver/bson.Marshal returned an error",
	})
}

// Unmarshal decodes a BSON document into v after the size cap.
func (*bsonCodec) Unmarshal(data []byte, v any) error {
	//: CWE-400 defence — refuse oversized inputs before the decoder allocates.
	if len(data) > maxBSONBytes {
		//: surface the size sentinel.
		return errs.Wrap(nil, errs.WrapParams{
			Code:    CodeBSONSizeExceeded,
			Reason:  "BSON_SIZE_EXCEEDED",
			Public:  "BSON input exceeds size limit",
			Private: "service/codec/bson.Unmarshal: len(data) > maxBSONBytes",
		})
	}
	//: delegate to the library decoder.
	uerr := gobson.Unmarshal(data, v)
	//: success fast-path.
	if uerr == nil {
		//: nothing to wrap.
		return nil
	}
	//: wrap the library failure with the dotted-quad code.
	return errs.Wrap(uerr, errs.WrapParams{
		Code:    CodeBSONUnmarshalFailed,
		Reason:  "BSON_UNMARSHAL_FAILED",
		Public:  "BSON decoding failed",
		Private: "service/codec/bson.Unmarshal: go.mongodb.org/mongo-driver/bson.Unmarshal returned an error",
	})
}

// Append encodes v as BSON and appends the result onto dst. Implements the
// optional codec.Appender. mongo-driver has no append-style API, so the encode
// allocates an intermediate document; a failure leaves dst untouched.
func (c *bsonCodec) Append(dst []byte, v any) (appended []byte, err error) {
	//: snapshot dst so a failure restores the caller's buffer exactly.
	origLen := len(dst)
	//: encode the document (intermediate allocation is unavoidable here).
	out, merr := c.Marshal(v)
	//: Marshal already wrapped any error with the BSON sentinel.
	if merr != nil {
		//: leave dst as the caller passed it.
		return dst[:origLen], merr
	}
	//: append the encoded document onto dst.
	return append(dst, out...), nil
}
