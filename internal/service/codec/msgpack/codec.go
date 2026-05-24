// Package msgpack wraps github.com/vmihailenco/msgpack/v5 as a codec.Codec
// implementation. The library exposes an Encoder/Decoder pair so this codec
// also implements StreamingCodec.
package msgpack

import (
	"bytes"
	"io"
	"slices"
	"sync"

	gomsgpack "github.com/vmihailenco/msgpack/v5"

	"github.com/kitsunium/sdk/internal/core/codec"
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

// maxRetainedBufBytes caps the size of *bytes.Buffer instances eligible
// for re-pooling. A one-off oversized payload would otherwise pin a
// large buffer for the lifetime of the pool's GC window. 256 KiB is the
// project-wide cap (matches the scratch-package threshold from the
// codec-perf-extreme initiative).
const maxRetainedBufBytes int = 256 << 10

// Package-level state: the codec singleton plus the hoisted MIME /
// extension tables (hoisted).
var (
	//: register the singleton and expose it as a typed package var.
	Codec codec.Codec = codec.Register(&msgpackCodec{})

	//: MIME table hoisted.
	mimeTypes = []string{"application/msgpack", "application/x-msgpack"}

	//: extension table hoisted for the same reason.
	extensions = []string{".msgpack", ".mpk"}

	//: bufferPool reuses *bytes.Buffer across Marshal calls. The
	//: vmihailenco lib's package-level Marshal allocates a fresh
	//: bytes.Buffer every call — this pool eliminates that allocation
	//: on steady-state hot paths.
	bufferPool = sync.Pool{
		New: func() any { return new(bytes.Buffer) },
	}

	//: bytesReaderPool reuses *bytes.Reader across Unmarshal calls.
	//: gomsgpack.Unmarshal internally builds a fresh bytes.Reader that
	//: escapes to heap through the io.Reader interface — pooling it
	//: removes that 16-byte struct allocation per call.
	bytesReaderPool = sync.Pool{
		New: func() any { return new(bytes.Reader) },
	}
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
	//: rent the output buffer from the pool. Type assertion is total
	//: because bufferPool.New always returns *bytes.Buffer; comma-ok
	//: silences KTN-VAR-TYPEASSERT and surfaces a clear panic if a
	//: future change ever broke the invariant.
	buf, ok := bufferPool.Get().(*bytes.Buffer)
	//: pool invariant guard — never expected to fail at runtime.
	if !ok {
		//: invariant broken — fail loud at the call site.
		panic("service/codec/msgpack: bufferPool yielded non-*bytes.Buffer")
	}
	//: start clean — pool may return a partially-filled buffer from a
	//: previous call that hit the cap-discard threshold.
	buf.Reset()
	//: rent the encoder from the library's exposed pool.
	enc := gomsgpack.GetEncoder()
	//: re-point the encoder at our pooled buffer.
	enc.Reset(buf)
	//: encode the value.
	merr := enc.Encode(v)
	//: return the encoder to the pool unconditionally — Encode failure
	//: leaves the encoder in a reusable state (Reset is the contract).
	gomsgpack.PutEncoder(enc)
	//: failure path — surface the typed sentinel.
	if merr != nil {
		//: drop the buffer back to the pool only if it's not oversized.
		releaseBuffer(buf)
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
	releaseBuffer(buf)
	//: success — bytes are the caller's now.
	return out, nil
}

// releaseBuffer returns buf to the pool unless its capacity exceeds the
// cap-discard threshold. Hoisted to avoid duplicating the cap check at
// every Put site and to keep Marshal under the linter's MAXLOC budget.
func releaseBuffer(buf *bytes.Buffer) {
	//: oversized buffers would pin large allocations for the lifetime
	//: of the pool's GC window — drop them instead.
	if buf.Cap() > maxRetainedBufBytes {
		//: orphan the buffer; the GC will reclaim it.
		return
	}
	//: pool expects a clean buffer.
	buf.Reset()
	//: return for the next caller.
	bufferPool.Put(buf)
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
	//: rent the bytes reader from the pool and re-point at data.
	//: Comma-ok asserts pool-invariant: bytesReaderPool.New always
	//: returns *bytes.Reader; a future change that broke this would
	//: panic with a clear diagnostic.
	r, ok := bytesReaderPool.Get().(*bytes.Reader)
	//: pool invariant guard — never expected to fail at runtime.
	if !ok {
		//: invariant broken — fail loud at the call site.
		panic("service/codec/msgpack: bytesReaderPool yielded non-*bytes.Reader")
	}
	r.Reset(data)
	//: rent the decoder from the library's exposed pool.
	dec := gomsgpack.GetDecoder()
	dec.Reset(r)
	//: decode into the caller's target.
	uerr := dec.Decode(v)
	//: return the decoder + reader to their pools unconditionally —
	//: both are reusable post-Reset.
	gomsgpack.PutDecoder(dec)
	bytesReaderPool.Put(r)
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

// NewEncoder wraps w in a streaming codec.Encoder.
func (*msgpackCodec) NewEncoder(w io.Writer) codec.Encoder {
	//: wrap the vmihailenco encoder.
	return &msgpackEncoder{inner: gomsgpack.NewEncoder(w)}
}

// NewDecoder wraps r in a streaming codec.Decoder.
func (*msgpackCodec) NewDecoder(r io.Reader) codec.Decoder {
	//: wrap the vmihailenco decoder.
	return &msgpackDecoder{inner: gomsgpack.NewDecoder(r)}
}
