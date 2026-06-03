// Package msgpack wraps github.com/vmihailenco/msgpack/v5 as a codec.Codec
// implementation. The library exposes an Encoder/Decoder pair so this codec
// also implements StreamingCodec.
package msgpack

import (
	"io"
	"slices"

	gomsgpack "github.com/vmihailenco/msgpack/v5"

	"github.com/kitsunium/sdk/internal/core/codec"
	"github.com/kitsunium/sdk/internal/core/codec/scratch"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// maxMsgPackBytes caps the Unmarshal input size for untrusted payloads.
// vmihailenco/msgpack v5 does not expose per-decoder caps for array /
// map / nesting depth; a MessagePack value with a huge declared length
// field can pre-allocate that many slots before any consistency check,
// so size-limiting the buffer is the primary defence against memory-
// exhaustion DoS (CWE-400 / CWE-1284). 10 MiB comfortably covers every
// realistic configuration or event-stream document.
const maxMsgPackBytes int = 10 << 20

// Package-level state: the codec singleton plus the hoisted MIME /
// extension tables (hoisted).
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

// Marshal serialises v as MessagePack bytes. Uses gomsgpack's exposed
// Encoder pool (GetEncoder/PutEncoder) plus a local *bytes.Buffer pool
// — gomsgpack.Marshal already pools the encoder but allocates a fresh
// bytes.Buffer per call; pooling the buffer eliminates that allocation.
func (*msgpackCodec) Marshal(v any) (encoded []byte, err error) {
	//: rent an already-Reset buffer from the shared codec pool.
	buf := scratch.AcquireBuffer()
	//: rent the encoder from the library's exposed pool.
	enc := gomsgpack.GetEncoder()
	//: re-point the encoder at our pooled buffer.
	enc.Reset(buf)
	//: UseCompactInts shrinks wire size on int-heavy payloads by
	//: emitting positive fixint / int8 / int16 / int32 instead of the
	//: lib default int64 for every int. Wire-compatible on the read
	//: side (any conforming MessagePack decoder accepts narrower int
	//: forms). Audit M3.
	enc.UseCompactInts(true)
	//: encode the value.
	merr := enc.Encode(v)
	//: return the encoder to the pool unconditionally — Encode failure
	//: leaves the encoder in a reusable state (Reset is the contract).
	gomsgpack.PutEncoder(enc)
	//: failure path — surface the typed sentinel.
	if merr != nil {
		//: drop the buffer back to the pool only if it's not oversized.
		scratch.ReleaseBuffer(buf)
		//: wrap the library error for reason-based matching.
		return nil, errs.Wrap(merr, errs.WrapParams{
			Code:    CodeMsgPackMarshalFailed,
			Reason:  "MARSHAL_FAILED",
			Public:  "MessagePack encoding failed",
			Private: "service/codec/msgpack.Marshal: vmihailenco/msgpack/v5 returned an error",
		})
	}
	//: detach: slices.Clone so the returned slice doesn't alias the
	//: pooled buffer (next caller would overwrite it).
	out := slices.Clone(buf.Bytes())
	//: cap-discard release.
	scratch.ReleaseBuffer(buf)
	//: success — bytes are the caller's now.
	return out, nil
}

// Unmarshal parses data as MessagePack into v. Uses gomsgpack's exposed
// Decoder pool (GetDecoder/PutDecoder) plus a local *bytes.Reader pool
// — gomsgpack.Unmarshal builds a fresh bytes.Reader per call that
// escapes to heap; pooling it removes that allocation.
func (*msgpackCodec) Unmarshal(data []byte, v any) error {
	//: cap input size so attacker-controlled payloads cannot exhaust RAM
	//: during pre-allocation from huge declared length fields.
	if len(data) > maxMsgPackBytes {
		//: surface an UNMARSHAL_FAILED with a diagnostic Private message.
		return errs.Wrap(nil, errs.WrapParams{
			Code:    CodeMsgPackUnmarshalFailed,
			Reason:  "UNMARSHAL_FAILED",
			Public:  "MessagePack input exceeds size limit",
			Private: "service/codec/msgpack.Unmarshal: len(data) exceeds maxMsgPackBytes",
		}, errs.Int("len", len(data)), errs.Int("cap", maxMsgPackBytes))
	}
	//: rent a *bytes.Reader from the shared pool, positioned at data.
	r := scratch.AcquireReader(data)
	//: rent the decoder from the library's exposed pool.
	dec := gomsgpack.GetDecoder()
	dec.Reset(r)
	//: decode into the caller's target.
	uerr := dec.Decode(v)
	//: return the decoder + reader to their pools unconditionally —
	//: both are reusable post-Reset.
	gomsgpack.PutDecoder(dec)
	scratch.ReleaseReader(r)
	//: success fast-path.
	if uerr == nil {
		//: nothing to wrap.
		return nil
	}
	//: wrap the library error.
	return errs.Wrap(uerr, errs.WrapParams{
		Code:    CodeMsgPackUnmarshalFailed,
		Reason:  "UNMARSHAL_FAILED",
		Public:  "MessagePack decoding failed",
		Private: "service/codec/msgpack.Unmarshal: vmihailenco/msgpack/v5 returned an error",
	})
}

// Append encodes v as MessagePack and appends the bytes to dst. Reuses
// the same pooled encoder + buffer as Marshal — the encode result is
// then appended onto the caller's buffer instead of returned as a
// fresh slice. Saves the slices.Clone Marshal pays.
func (*msgpackCodec) Append(dst []byte, v any) (appended []byte, err error) {
	//: rent an already-Reset buffer from the shared codec pool.
	buf := scratch.AcquireBuffer()
	//: rent the encoder from the library's exposed pool.
	enc := gomsgpack.GetEncoder()
	//: re-point the encoder at our pooled buffer.
	enc.Reset(buf)
	//: UseCompactInts — same wire-compatible flag as Marshal.
	enc.UseCompactInts(true)
	//: encode the value.
	merr := enc.Encode(v)
	//: return the encoder to the lib pool unconditionally.
	gomsgpack.PutEncoder(enc)
	//: failure path — surface the typed sentinel, leave dst pristine.
	if merr != nil {
		//: drop the buffer back to the pool if not oversized.
		scratch.ReleaseBuffer(buf)
		//: wrap the library error for reason-based matching.
		return dst, errs.Wrap(merr, errs.WrapParams{
			Code:    CodeMsgPackMarshalFailed,
			Reason:  "MARSHAL_FAILED",
			Public:  "MessagePack encoding failed",
			Private: "service/codec/msgpack.Append: vmihailenco/msgpack/v5 returned an error",
		})
	}
	//: append into the caller's buffer (1 copy total — no slices.Clone).
	dst = append(dst, buf.Bytes()...)
	//: cap-discard release.
	scratch.ReleaseBuffer(buf)
	//: success — bytes are the caller's now.
	return dst, nil
}

// NewEncoder wraps w in a streaming codec.Encoder.
func (*msgpackCodec) NewEncoder(w io.Writer) codec.Encoder {
	//: wrap the vmihailenco encoder.
	return &msgpackEncoder{inner: gomsgpack.NewEncoder(w)}
}

// NewDecoder wraps r in a streaming codec.Decoder. The reader is funnelled
// through io.LimitReader(r, maxMsgPackBytes+1) so the streaming path inherits
// the same untrusted-input cap Unmarshal enforces — a value with a huge
// declared array / map / bin length truncates at the limit and surfaces as
// UNMARSHAL_FAILED via Decode's wrap branch instead of pre-allocating those
// slots (CWE-400 / CWE-1284). The +1 keeps payloads of exactly maxMsgPackBytes
// decodable while still bounding allocation. Mirrors CBOR's hardened DecMode
// streaming fix.
func (*msgpackCodec) NewDecoder(r io.Reader) codec.Decoder {
	//: cap the stream so a single oversized value cannot exhaust RAM.
	limited := io.LimitReader(r, int64(maxMsgPackBytes)+1)
	//: wrap the vmihailenco decoder over the bounded reader.
	return &msgpackDecoder{inner: gomsgpack.NewDecoder(limited)}
}
