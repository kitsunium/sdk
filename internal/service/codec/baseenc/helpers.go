// Package baseenc — IO adapters used by the streaming pipeline.
package baseenc

import (
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
