// Package toml — the streaming codec.Encoder: one TOML document per Encode
// call, written to the writer in one Write.
package toml

import (
	"io"

	"github.com/kitsunium/sdk/internal/core/codec/scratch"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// tomlEncoder writes each value Encode is given as a document of its own.
// Consecutive documents are written one after the other: TOML has no
// separator between documents, so a reader reads the stream as one, which
// fails if two of them define the same key.
type tomlEncoder struct {
	// w receives each document.
	w io.Writer
}

// Encode writes v as one TOML document to the writer.
func (e *tomlEncoder) Encode(v any) error {
	//: rent an already-Reset buffer from the shared codec pool.
	buf := scratch.AcquireBuffer()
	defer scratch.ReleaseBuffer(buf)
	out, err := encodeDocument(buf.AvailableBuffer(), v)
	//: a value TOML cannot represent.
	if err != nil {
		//: MARSHAL_FAILED.
		return err
	}
	//: one write for the whole document.
	if _, werr := e.w.Write(out); werr != nil {
		//: MARSHAL_FAILED over the writer's error.
		return errs.Wrap(werr, errs.WrapParams{
			Code:    CodeTOMLMarshalFailed,
			Reason:  MarshalFailed.Reason(),
			Public:  MarshalFailed.Public(),
			Private: privateWriteFailed,
		})
	}
	//: written.
	return nil
}

// Close is a no-op: the encoder buffers nothing between documents and does
// not own the writer.
func (*tomlEncoder) Close() error {
	//: nothing to flush or release.
	return nil
}
