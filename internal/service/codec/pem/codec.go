// Package pem wraps encoding/pem as a codec.Codec implementation.
// PEM is block-structured: each wire message is a typed block ("CERTIFICATE",
// "PRIVATE KEY", etc.), so the codec operates on *pem.Block values.
package pem

import (
	"bytes"
	"encoding/base64"
	stdpem "encoding/pem"
	"slices"

	"github.com/kitsunium/sdk/internal/core/codec"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// promotionBlockType is the Type label the facade promotion path stamps
// when wrapping a non-PEM value (matches pkg/v1/codec/promote.go
// pemPromotionBlockType).
const promotionBlockType = "JSON"

// promotionLineLength is the base64 line width pem.Encode uses (each
// line of base64 content within a PEM block is wrapped at this width).
const promotionLineLength int = 64

// promotionInputChunk is the raw-byte input width that produces one
// promotionLineLength-byte base64 line. Each block of 48 input bytes
// encodes to a 64-byte base64 line (a 4-out-per-3-in ratio).
const promotionInputChunk int = 48

// Package-level state: the codec singleton, MIME/extension tables, and
// the cached promotion fast-path markers.
var (
	//: register the singleton and expose it as a typed package var.
	Codec codec.Codec = codec.Register(&pemCodec{})

	//: no official IANA type exists; the de facto value is application/x-pem-file.
	mimeTypes = []string{"application/x-pem-file"}

	//: .pem is the canonical extension; .crt and .key are historical aliases.
	extensions = []string{".pem", ".crt", ".key"}

	//: pemJSONBegin is the BEGIN marker for a "JSON"-typed PEM block.
	//: Cached so the promotion fast-path never re-builds it.
	pemJSONBegin = []byte("-----BEGIN " + promotionBlockType + "-----\n")

	//: pemJSONEnd is the END marker for a "JSON"-typed PEM block.
	pemJSONEnd = []byte("-----END " + promotionBlockType + "-----\n")
)

// pemCodec is the concrete Codec implementation for PEM.
type pemCodec struct{}

// Block re-exports encoding/pem.Block so consumers do not need to import
// the stdlib package directly.
type Block = stdpem.Block

// New returns a PEM codec instance.
func New() codec.Codec {
	//: stateless — one singleton is enough for the whole process.
	return Codec
}

// Name implements codec.Codec.
func (*pemCodec) Name() string {
	//: canonical identifier.
	return "pem"
}

// MIMETypes lists every MIME alias.
func (*pemCodec) MIMETypes() []string {
	//: hand back the package-level slice.
	return slices.Clone(mimeTypes)
}

// Extensions lists every file extension.
func (*pemCodec) Extensions() []string {
	//: hand back the package-level slice.
	return slices.Clone(extensions)
}

// Marshal encodes a *pem.Block into PEM bytes.
func (*pemCodec) Marshal(v any) (encoded []byte, err error) {
	//: type-gate: PEM only accepts a typed block.
	block, ok := v.(*stdpem.Block)
	//: loud failure when the caller passed the wrong type OR a nil block.
	if !ok || block == nil {
		//: shape-the-input rejection uses the VALUE_INVALID sentinel.
		return nil, errs.Wrap(nil, errs.WrapParams{
			Code:    CodePEMValueInvalid,
			Reason:  "VALUE_INVALID",
			Public:  "PEM codec requires a non-nil *pem.Block value",
			Private: "service/codec/pem.Marshal: argument is not a non-nil *pem.Block",
		})
	}
	//: promotion-shape fast-path: Type=="JSON" + no Headers is the
	//: canonical block pkg/v1/codec/promote.go produces. Bypass
	//: pem.Encode (which does its own bytes.Buffer + lineBreaker +
	//: base64.NewEncoder dance) and hand-write the wire bytes.
	if block.Type == promotionBlockType && len(block.Headers) == 0 {
		//: dedicated fast-path emits identical wire bytes to pem.Encode.
		return marshalPromotionBlock(block.Bytes), nil
	}
	//: encode into a buffer so the caller gets []byte.
	var buf bytes.Buffer
	//: stdlib Encode writes directly; propagate any writer failure.
	if werr := stdpem.Encode(&buf, block); werr != nil {
		//: wrap the stdlib error.
		return nil, errs.Wrap(werr, errs.WrapParams{
			Code:    CodePEMMarshalFailed,
			Reason:  "MARSHAL_FAILED",
			Public:  "PEM encoding failed",
			Private: "service/codec/pem.Marshal: encoding/pem.Encode returned an error",
		})
	}
	//: hand back the buffered bytes.
	return buf.Bytes(), nil
}

// marshalPromotionBlock emits the wire bytes for a "JSON"-typed PEM
// block with no headers — the exact shape pkg/v1/codec/promote.go
// stamps. The output is byte-identical to pem.Encode for this shape
// (BEGIN marker, base64-encoded payload split into 64-char lines,
// END marker, trailing newline). Saves pem.Encode's intermediate
// bytes.Buffer + lineBreaker + base64.NewEncoder allocations.
func marshalPromotionBlock(payload []byte) []byte {
	//: base64.EncodedLen gives the exact encoded body length.
	bodyLen := base64.StdEncoding.EncodedLen(len(payload))
	//: ceiling-divide so a partial last line still counts.
	lineCount := (bodyLen + promotionLineLength - 1) / promotionLineLength
	//: total bytes = BEGIN marker + body + 1 newline per line + END marker.
	total := len(pemJSONBegin) + bodyLen + lineCount + len(pemJSONEnd)
	out := make([]byte, 0, total)
	//: emit the BEGIN marker (includes trailing \n).
	out = append(out, pemJSONBegin...)
	//: encode the payload in 48-byte input chunks → 64-byte output
	//: lines (the exact width pem.Encode picks via lineBreaker).
	for off := 0; off < len(payload); off += promotionInputChunk {
		//: clamp the input slice to the remaining payload (last chunk
		//: may be partial — min handles the partial-chunk boundary).
		end := min(off+promotionInputChunk, len(payload))
		//: prepare encode-destination slice extension.
		startLen := len(out)
		encLen := base64.StdEncoding.EncodedLen(end - off)
		out = out[:startLen+encLen]
		//: encode directly into the output buffer.
		base64.StdEncoding.Encode(out[startLen:], payload[off:end])
		//: line terminator after each base64 line.
		out = append(out, '\n')
	}
	//: emit the END marker (includes trailing \n).
	out = append(out, pemJSONEnd...)
	//: caller owns the bytes.
	return out
}

// Append encodes a *pem.Block as PEM bytes and appends them to dst.
// Implements the optional codec.Appender interface so hot-path callers
// can stitch PEM blocks into a larger framed buffer (multi-block cert
// chain construction) without an intermediate allocation per block.
func (c *pemCodec) Append(dst []byte, v any) (appended []byte, err error) {
	//: delegate to Marshal so the type-gate + wrap/error contract has
	//: a single source.
	encoded, merr := c.Marshal(v)
	//: surface any encoding failure without touching dst.
	if merr != nil {
		//: return the untouched buffer plus the wrapped error.
		return dst, merr
	}
	//: append the encoded bytes onto the caller's buffer.
	return append(dst, encoded...), nil
}

// Unmarshal parses the first PEM block from data into v.
func (*pemCodec) Unmarshal(data []byte, v any) error {
	//: target must be **pem.Block so we can populate it.
	dst, ok := v.(**stdpem.Block)
	//: shape-the-target rejection — wrong type or nil outer pointer panics later.
	if !ok || dst == nil {
		//: loud failure when the caller passed the wrong type.
		return errs.Wrap(nil, errs.WrapParams{
			Code:    CodePEMValueInvalid,
			Reason:  "VALUE_INVALID",
			Public:  "PEM codec requires a non-nil **pem.Block target",
			Private: "service/codec/pem.Unmarshal: target is not a non-nil **pem.Block",
		})
	}
	//: Decode returns the first block and any trailing bytes.
	block, _ := stdpem.Decode(data)
	//: no block = malformed input.
	if block == nil {
		//: loud failure — caller must be notified.
		return errs.Wrap(nil, errs.WrapParams{
			Code:    CodePEMUnmarshalFailed,
			Reason:  "UNMARSHAL_FAILED",
			Public:  "PEM decoding failed",
			Private: "service/codec/pem.Unmarshal: encoding/pem.Decode returned no block",
		})
	}
	//: publish the decoded block through the caller's pointer.
	*dst = block
	//: nothing to wrap.
	return nil
}
