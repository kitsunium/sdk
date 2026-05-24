// Package pem wraps encoding/pem as a codec.Codec implementation.
// PEM is block-structured: each wire message is a typed block ("CERTIFICATE",
// "PRIVATE KEY", etc.), so the codec operates on *pem.Block values.
package pem

import (
	"bytes"
	"encoding/base64"
	stdpem "encoding/pem"
	"slices"
	"sync"

	"github.com/kitsunium/sdk/internal/core/codec"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// Package-level constants: promotion-shape parameters + the cap-discard
// threshold for the Marshal-buffer pool. Grouped per KTN-CONST-ORDER.
const (
	// promotionBlockType is the Type label the facade promotion path stamps
	// when wrapping a non-PEM value (matches pkg/v1/codec/promote.go
	// pemPromotionBlockType).
	promotionBlockType = "JSON"

	// promotionLineLength is the base64 line width pem.Encode uses (each
	// line of base64 content within a PEM block is wrapped at this width).
	promotionLineLength int = 64

	// maxRetainedBufBytes caps the size of *bytes.Buffer instances re-pooled
	// by Marshal. A one-off oversized payload would otherwise pin a large
	// buffer for the lifetime of the pool's GC window. 256 KiB project-wide.
	maxRetainedBufBytes int = 256 << 10
)

// Package-level state: the codec singleton, MIME/extension tables, the
// cached promotion fast-path markers, and the Marshal-buffer pool.
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

	//: bufferPool reuses *bytes.Buffer across Marshal calls on the slow
	//: path (non-promotion blocks). pem.Encode allocates internally too
	//: but the outer bytes.Buffer header still escapes per call without
	//: this pool — the recycled storage saves one alloc per call.
	bufferPool = sync.Pool{
		New: func() any { return new(bytes.Buffer) },
	}
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
	//: rent the output buffer; pool guarantees a *bytes.Buffer.
	buf, ok := bufferPool.Get().(*bytes.Buffer)
	//: pool invariant guard — never expected to fail at runtime.
	if !ok {
		//: invariant broken — fail loud at the call site.
		panic("service/codec/pem: bufferPool yielded non-*bytes.Buffer")
	}
	//: start clean — pool may return a partially-filled buffer.
	buf.Reset()
	//: stdlib Encode writes directly; propagate any writer failure.
	if werr := stdpem.Encode(buf, block); werr != nil {
		//: cap-discard release; abort with the wrapped error.
		releaseBuffer(buf)
		//: wrap the stdlib error.
		return nil, errs.Wrap(werr, errs.WrapParams{
			Code:    CodePEMMarshalFailed,
			Reason:  "MARSHAL_FAILED",
			Public:  "PEM encoding failed",
			Private: "service/codec/pem.Marshal: encoding/pem.Encode returned an error",
		})
	}
	//: detach + release using the size-aware path (clone+repool small,
	//: orphan oversize).
	return detachAndRelease(buf), nil
}

// detachAndRelease pulls the encoded bytes out of buf and either clones-
// and-repools the small case or orphans the buffer untouched on the
// over-cap case (no full slices.Clone of a large payload).
func detachAndRelease(buf *bytes.Buffer) []byte {
	//: large buffers: orphan path — the returned slice IS the buffer's
	//: storage (caller-owned now), no clone, GC reclaims the buffer.
	if buf.Cap() > maxRetainedBufBytes {
		//: do NOT reset the buffer; it would zero the bytes we return.
		return buf.Bytes()
	}
	//: small buffer path: clone so the caller's slice doesn't alias
	//: the pooled buffer (next caller would overwrite it).
	out := slices.Clone(buf.Bytes())
	//: pool expects a clean buffer.
	buf.Reset()
	//: return for the next caller.
	bufferPool.Put(buf)
	//: caller-owned slice.
	return out
}

// releaseBuffer feeds buf back to the pool on the error path without
// cloning (caller discards the bytes). Same cap-discard semantics.
func releaseBuffer(buf *bytes.Buffer) {
	//: oversized buffers would pin large allocations; orphan them.
	if buf.Cap() > maxRetainedBufBytes {
		//: GC reclaims; pool stays cap-bounded.
		return
	}
	//: pool expects a clean buffer.
	buf.Reset()
	//: return for the next caller.
	bufferPool.Put(buf)
}

// marshalPromotionBlock emits the wire bytes for a "JSON"-typed PEM
// block with no headers — the exact shape pkg/v1/codec/promote.go
// stamps. The output is byte-identical to pem.Encode for this shape
// (BEGIN marker, base64-encoded payload split into 64-char lines,
// END marker, trailing newline). Saves pem.Encode's intermediate
// bytes.Buffer + lineBreaker + base64.NewEncoder allocations.
//
// Implementation: one-shot base64.Encode into the output buffer's
// tail, then a forward walk that inserts '\n' every 64 bytes. The
// earlier per-chunk loop called base64.StdEncoding.Encode N times
// (one per 48-byte input chunk) — each call paid function-call
// overhead + reset internal state. The single-encode + insert
// approach makes one call total then memmove's lines into their
// final slots.
func marshalPromotionBlock(payload []byte) []byte {
	//: base64.EncodedLen gives the exact encoded body length.
	bodyLen := base64.StdEncoding.EncodedLen(len(payload))
	//: ceiling-divide so a partial last line still counts.
	lineCount := (bodyLen + promotionLineLength - 1) / promotionLineLength
	//: total bytes = BEGIN marker + body + 1 newline per line + END marker.
	total := len(pemJSONBegin) + bodyLen + lineCount + len(pemJSONEnd)
	//: allocate the output buffer at exact size; reslicing within
	//: this storage avoids further appends/grows.
	out := make([]byte, 0, total)
	//: emit the BEGIN marker (includes trailing \n).
	out = append(out, pemJSONBegin...)
	//: snapshot where the base64 body starts so the line-split walk
	//: knows the body's left edge inside out.
	bodyStart := len(out)
	//: extend out by the encoded body length AND every line's '\n'
	//: in one slice grow, then encode + split in place.
	out = out[:bodyStart+bodyLen+lineCount]
	//: encode the whole payload in ONE call into the body region.
	//: Working slice writes into out[bodyStart : bodyStart+bodyLen].
	base64.StdEncoding.Encode(out[bodyStart:bodyStart+bodyLen], payload)
	//: walk backwards inserting '\n' so we don't have to track
	//: shifting offsets — the byte at lineCount × promotionLineLength
	//: needs to land at lineCount × (promotionLineLength + 1).
	insertNewlinesEvery64(out[bodyStart:], bodyLen, lineCount)
	//: emit the END marker (includes trailing \n).
	out = append(out, pemJSONEnd...)
	//: caller owns the bytes.
	return out
}

// insertNewlinesEvery64 walks the base64 body region in `body` (which
// already has lineCount extra bytes reserved at the tail) and inserts
// '\n' after every promotionLineLength (= 64) base64 characters. The
// walk is backwards so earlier byte positions stay valid until they
// are moved — same shift-right idiom encoding/pem's lineBreaker uses
// internally, except in one pass without per-byte function calls.
func insertNewlinesEvery64(body []byte, bodyLen, lineCount int) {
	//: bytesWritten tracks the running insert position from the right.
	//: After the loop, body[:bodyLen+lineCount] is fully populated.
	dst := bodyLen + lineCount - 1
	//: walk lines in reverse: last line (possibly partial) first.
	for line := lineCount - 1; line >= 0; line-- {
		//: line `line` in the encoded body starts at index `line*64`
		//: in the un-inserted region. Its length is 64 except for the
		//: last line which may be shorter (bodyLen - line*64).
		lineStart := line * promotionLineLength
		lineEnd := lineStart + promotionLineLength
		//: clamp the final line to the actual body length via min.
		lineEnd = min(lineEnd, bodyLen)
		//: the line's trailing '\n' lands at dst.
		body[dst] = '\n'
		dst--
		//: copy line bytes from right to left into [dst-lineLen+1 ... dst].
		lineLen := lineEnd - lineStart
		//: copy() handles overlapping src/dst correctly (memmove semantics).
		copy(body[dst-lineLen+1:dst+1], body[lineStart:lineEnd])
		//: advance dst past the line we just placed.
		dst -= lineLen
	}
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
