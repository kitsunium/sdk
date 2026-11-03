package pem

import (
	"encoding/base64"
	stdpem "encoding/pem"
	"slices"
	"strings"

	"github.com/kitsunium/sdk/internal/core/data/codec"
	corepem "github.com/kitsunium/sdk/internal/core/data/codec/pem"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// Package-level constants: the framing encoding/pem writes. Grouped per
// KTN-CONST-ORDER.
const (
	// promotionLineLength is the base64 line width pem.Encode uses (each
	// line of base64 content within a PEM block is wrapped at this width).
	promotionLineLength int = 64

	// pemBegin opens the BEGIN line, before the block's type.
	pemBegin = "-----BEGIN "

	// pemEnd opens the END line, before the block's type.
	pemEnd = "-----END "

	// pemDashes closes the BEGIN and END lines, with their newline.
	pemDashes = "-----\n"

	// procTypeHeader is the header encoding/pem writes before the others
	// (RFC 1421 §4.6.1.1).
	procTypeHeader = "Proc-Type"
)

// Package-level state: the codec singleton, built with its hoisted extension
// table — the initialiser names the table, which is what orders it before
// Register reads it (see core/data/codec.Register).
var (
	//: register the singleton and expose it as a typed package var.
	Codec codec.Codec = codec.Register(&pemCodec{extensions: extensions})

	//: .pem is the canonical extension; .crt and .key are historical aliases.
	//: Hoisted (KTN-VAR-CONSTSLICE) and handed to the singleton by Codec's own
	//: initialiser: that reference is what orders the table before Register
	//: reads it, whatever the declaration order (see core/data/codec.Register).
	extensions = []string{".pem", ".crt", ".key"}
)

// pemCodec is the concrete Codec implementation for PEM.
type pemCodec struct {
	// extensions is the extension table this instance reports — the package
	// table for the registered singleton, which is built with it.
	extensions []string
}

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
	//: no official IANA type exists; the de facto value is application/x-pem-file.
	//: a fresh literal the caller owns; no package variable is read when Register calls this.
	return []string{"application/x-pem-file"}
}

// Extensions lists every file extension.
func (c *pemCodec) Extensions() []string {
	//: a copy the caller owns; the table itself came through the initialiser.
	return slices.Clone(c.extensions)
}

// Marshal encodes a *pem.Block into PEM bytes: encoding/pem.Encode's bytes,
// written into one allocation of their exact size.
func (*pemCodec) Marshal(v any) (encoded []byte, err error) {
	//: a non-nil *pem.Block, or nil for checkBlock to refuse.
	block, _ := v.(*stdpem.Block)
	//: the type gate and the header check, before anything is allocated.
	berr := checkBlock(block, "service/data/codec/pem.Marshal")
	//: refused.
	if berr != nil {
		//: the wrapped refusal.
		return nil, berr
	}
	//: exactly sized, then written in place.
	return appendBlock(make([]byte, 0, blockLen(block)), block), nil
}

// Append encodes a *pem.Block as PEM bytes and appends them to dst.
// Implements the optional codec.Appender interface so hot-path callers
// can stitch PEM blocks into a larger framed buffer (multi-block cert
// chain construction): the block is written into dst itself, grown once to
// the exact size when it lacks the room. On a refusal dst is returned as it
// was given — the header check runs before a byte is written.
func (*pemCodec) Append(dst []byte, v any) (appended []byte, err error) {
	//: a non-nil *pem.Block, or nil for checkBlock to refuse.
	block, _ := v.(*stdpem.Block)
	//: a forwarder small enough to inline, as Append always was.
	return appendValue(dst, block)
}

// appendValue is Append's body: the gate, then the block written in place.
func appendValue(dst []byte, block *stdpem.Block) (appended []byte, err error) {
	//: the same gate as Marshal.
	berr := checkBlock(block, "service/data/codec/pem.Append")
	//: refused: dst untouched.
	if berr != nil {
		//: the caller's bytes and the wrapped refusal.
		return dst, berr
	}
	//: room for the whole block, then the block written in place.
	dst = slices.Grow(dst, blockLen(block))
	//: the caller's prefix, then the block.
	return appendBlock(dst, block), nil
}

// checkBlock is the gate Marshal, Append and the stream encoder share: the
// value must have been a non-nil *pem.Block — block is nil when it was
// anything else — and no header key may hold a colon, the one block
// encoding/pem.Encode refuses, checked as it checks it, before any output.
func checkBlock(block *stdpem.Block, caller string) error {
	//: anything but a block, or a nil block.
	if block == nil {
		//: the dedicated sentinel.
		return errs.Wrap(nil, errs.WrapParams{
			Code:    corepem.CodePEMValueInvalid,
			Reason:  "VALUE_INVALID",
			Public:  "PEM codec requires a non-nil *pem.Block value",
			Private: caller + ": argument is not a non-nil *pem.Block",
		})
	}
	//: encoding/pem.Encode's own refusal, kept with its code.
	for k := range block.Headers {
		//: a colon would split the header line where a reader splits it.
		if strings.Contains(k, ":") {
			//: the marshal failure encoding/pem reports.
			return errs.Wrap(nil, errs.WrapParams{
				Code:    corepem.CodePEMMarshalFailed,
				Reason:  "MARSHAL_FAILED",
				Public:  "PEM encoding failed",
				Private: caller + ": a header key contains a colon",
			})
		}
	}
	//: an encodable block.
	return nil
}

// appendBlock appends block as encoding/pem.Encode writes it: the BEGIN line,
// the headers — Proc-Type first (RFC 1421 §4.6.1.1), the others sorted by
// key — and a blank line when there are any, the body in base64 lines of 64
// characters, the END line.
func appendBlock(dst []byte, block *stdpem.Block) []byte {
	//: the BEGIN line.
	dst = append(dst, pemBegin...)
	dst = append(dst, block.Type...)
	dst = append(dst, pemDashes...)
	//: the headers and the blank line ending them, when there are any.
	if len(block.Headers) > 0 {
		//: Proc-Type first, when present.
		if procType, ok := block.Headers[procTypeHeader]; ok {
			//: the header line.
			dst = appendHeader(dst, procTypeHeader, procType)
		}
		//: the others in key order.
		for _, k := range sortedHeaderKeys(block.Headers) {
			//: the header line.
			dst = appendHeader(dst, k, block.Headers[k])
		}
		//: the blank line before the body.
		dst = append(dst, '\n')
	}
	//: the body, then the END line.
	dst = appendBody(dst, block.Bytes)
	dst = append(dst, pemEnd...)
	dst = append(dst, block.Type...)
	//: the closing dashes and the final newline.
	return append(dst, pemDashes...)
}

// appendHeader appends the header line "k: v\n".
func appendHeader(dst []byte, k, v string) []byte {
	//: key, separator, value, newline.
	dst = append(dst, k...)
	dst = append(dst, ": "...)
	dst = append(dst, v...)
	//: the line's end.
	return append(dst, '\n')
}

// sortedHeaderKeys returns the header keys but Proc-Type, sorted — the order
// encoding/pem.Encode writes them in.
func sortedHeaderKeys(headers map[string]string) []string {
	//: every key but Proc-Type, which is written first.
	keys := make([]string, 0, len(headers))
	//: collected in map order, sorted below.
	for k := range headers {
		//: Proc-Type is not among them.
		if k != procTypeHeader {
			keys = append(keys, k)
		}
	}
	//: byte order, as slices.Sort orders strings.
	slices.Sort(keys)
	//: the order to write them in.
	return keys
}

// appendBody appends payload in base64, broken into lines of 64 characters,
// each ended by '\n' — encoding/pem's lineBreaker. An empty payload writes
// nothing. One base64 encode into the tail, then the lines moved into place.
func appendBody(dst, payload []byte) []byte {
	//: the encoded length and the newline count.
	bodyLen := base64.StdEncoding.EncodedLen(len(payload))
	lineCount := (bodyLen + promotionLineLength - 1) / promotionLineLength
	//: reserve both, then encode at the start of the reservation.
	bodyStart := len(dst)
	dst = slices.Grow(dst, bodyLen+lineCount)[:bodyStart+bodyLen+lineCount]
	base64.StdEncoding.Encode(dst[bodyStart:bodyStart+bodyLen], payload)
	//: spread the encoded run into 64-character lines, back to front.
	insertNewlinesEvery64(dst[bodyStart:], bodyLen, lineCount)
	//: the body in place.
	return dst
}

// blockLen is the exact length appendBlock writes for block.
func blockLen(block *stdpem.Block) int {
	//: the BEGIN and END lines.
	n := len(pemBegin) + len(pemEnd) + 2*(len(block.Type)+len(pemDashes))
	//: each header line and the blank line, when there are any.
	if len(block.Headers) > 0 {
		//: the blank line.
		n++
		//: "k: v\n" per header.
		for k, v := range block.Headers {
			n += len(k) + len(": ") + len(v) + 1
		}
	}
	//: the base64 body and one newline per line of it.
	bodyLen := base64.StdEncoding.EncodedLen(len(block.Bytes))
	//: the exact size.
	return n + bodyLen + (bodyLen+promotionLineLength-1)/promotionLineLength
}

// Unmarshal parses the first PEM block from data into v.
func (*pemCodec) Unmarshal(data []byte, v any) error {
	//: a nil or non-pointer target is the caller's error, refused first (#1101).
	if terr := codec.CheckTarget(v); terr != nil {
		return terr
	}
	//: target must be **pem.Block so we can populate it.
	dst, ok := v.(**stdpem.Block)
	//: shape-the-target rejection — wrong type or nil outer pointer panics later.
	if !ok || dst == nil {
		//: loud failure when the caller passed the wrong type.
		return errs.Wrap(nil, errs.WrapParams{
			Code:    corepem.CodePEMValueInvalid,
			Reason:  "VALUE_INVALID",
			Public:  "PEM codec requires a non-nil **pem.Block target",
			Private: "service/data/codec/pem.Unmarshal: target is not a non-nil **pem.Block",
		})
	}
	//: Decode returns the first block and any trailing bytes.
	block, _ := stdpem.Decode(data)
	//: no block = malformed input.
	if block == nil {
		//: loud failure — caller must be notified.
		return errs.Wrap(nil, errs.WrapParams{
			Code:    corepem.CodePEMUnmarshalFailed,
			Reason:  "UNMARSHAL_FAILED",
			Public:  "PEM decoding failed",
			Private: "service/data/codec/pem.Unmarshal: encoding/pem.Decode returned no block",
		})
	}
	//: publish the decoded block through the caller's pointer.
	*dst = block
	//: nothing to wrap.
	return nil
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
