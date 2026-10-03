// Package msgpack — the streaming encoder. Each Encode encodes one value into
// a pooled scratch buffer and hands it to the writer in ONE Write, so a value
// that fails to encode writes nothing and leaves the stream intact — the
// vendor's encoder wrote as it went and left half a value behind. The bytes
// are Marshal's, so a streamed value and a marshalled one are identical: the
// vendor's streaming encoder, unlike its Marshal, wrote int8–int64 and
// uint8–uint64 fields at their full Go width.
package msgpack

import (
	"io"

	"github.com/kitsunium/sdk/internal/core/data/codec/scratch"
)

// msgpackEncoder writes one MessagePack value per Encode to w.
type msgpackEncoder struct {
	// w receives each encoded value in one Write.
	w io.Writer
}

// newEncoder returns the streaming encoder over w.
func newEncoder(w io.Writer) *msgpackEncoder {
	//: nothing to set up until the first value.
	return &msgpackEncoder{w: w}
}

// Encode serialises v and writes it to the wrapped writer.
func (e *msgpackEncoder) Encode(v any) error {
	//: a nil writer is a wiring defect, refused instead of panicking.
	if e.w == nil {
		//: nothing can be written.
		return marshalFault("streaming encoder has no writer")
	}
	buf := scratch.AcquireBuffer()
	b, err := appendAny(buf.AvailableBuffer(), v, 0)
	//: only a complete value reaches the writer.
	if err == nil {
		err = e.write(b)
	}
	retain(buf, b)
	scratch.ReleaseBuffer(buf)
	//: the encode or the write failure, typed.
	return err
}

// write hands one complete value to the writer.
func (e *msgpackEncoder) write(b []byte) error {
	//: one Write per value.
	if _, err := e.w.Write(b); err != nil {
		//: the writer's failure, wrapped for reason-based matching.
		return wrapMarshal(err, "writing the encoded value failed")
	}
	//: written.
	return nil
}

// Close is a no-op: the encoder owns neither the writer nor a buffer between
// calls.
func (*msgpackEncoder) Close() error {
	//: nothing to flush or release.
	return nil
}
