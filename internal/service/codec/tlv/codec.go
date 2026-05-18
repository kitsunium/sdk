// Package tlv implements a self-describing Type-Length-Value codec.
// Every value is encoded as a 1-byte tag, an unsigned LEB128 length, and
// a tag-specific value payload. Composite tags (slice/map/struct) recurse
// into nested TLV records. Reflection drives both directions so arbitrary
// Go values fit through the same Marshal/Unmarshal interface.
//
// The codec is registered under Name "tlv" and the canonical
// "application/x-tlv" MIME type at package import time.
package tlv

import (
	"io"
	"slices"

	"github.com/kitsunium/sdk/internal/core/codec"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// Package-level state: the codec singleton plus the hoisted MIME /
// extension tables.
var (
	//: register the singleton and expose it as a typed package var.
	Codec codec.Codec = codec.Register(&tlvCodec{})

	//: MIME table hoisted so MIMETypes() can hand back a slices.Clone.
	mimeTypes = []string{"application/x-tlv", "application/vnd.tlv"}

	//: extension table hoisted for the same reason.
	extensions = []string{".tlv"}
)

// tlvCodec is the concrete Codec implementation for TLV.
type tlvCodec struct{}

// New returns the TLV codec singleton.
func New() codec.Codec {
	//: stateless — one singleton is enough for the whole process.
	return Codec
}

// Name implements codec.Codec.
func (*tlvCodec) Name() string {
	//: canonical identifier.
	return "tlv"
}

// MIMETypes lists every MIME alias.
func (*tlvCodec) MIMETypes() []string {
	//: defensive copy so callers cannot mutate the package-level slice.
	return slices.Clone(mimeTypes)
}

// Extensions lists every file extension.
func (*tlvCodec) Extensions() []string {
	//: defensive copy so callers cannot mutate the package-level slice.
	return slices.Clone(extensions)
}

// Marshal serialises v as TLV bytes by allocating a fresh buffer.
func (c *tlvCodec) Marshal(v any) (encoded []byte, err error) {
	//: delegate to Append against a nil buffer so the encode path has a
	//: single source of truth.
	out, aerr := c.Append(nil, v)
	//: surface any encode failure verbatim — Append already wrapped.
	if aerr != nil {
		//: discard whatever Append returned to keep the contract clean.
		return nil, aerr
	}
	//: hand back the freshly-allocated buffer.
	return out, nil
}

// Unmarshal parses data as TLV into v, which must be a non-nil pointer.
func (*tlvCodec) Unmarshal(data []byte, v any) error {
	//: cap input size so attacker-controlled payloads cannot exhaust RAM.
	if len(data) > maxTLVBytes {
		//: surface the size sentinel with diagnostic Fields.
		return errs.Wrap(nil, errs.WrapParams{
			Code:    CodeTLVSizeExceeded,
			Reason:  "SIZE_EXCEEDED",
			Public:  "TLV input exceeds size limit",
			Private: "service/codec/tlv.Unmarshal: len(data) exceeds maxTLVBytes",
		}, errs.Int("len", len(data)), errs.Int("cap", maxTLVBytes))
	}
	//: delegate to the decode helper; it handles the pointer-shape contract.
	return decodeRoot(data, v)
}

// Append encodes v as TLV and appends the bytes onto dst. Implements the
// optional codec.Appender interface so hot-path callers can write into a
// recycled buffer.
func (*tlvCodec) Append(dst []byte, v any) (appended []byte, err error) {
	//: snapshot dst's prior length so a mid-encode failure can roll back
	//: and honour the Appender contract.
	origLen := len(dst)
	//: drive the recursive encoder; depth starts at zero.
	out, eerr := encodeValue(dst, v, 0)
	//: success fast-path.
	if eerr == nil {
		//: hand back the (possibly re-allocated) buffer.
		return out, nil
	}
	//: rollback to the caller's prior buffer length on failure.
	return dst[:origLen], eerr
}

// NewEncoder wraps w in a streaming codec.Encoder. Each Encode call emits
// one independent TLV record onto the writer.
func (*tlvCodec) NewEncoder(w io.Writer) codec.Encoder {
	//: encoder owns no other state.
	return &tlvEncoder{w: w}
}

// NewDecoder wraps r in a streaming codec.Decoder. Each Decode call
// consumes one TLV record from the reader.
func (*tlvCodec) NewDecoder(r io.Reader) codec.Decoder {
	//: decoder owns no other state.
	return &tlvDecoder{r: r}
}
