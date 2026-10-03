// Package msgpack is the MessagePack codec — a native implementation of the
// specification (github.com/msgpack/msgpack/blob/master/spec.md) on the
// standard library alone, registered as the "msgpack" Format. It replaced
// github.com/vmihailenco/msgpack/v5 and writes the bytes that library wrote:
// testdata/vendor-golden.txt holds the vendor's output for every value family
// and the suite holds the native encoder to it byte for byte.
//
// The codec also implements StreamingCodec (one value per Encode, read back
// one per Decode) and Appender (encode straight onto a caller's buffer).
// Decoding is held to the bounds wire.go names: 10 MiB of input per Unmarshal
// and per stream, 1000 levels of nesting, and no allocation sized by a length
// the input declares before the input has proved it holds that many bytes.
package msgpack

import (
	"bytes"
	"io"
	"slices"

	"github.com/kitsunium/sdk/internal/core/data/codec"
	coremsgpack "github.com/kitsunium/sdk/internal/core/data/codec/msgpack"
	"github.com/kitsunium/sdk/internal/core/data/codec/scratch"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// Package-level state: the codec singleton plus the hoisted MIME / extension
// tables.
var (
	//: register the singleton and expose it as a typed package var.
	Codec codec.Codec = codec.Register(&msgpackCodec{})

	//: MIME table hoisted.
	mimeTypes = []string{"application/msgpack", "application/x-msgpack"}

	//: extension table hoisted for the same reason.
	extensions = []string{".msgpack", ".mpk"}
)

// msgpackCodec is the concrete Codec implementation for MessagePack.
type msgpackCodec struct{}

// New returns a MessagePack codec instance.
func New() codec.Codec {
	//: stateless — one singleton is enough for the whole process.
	return Codec
}

// Name implements codec.Codec.
func (*msgpackCodec) Name() string {
	//: canonical identifier.
	return "msgpack"
}

// MIMETypes lists every MIME alias.
func (*msgpackCodec) MIMETypes() []string {
	//: hand back the package-level slice.
	return slices.Clone(mimeTypes)
}

// Extensions lists every file extension.
func (*msgpackCodec) Extensions() []string {
	//: hand back the package-level slice.
	return slices.Clone(extensions)
}

// Marshal serialises v as MessagePack bytes. The value is encoded into a
// pooled scratch buffer and the result is one exact-size copy, so a call
// costs one allocation plus whatever v's own marshal methods allocate.
func (*msgpackCodec) Marshal(v any) (encoded []byte, err error) {
	//: rent an already-Reset buffer from the shared codec pool.
	buf := scratch.AcquireBuffer()
	b, merr := appendAny(buf.AvailableBuffer(), v, 0)
	//: success copies out before the buffer goes back.
	if merr == nil {
		encoded = slices.Clone(b)
	}
	//: keep a grown array in the pool (subject to its cap-discard).
	retain(buf, b)
	scratch.ReleaseBuffer(buf)
	//: failure path — the typed MARSHAL_FAILED from the encoder.
	if merr != nil {
		//: nothing to hand back.
		return nil, merr
	}
	//: the caller owns the copy.
	return encoded, nil
}

// Unmarshal parses data — exactly one MessagePack value, at most
// maxMsgPackBytes long — into the value v points at.
func (*msgpackCodec) Unmarshal(data []byte, v any) error {
	//: cap input size so a hostile payload cannot make the decoder hold more
	//: than the bound, whatever lengths it declares.
	if len(data) > maxMsgPackBytes {
		//: surface an UNMARSHAL_FAILED with a diagnostic Private message.
		return errs.Wrap(nil, errs.WrapParams{
			Code:    coremsgpack.CodeMsgPackUnmarshalFailed,
			Reason:  reasonUnmarshal,
			Public:  "MessagePack input exceeds size limit",
			Private: "service/data/codec/msgpack.Unmarshal: len(data) exceeds maxMsgPackBytes",
		}, errs.Int("len", len(data)), errs.Int("cap", maxMsgPackBytes))
	}
	//: one value, nothing after it.
	return unmarshalInto(data, v)
}

// Append encodes v as MessagePack and appends the bytes to dst, encoding
// straight onto it: no intermediate buffer and no copy. On failure dst is
// returned with its original length.
func (*msgpackCodec) Append(dst []byte, v any) (appended []byte, err error) {
	out, aerr := appendAny(dst, v, 0)
	//: failure path — the caller's slice keeps its length.
	if aerr != nil {
		//: dst as it came in.
		return dst, aerr
	}
	//: dst plus the encoding.
	return out, nil
}

// NewEncoder wraps w in a streaming codec.Encoder: each Encode writes one
// complete value with a single Write, and a value that fails to encode writes
// nothing.
func (*msgpackCodec) NewEncoder(w io.Writer) codec.Encoder {
	//: the encoder owns no buffer between calls.
	return newEncoder(w)
}

// NewDecoder wraps r in a streaming codec.Decoder. The whole stream is held to
// one byte past maxMsgPackBytes — the bound Unmarshal enforces on one input —
// and a declared length beyond what the stream can still deliver is refused
// before anything is read or reserved for it (CWE-400 / CWE-1284).
func (*msgpackCodec) NewDecoder(r io.Reader) codec.Decoder {
	//: the decoder buffers reads; values are framed one at a time.
	return newDecoder(r)
}

// retain hands b's backing array to buf, so a buffer that grew while encoding
// returns to the pool at its new size — scratch discards it past
// MaxRetainedBufBytes.
func retain(buf *bytes.Buffer, b []byte) {
	//: adopt the array without copying; the next user starts empty.
	*buf = *bytes.NewBuffer(b[:0])
}
