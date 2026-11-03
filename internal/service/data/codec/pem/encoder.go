package pem

import (
	stdpem "encoding/pem"
	"io"

	"github.com/kitsunium/sdk/internal/core/data/codec"
	corepem "github.com/kitsunium/sdk/internal/core/data/codec/pem"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// pemEncoder writes one *pem.Block per Encode (#901): Marshal's bytes, built
// in a buffer the encoder reuses, then written in one Write.
type pemEncoder struct {
	// w is the stream; the encoder does not own it.
	w io.Writer
	// buf is the block being written, reused across Encode calls.
	buf []byte
}

// NewEncoder wraps w in a streaming codec.Encoder writing one block per
// Encode.
func (*pemCodec) NewEncoder(w io.Writer) codec.Encoder {
	//: the buffer grows to the largest block written.
	return &pemEncoder{w: w}
}

// Encode writes v, a non-nil *pem.Block, as Marshal writes it. A block
// Marshal refuses writes nothing; a failing writer is MARSHAL_FAILED.
func (e *pemEncoder) Encode(v any) error {
	//: a non-nil *pem.Block, or nil for checkBlock to refuse.
	block, _ := v.(*stdpem.Block)
	//: the same gate as Marshal.
	if berr := checkBlock(block, "service/data/codec/pem.Encoder.Encode"); berr != nil {
		//: nothing written.
		return berr
	}
	//: the block, into the reused buffer.
	e.buf = appendBlock(e.buf[:0], block)
	//: one write per block.
	_, werr := e.w.Write(e.buf)
	//: success fast-path.
	if werr == nil {
		//: nothing to wrap.
		return nil
	}
	//: the writer failed.
	return errs.Wrap(werr, errs.WrapParams{
		Code:    corepem.CodePEMMarshalFailed,
		Reason:  "MARSHAL_FAILED",
		Public:  "PEM encoding failed",
		Private: "service/data/codec/pem.Encoder.Encode: the writer failed",
	})
}

// Close is a no-op: the encoder buffers nothing between calls and does not
// own the writer.
func (*pemEncoder) Close() error {
	//: every Encode wrote its block already.
	return nil
}
