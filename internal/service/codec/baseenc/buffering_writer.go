// Package baseenc — buffered writer used by variants whose stdlib has no
// streaming encoder (base16 uppercase). Held in its own file so the
// KTN-STRUCT-ONEFILE rule sees exactly one exported-shape struct per file.
package baseenc

import (
	"bytes"
	"io"
	"strconv"

	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// bufferingWriter accumulates writes until Close then runs the codec's
// encodeBytes path against the buffered payload. Used by variants whose
// stdlib has no streaming encoder (base16 uppercase) — encoding the whole
// payload at Close keeps the byte-stream legal without resorting to a
// per-byte upper-case shim that would corrupt the partial groups stdlib
// hex.Encoder emits.
type bufferingWriter struct {
	dst   io.Writer
	codec *baseencCodec
	buf   bytes.Buffer
}

// Write appends p to the internal buffer; encoding happens at Close.
func (b *bufferingWriter) Write(p []byte) (n int, err error) {
	//: bytes.Buffer.Write never returns an error.
	return b.buf.Write(p)
}

// Close encodes the buffered payload and flushes to the wrapped writer.
// A short write (n < len(encoded)) is surfaced as io.ErrShortWrite wrapped
// with the variant's marshal-failed sentinel so callers see a precise
// "encoded payload was partially written" diagnostic instead of a silent
// truncation.
func (b *bufferingWriter) Close() error {
	//: base-conversion variants are O(n²) — refuse an oversized buffered
	//: payload before the quadratic encode runs, matching the Marshal cap so
	//: streaming cannot bypass it (CWE-400).
	if isBaseConversion(b.codec.variant) && b.buf.Len() > maxConvBytes {
		//: surface the shared size sentinel.
		return convSizeExceeded("service/codec/baseenc.bufferingWriter.Close: base-conversion buffered input exceeds maxConvBytes")
	}
	//: encode the accumulated bytes through the variant's path.
	encoded := b.codec.encodeBytes(b.buf.Bytes())
	//: flush to the caller's writer.
	n, werr := b.dst.Write(encoded)
	//: surface writer failures via the marshal-failed wrap.
	if werr != nil {
		//: wrap so the dotted-quad code propagates.
		return errs.Wrap(werr, errs.WrapParams{
			Code:    CodeBaseEncMarshalFailed,
			Reason:  "BASE_ENC_MARSHAL_FAILED",
			Public:  "base-N encoding failed",
			Private: "service/codec/baseenc.bufferingWriter.Close: underlying writer returned an error",
		}, errs.String("wrote", strconv.Itoa(n)), errs.String("want", strconv.Itoa(len(encoded))))
	}
	//: a short write (n < len(encoded)) means the writer accepted only
	//: part of the encoded payload — surface as io.ErrShortWrite so the
	//: caller knows the stream was truncated.
	if n < len(encoded) {
		//: wrap io.ErrShortWrite with the marshal-failed sentinel.
		return errs.Wrap(io.ErrShortWrite, errs.WrapParams{
			Code:    CodeBaseEncMarshalFailed,
			Reason:  "BASE_ENC_MARSHAL_FAILED",
			Public:  "base-N encoding failed",
			Private: "service/codec/baseenc.bufferingWriter.Close: short write of encoded payload",
		}, errs.String("wrote", strconv.Itoa(n)), errs.String("want", strconv.Itoa(len(encoded))))
	}
	//: success — every encoded byte reached the wrapped writer.
	return nil
}
