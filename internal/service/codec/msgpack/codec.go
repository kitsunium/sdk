// Package msgpack wraps github.com/vmihailenco/msgpack/v5 as a codec.Codec
// implementation. The library exposes an Encoder/Decoder pair so this codec
// also implements StreamingCodec.
package msgpack

import (
	"io"
	"slices"

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

// Package-level state: the codec singleton plus the hoisted MIME /
// extension tables (hoisted to satisfy KTN-VAR-CONSTSLICE).
var (
	//: register the singleton and expose it as a typed package var.
	Codec codec.Codec = codec.Register(&msgpackCodec{})

	//: MIME table hoisted to satisfy KTN-VAR-CONSTSLICE.
	mimeTypes = []string{"application/msgpack", "application/x-msgpack"}

	//: extension table hoisted for the same reason.
	extensions = []string{".msgpack", ".mpk"}
)

// msgpackCodec is the concrete Codec implementation for MessagePack.
type msgpackCodec struct{}

// New returns a MessagePack codec instance.
//
// Returns:
//   - codec.Codec: a fresh stateless codec.
func New() (c codec.Codec) {
	//: stateless — one singleton is enough for the whole process.
	return Codec
}

// Name implements codec.Codec.
//
// Returns:
//   - string: always "msgpack".
func (*msgpackCodec) Name() (name string) {
	//: canonical identifier.
	return "msgpack"
}

// MIMETypes lists every MIME alias.
//
// Returns:
//   - []string: canonical MIME first.
func (*msgpackCodec) MIMETypes() (mimes []string) {
	//: hand back the package-level slice.
	return slices.Clone(mimeTypes)
}

// Extensions lists every file extension.
//
// Returns:
//   - []string: canonical extension first.
func (*msgpackCodec) Extensions() (exts []string) {
	//: hand back the package-level slice.
	return slices.Clone(extensions)
}

// Marshal serialises v as MessagePack bytes.
//
// Params:
//   - v: value vmihailenco/msgpack/v5 supports.
//
// Returns:
//   - []byte: the encoded MessagePack bytes.
//   - error: MarshalFailed wrapping the library cause on failure.
func (*msgpackCodec) Marshal(v any) (data []byte, err error) {
	//: delegate to the library for the actual encoding.
	out, merr := gomsgpack.Marshal(v)
	//: success fast-path.
	if merr == nil {
		//: return the encoded bytes verbatim.
		return out, nil
	}
	//: wrap the library error for reason-based matching.
	return nil, errs.Wrap(merr, errs.WrapParams{
		Code:    CodeMsgPackMarshalFailed,
		Reason:  "MARSHAL_FAILED",
		Public:  "MessagePack encoding failed",
		Private: "service/codec/msgpack.Marshal: vmihailenco/msgpack/v5 returned an error",
	})
}

// Unmarshal parses data as MessagePack into v.
//
// Params:
//   - data: MessagePack bytes.
//   - v: pointer to the destination value.
//
// Returns:
//   - error: UnmarshalFailed wrapping the library cause on failure.
func (*msgpackCodec) Unmarshal(data []byte, v any) (err error) {
	//: cap input size so attacker-controlled payloads cannot exhaust RAM
	//: during pre-allocation from huge declared length fields (finding #17).
	if len(data) > maxMsgPackBytes {
		//: surface an UNMARSHAL_FAILED with a diagnostic Private message.
		return errs.Wrap(nil, errs.WrapParams{
			Code:    CodeMsgPackUnmarshalFailed,
			Reason:  "UNMARSHAL_FAILED",
			Public:  "MessagePack input exceeds size limit",
			Private: "service/codec/msgpack.Unmarshal: len(data) exceeds maxMsgPackBytes",
		}, errs.Int("len", len(data)), errs.Int("cap", maxMsgPackBytes))
	}
	//: delegate to the library for the actual decoding.
	uerr := gomsgpack.Unmarshal(data, v)
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
//
// Params:
//   - w: destination writer.
//
// Returns:
//   - codec.Encoder: a streaming MessagePack encoder bound to w.
func (*msgpackCodec) NewEncoder(w io.Writer) (enc codec.Encoder) {
	//: wrap the vmihailenco encoder.
	return &msgpackEncoder{inner: gomsgpack.NewEncoder(w)}
}

// NewDecoder wraps r in a streaming codec.Decoder.
//
// Params:
//   - r: source reader.
//
// Returns:
//   - codec.Decoder: a streaming MessagePack decoder bound to r.
func (*msgpackCodec) NewDecoder(r io.Reader) (dec codec.Decoder) {
	//: wrap the vmihailenco decoder.
	return &msgpackDecoder{inner: gomsgpack.NewDecoder(r)}
}
