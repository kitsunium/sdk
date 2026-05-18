// Package baseenc — IO adapters used by the streaming pipeline.
package baseenc

import (
	"bytes"
	"io"
)

// nopWriteCloser turns an io.Writer into an io.WriteCloser with a no-op Close.
// Used by variants whose stdlib stream encoder is a plain io.Writer
// (hex.NewEncoder) so the codec.Encoder interface can call Close uniformly.
type nopWriteCloser struct {
	io.Writer
}

// Close satisfies io.WriteCloser without releasing the wrapped writer.
func (nopWriteCloser) Close() error {
	//: caller owns the wrapped writer.
	return nil
}

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
func (b *bufferingWriter) Close() error {
	//: encode the accumulated bytes through the variant's path.
	encoded := b.codec.encodeBytes(b.buf.Bytes())
	//: flush to the caller's writer.
	_, werr := b.dst.Write(encoded)
	//: propagate the writer's error verbatim — caller will wrap.
	return werr
}
