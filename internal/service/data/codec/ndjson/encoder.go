package ndjson

import (
	"io"

	"github.com/kitsunium/sdk/internal/core/data/codec"
	corendjson "github.com/kitsunium/sdk/internal/core/data/codec/ndjson"
	"github.com/kitsunium/sdk/internal/core/data/codec/scratch"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// ndjsonEncoder writes one record per Encode (#901): the line Marshal writes
// for the record, its '\n' included.
type ndjsonEncoder struct {
	// w is the stream; the encoder does not own it.
	w io.Writer
	// line is the record being written, reused across Encode calls.
	line []byte
}

// NewEncoder wraps w in a streaming codec.Encoder writing one line per
// Encode.
func (*ndjsonCodec) NewEncoder(w io.Writer) codec.Encoder {
	//: the line buffer grows to the longest record written.
	return &ndjsonEncoder{w: w}
}

// Encode writes v as one record: its JSON, then '\n', in one Write.
func (e *ndjsonEncoder) Encode(v any) error {
	//: the line, built in the reused buffer.
	merr := e.appendLine(v)
	//: written whole, so a record never reaches the stream torn.
	if merr == nil {
		_, merr = e.w.Write(e.line)
	}
	//: success fast-path.
	if merr == nil {
		//: nothing to wrap.
		return nil
	}
	//: an unencodable record, or a failing writer.
	return errs.Wrap(merr, errs.WrapParams{
		Code:    corendjson.CodeNDJSONMarshalFailed,
		Reason:  "MARSHAL_FAILED",
		Public:  "NDJSON encoding failed",
		Private: "service/data/codec/ndjson.Encoder.Encode: encoding/json returned an error",
	})
}

// Close is a no-op: the encoder buffers nothing and does not own the writer.
func (*ndjsonEncoder) Close() error {
	//: every Encode wrote its line already.
	return nil
}

// appendLine builds v's line in e.line: the record as encodeRecordTo writes
// it, then '\n'.
func (e *ndjsonEncoder) appendLine(v any) error {
	//: a writer over the reused buffer.
	w := scratch.AcquireAppendWriter(e.line[:0])
	//: the record.
	merr := encodeRecordTo(w, v)
	//: the buffer, kept for the next record, with the terminator.
	e.line = append(w.Bytes(), '\n')
	scratch.ReleaseAppendWriter(w)
	//: refused, or written.
	return merr
}
