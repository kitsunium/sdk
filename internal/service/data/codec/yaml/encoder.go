package yaml

import (
	"io"

	"github.com/kitsunium/sdk/internal/core/data/codec/scratch"
	coreyaml "github.com/kitsunium/sdk/internal/core/data/codec/yaml"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// documentSeparator opens every document after the first.
const documentSeparator string = "---\n"

// yamlEncoder writes a stream of documents to w. A failed write is sticky:
// every later Encode, and Close, return it, so a caller who checks only Close
// still learns the stream is incomplete.
type yamlEncoder struct {
	// w receives the documents.
	w io.Writer
	// err is the first write failure.
	err error
	// written counts the documents written.
	written int
	// closed refuses an Encode after Close.
	closed bool
}

// Encode writes v as the next document. A value the encoder cannot write is
// refused before anything reaches w, and does not end the stream.
func (e *yamlEncoder) Encode(v any) error {
	//: a previous write failed.
	if e.err != nil {
		//: the same failure.
		return e.err
	}
	//: the stream is closed.
	if e.closed {
		//: refused.
		return marshalError("the encoder is closed", "")
	}
	buf := scratch.AcquireBuffer()
	defer scratch.ReleaseBuffer(buf)
	//: every document after the first opens with its marker.
	if e.written > 0 {
		buf.WriteString(documentSeparator)
	}
	enc := encoder{buf: buf}
	//: the value, refused whole before a byte is written.
	if err := enc.encodeDocument(v); err != nil {
		//: refused.
		return err
	}
	//: the document, in one write.
	if _, err := e.w.Write(buf.Bytes()); err != nil {
		e.err = errs.Wrap(err, errs.WrapParams{
			Code:    coreyaml.CodeYAMLMarshalFailed,
			Reason:  "MARSHAL_FAILED",
			Public:  "YAML encoding failed",
			Private: "service/data/codec/yaml.Encoder: the writer failed",
		})
		//: sticky.
		return e.err
	}
	e.written++
	//: written.
	return nil
}

// Close ends the stream. Every document is written whole by Encode, so there
// is nothing to flush; Close reports a write that failed.
func (e *yamlEncoder) Close() error {
	e.closed = true
	//: nil, or the first write failure.
	return e.err
}
