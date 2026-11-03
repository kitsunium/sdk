package bson

import (
	"bytes"
	"slices"
	"sync/atomic"

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

	// largeDocumentHint is the capacity the last document Marshal wrote
	// needed, when the shared pool could not keep a buffer that large, or 0
	// when it could: the size Marshal asks the size-classed tier for (#906). A
	// hint is only a choice of pool, so the race between two callers setting
	// it costs at most one buffer growth.
	largeDocumentHint atomic.Int64
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
	//: a fresh literal the caller owns; no package variable is read when Register calls this.
	return []string{"application/bson"}
}

// Extensions returns a defensive copy of the file-extension list.
func (*bsonCodec) Extensions() []string {
	//: a fresh literal the caller owns; no package variable is read when Register calls this.
	return []string{".bson"}
}

// Marshal encodes v as a BSON document into a slice the caller owns. v must
// encode as a document at the top level; anything else surfaces
// BSON_MARSHAL_FAILED, and nesting past 100 levels — a cyclic value included —
// DEPTH_EXCEEDED.
//
// A document past the shared pool's 256 KiB cap is encoded into a buffer of
// the size-classed tier, sized by the last such document (#906): a workload
// of large documents reuses its buffer instead of growing one on every call.
func (*bsonCodec) Marshal(v any) (encoded []byte, err error) {
	//: a channel, a function, an unsafe pointer: no wire format holds one (#1101).
	if verr := codec.CheckValue(v); verr != nil {
		return nil, verr
	}
	hint := int(largeDocumentHint.Load())
	buf := scratch.AcquireBufferSized(hint)
	defer scratch.ReleaseBufferSized(buf)
	out, err := appendDocument(buf.AvailableBuffer(), v)
	//: nothing half-written reaches the caller.
	if err != nil {
		//: the typed refusal.
		return nil, err
	}
	encoded = slices.Clone(out)
	//: remember a buffer the shared pool could not keep, forget it after a
	//: document that fit one.
	if cap(out) > scratch.MaxRetainedBufBytes {
		largeDocumentHint.Store(int64(cap(out)))
	} else if hint != 0 {
		largeDocumentHint.Store(0)
	}
	//: a document that outgrew the pooled buffer leaves its larger backing
	//: array to the pool its capacity belongs to instead.
	if cap(out) > buf.Cap() {
		*buf = *bytes.NewBuffer(out[:0])
	}
	//: one exact-size copy for the caller.
	return encoded, nil
}

// Unmarshal decodes the BSON document data into v, which must be a non-nil
// pointer or a non-nil map. The input is checked whole before v is touched.
func (*bsonCodec) Unmarshal(data []byte, v any) error {
	//: a nil or non-pointer target is the caller's error, refused first (#1101).
	if terr := checkTarget(v); terr != nil {
		return terr
	}
	//: no bytes is no document in this format (#1101).
	if len(data) == 0 {
		return codec.EmptyInput
	}
	//: CWE-400 defence — refuse oversized inputs before anything is read.
	if len(data) > maxBSONBytes {
		//: surface the size sentinel.
		return errs.Wrap(nil, errs.WrapParams{
			Code:    corebson.CodeBSONSizeExceeded,
			Reason:  "SIZE_EXCEEDED",
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
