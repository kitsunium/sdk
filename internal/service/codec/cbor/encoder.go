// Package cbor — the streaming encoder: one data item per Encode, written in
// one Write once it is completely encoded, so a value that cannot be encoded
// writes nothing.
package cbor

import (
	"io"

	"github.com/kitsunium/sdk/internal/core/codec/scratch"
)

// cborEncoder writes CBOR data items to a writer, one per Encode.
type cborEncoder struct {
	// w receives each encoded item.
	w io.Writer
}

// Encode encodes v and writes it as one data item.
func (e *cborEncoder) Encode(v any) error {
	buf := scratch.AcquireBuffer()
	b, err := appendValue(buf.AvailableBuffer(), v, walkDepth{})
	//: only a complete item is written.
	if err == nil {
		err = e.write(b)
	}
	keepScratch(buf, b)
	//: MARSHAL_FAILED, or the cause's own code when it is an SDK error.
	return err
}

// write hands one encoded item to the writer.
func (e *cborEncoder) write(b []byte) error {
	_, err := e.w.Write(b)
	//: a short write is reported by the writer as an error.
	if err != nil {
		//: the writer's failure, kept as the cause.
		return encodeCause(err, "writing an encoded item failed")
	}
	//: written.
	return nil
}

// Close is a no-op: the encoder owns no state and does not own the writer.
func (*cborEncoder) Close() error {
	//: the writer is the caller's to close.
	return nil
}
