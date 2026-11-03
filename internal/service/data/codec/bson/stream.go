package bson

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"errors"
	"io"

	"github.com/kitsunium/sdk/internal/core/data/codec"
	corebson "github.com/kitsunium/sdk/internal/core/data/codec/bson"
	"github.com/kitsunium/sdk/internal/core/data/codec/scratch"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// streamFirstRead is how much of a streamed document's body the decoder
// reserves before the reader has delivered any of it; past it, the buffer
// grows with the bytes that arrive, so a length prefix the reader never
// honours costs what it delivered, not what it declared (CWE-400).
const streamFirstRead int = 64 << 10

// bsonEncoder writes one document per Encode — the layout of a mongodump file
// and of a socket of concatenated documents (#1100).
type bsonEncoder struct {
	// w receives the stream; the encoder never closes it.
	w io.Writer
}

// bsonDecoder reads one length-prefixed document per Decode.
type bsonDecoder struct {
	// r is the stream, buffered so More can look ahead one byte.
	r *bufio.Reader
	// done latches once the stream drained or failed.
	done bool
	// strict decodes every document as NewStrict's Unmarshal does.
	strict bool
}

// NewEncoder wraps w in a streaming codec.Encoder: each Encode appends one
// document through the Append path into a pooled buffer and issues one Write,
// so a value that fails to encode writes nothing.
func (*bsonCodec) NewEncoder(w io.Writer) codec.Encoder {
	//: stateless besides the writer.
	return &bsonEncoder{w: w}
}

// NewDecoder wraps r in a streaming codec.Decoder: each Decode reads the
// int32 length prefix, refuses a length past maxBSONBytes before reserving
// it, reads the document — growing with what the reader delivers — validates
// it whole and decodes it.
func (*bsonCodec) NewDecoder(r io.Reader) codec.Decoder {
	br, ok := r.(*bufio.Reader)
	//: More peeks one byte.
	if !ok {
		br = bufio.NewReader(r)
	}
	//: ready.
	return &bsonDecoder{r: br}
}

// Encode writes v as one document.
func (e *bsonEncoder) Encode(v any) error {
	buf := scratch.AcquireBufferSized(int(largeDocumentHint.Load()))
	defer scratch.ReleaseBufferSized(buf)
	out, err := appendDocument(buf.AvailableBuffer(), v)
	//: nothing reaches the stream.
	if err != nil {
		//: the typed refusal.
		return err
	}
	//: keep a backing array the encode grew into for the next document.
	if cap(out) > buf.Cap() {
		*buf = *bytes.NewBuffer(out[:0])
	}
	//: one Write per document.
	if _, werr := e.w.Write(out); werr != nil {
		//: the writer's failure.
		return marshalError(werr, "Encoder.Encode: the writer returned an error")
	}
	//: one document on the stream.
	return nil
}

// Close implements codec.Encoder; a stream of documents has no trailer.
func (*bsonEncoder) Close() error {
	//: nothing to flush.
	return nil
}

// Decode reads the next document into v.
func (d *bsonDecoder) Decode(v any) error {
	//: a nil or non-pointer target is the caller's error, refused first (#1101).
	if terr := checkTarget(v); terr != nil {
		return terr
	}
	//: a drained or failed stream stays so.
	if d.done {
		//: the clean end.
		return io.EOF
	}
	doc, err := d.next()
	//: io.EOF at a clean end, or a typed refusal; either ends the stream.
	if err != nil {
		d.done = true
		//: verbatim.
		return err
	}
	//: one well-formed document, or nothing is decoded.
	if verr := validateRoot(doc); verr != nil {
		d.done = true
		//: malformed, or nested too deep.
		return verr
	}
	//: one reading of the document, when strict.
	if d.strict {
		//: a repeated name or an unknown member; the stream stays usable.
		return decodeStrict(doc, v)
	}
	//: decode into the target; a target the document does not fit leaves the
	//: stream usable, as the document was read whole.
	return decodeInto(doc, v)
}

// More reports whether another document may follow.
func (d *bsonDecoder) More() bool {
	//: latched.
	if d.done {
		//: nothing more.
		return false
	}
	_, perr := d.r.Peek(1)
	//: a byte waits.
	return perr == nil
}

// next reads one length-prefixed document whole.
func (d *bsonDecoder) next() ([]byte, error) {
	var prefix [4]byte
	n, rerr := io.ReadFull(d.r, prefix[:])
	//: no byte at all: the stream ended cleanly.
	if n == 0 && errors.Is(rerr, io.EOF) {
		//: verbatim.
		return nil, io.EOF
	}
	//: a prefix cut short.
	if rerr != nil {
		//: truncated.
		return nil, unmarshalError(rerr, "Decoder.Decode: the stream ends inside a length prefix")
	}
	declared := int64(int32(binary.LittleEndian.Uint32(prefix[:]))) //nolint:gosec // the wire's int32
	//: past the ceiling: refused before anything of that size is reserved.
	if declared > int64(maxBSONBytes) {
		//: CWE-400.
		return nil, errs.Wrap(nil, errs.WrapParams{
			Code:    corebson.CodeBSONSizeExceeded,
			Reason:  "SIZE_EXCEEDED",
			Public:  "BSON input exceeds size limit",
			Private: "service/data/codec/bson.Decoder.Decode: a declared document length exceeds maxBSONBytes",
		}, errs.Int64("len", declared), errs.Int("cap", maxBSONBytes))
	}
	//: smaller than the empty document.
	if declared < int64(minDocumentSize) {
		//: malformed.
		return nil, unmarshalError(nil, "Decoder.Decode: a declared document length is below the minimum")
	}
	return readDocument(d.r, prefix, int(declared))
}

// readDocument reads the rest of a document of size bytes after its prefix,
// reserving at most streamFirstRead before the reader delivers, then growing
// with what it delivers.
func readDocument(r io.Reader, prefix [4]byte, size int) ([]byte, error) {
	doc := make([]byte, len(prefix), min(size, len(prefix)+streamFirstRead))
	copy(doc, prefix[:])
	//: fill, doubling the reservation as the bytes arrive.
	for len(doc) < size {
		//: the reserved part is full: reserve as much again, up to size.
		if len(doc) == cap(doc) {
			grown := make([]byte, len(doc), min(size, 2*cap(doc)))
			copy(grown, doc)
			doc = grown
		}
		n, rerr := io.ReadFull(r, doc[len(doc):cap(doc)])
		doc = doc[:len(doc)+n]
		//: the reader stopped short of the declared length.
		if rerr != nil {
			//: truncated.
			return nil, unmarshalError(rerr, "Decoder.Decode: the stream ends inside a document")
		}
	}
	//: the whole document.
	return doc, nil
}
