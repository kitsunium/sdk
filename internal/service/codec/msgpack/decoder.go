// Package msgpack — the streaming decoder. Decode first FRAMES one value:
// it reads exactly the bytes of the next value from the stream into a pooled
// scratch buffer, walking headers with the same table the in-memory decoder
// uses and keeping a count of values still owed instead of recursing. Only
// then is the frame decoded, by the same code Unmarshal runs. Framing never
// trusts a declared length with memory: a string, binary or extension payload
// is refused outright when it is longer than the stream can still deliver
// under its bound, and is otherwise read in frameChunk pieces, so what a
// hostile header costs is what its bytes cost.
//
// The bound is the one the vendor-backed decoder had: one byte past
// maxMsgPackBytes, for the whole stream. A clean end of input between two values is io.EOF; an
// end inside a value, a malformed byte or a value that does not fit its target
// is UNMARSHAL_FAILED, and ends the stream — More reports false and every
// later Decode returns the same error.
package msgpack

import (
	"bufio"
	"errors"
	"io"
	"slices"

	"github.com/kitsunium/sdk/internal/core/codec/scratch"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// streamBufSize is the read-ahead the decoder keeps over its reader.
const streamBufSize int = 4 << 10

// msgpackDecoder reads one MessagePack value per Decode from a stream.
type msgpackDecoder struct {
	// src is the stream, limited to one byte past maxMsgPackBytes in all;
	// nil when the decoder was built over a nil reader.
	src *io.LimitedReader
	// r buffers src.
	r *bufio.Reader
	// err is the failure that ended the stream; nil once it ended cleanly.
	err error
	// done latches once the stream has ended, cleanly or not.
	done bool
}

// framer reads the bytes of exactly one value.
type framer struct {
	// src reports how much of the stream's bound is left.
	src *io.LimitedReader
	// r is the buffered stream.
	r *bufio.Reader
	// out accumulates the value's bytes.
	out []byte
}

// newDecoder returns the streaming decoder over r.
func newDecoder(r io.Reader) *msgpackDecoder {
	//: a nil reader is reported by the first Decode, not by a panic.
	if r == nil {
		//: no stream.
		return &msgpackDecoder{}
	}
	src := &io.LimitedReader{R: r, N: int64(maxMsgPackBytes) + 1}
	//: the read-ahead sits inside the bound.
	return &msgpackDecoder{src: src, r: bufio.NewReaderSize(src, streamBufSize)}
}

// Decode reads the next value into the value v points at. It returns io.EOF,
// untouched, once the stream ends cleanly between two values.
func (d *msgpackDecoder) Decode(v any) error {
	//: an ended stream keeps saying how it ended.
	if d.done {
		//: io.EOF, or the failure that ended it.
		return d.ending()
	}
	//: a decoder built over a nil reader has no stream.
	if d.r == nil {
		d.finish(unmarshalFault("streaming decoder has no reader"))
		//: the latched failure.
		return d.err
	}
	buf := scratch.AcquireBuffer()
	f := framer{src: d.src, r: d.r, out: buf.AvailableBuffer()}
	err := f.run()
	//: a complete frame decodes exactly as Unmarshal would decode it.
	if err == nil {
		err = unmarshalInto(f.out, v)
	}
	retain(buf, f.out)
	scratch.ReleaseBuffer(buf)
	//: any failure — io.EOF included — ends the stream.
	if err != nil {
		d.finish(err)
		//: io.EOF untouched, anything else typed.
		return err
	}
	//: one value decoded.
	return nil
}

// More reports whether the stream has not ended yet.
func (d *msgpackDecoder) More() bool {
	//: the latch, nothing else: More never reads.
	return !d.done
}

// finish latches the end of the stream; io.EOF is a clean end.
func (d *msgpackDecoder) finish(err error) {
	d.done = true
	//: a clean end is not a failure to replay.
	if !errors.Is(err, io.EOF) {
		//: replayed by every later Decode.
		d.err = err
	}
}

// ending is what Decode returns once the stream has ended.
func (d *msgpackDecoder) ending() error {
	//: a failure is replayed.
	if d.err != nil {
		//: the same failure.
		return d.err
	}
	//: a clean end.
	return io.EOF
}

// run frames one value: it owes one value, and each header read either
// settles what it owes (a scalar, a payload copied whole) or adds the
// elements of an array or map to the count.
func (f *framer) run() error {
	//: one value is owed.
	for owed := uint64(1); owed > 0; owed-- {
		c, err := f.r.ReadByte()
		//: nothing more to read.
		if err != nil {
			//: io.EOF before the first byte is a clean end of the stream.
			return f.readFailure(err, len(f.out) == 0)
		}
		f.out = append(f.out, c)
		info := &headerTable[c]
		//: 0xc1 is assigned to nothing.
		if info.fam == famInvalid {
			//: name where it sits in the frame.
			return unmarshalFault("header byte 0xc1 is never used", errs.Int(fieldOffset, len(f.out)-1))
		}
		owed, err = f.body(info, owed)
		//: the body is not there, or not plausible.
		if err != nil {
			//: stop.
			return err
		}
	}
	//: one whole value framed.
	return nil
}

// body copies what follows a header and returns the updated count of values
// still owed.
func (f *framer) body(info *headerInfo, owed uint64) (uint64, error) {
	arg := info.inline
	//: a family with a separate field copies it, and reads it as a length.
	if info.width != 0 {
		raw, err := f.copyField(info.width)
		if err != nil {
			//: the stream ended inside the header.
			return owed, err
		}
		arg = raw
	}
	//: what the header owes beyond itself.
	switch info.fam {
	//: str and bin: the payload.
	case famStr, famBin:
		return owed, f.copyPayload(arg)
	//: ext: the type byte, then the payload.
	case famExt:
		return owed, f.copyPayload(arg + 1)
	//: an array owes its elements.
	case famArray:
		return f.owe(owed, arg)
	//: a map owes a key and a value per pair.
	case famMap:
		return f.owe(owed, arg*valuesPerPair)
	//: nil, booleans and numbers are complete.
	default:
		return owed, nil
	}
}

// owe adds n values to the count, refusing more than the stream could still
// deliver one byte each. owed still counts the container being read, whose
// header is already consumed, hence the −1.
func (f *framer) owe(owed, n uint64) (uint64, error) {
	//: every value still owed needs at least one more byte.
	if owed-1+n > f.budget() {
		//: refuse before reading on.
		return owed, unmarshalFault("declared element count exceeds what the stream can still deliver",
			errs.Int(fieldOffset, len(f.out)), errs.Int64(fieldLen, int64(n)))
	}
	//: owed.
	return owed + n, nil
}

// budget is how many bytes the stream can still deliver under its bound.
func (f *framer) budget() uint64 {
	//: unread bound plus what the read-ahead already holds.
	return uint64(f.src.N) + uint64(f.r.Buffered())
}

// copyField copies a big-endian field of width bytes and returns its value.
func (f *framer) copyField(width uint8) (uint64, error) {
	start := len(f.out)
	//: read the field in place.
	if err := f.read(uint64(width)); err != nil {
		//: the stream ended inside the header.
		return 0, err
	}
	//: the field's value.
	return beUint(f.out[start:]), nil
}

// copyPayload copies n payload bytes, refusing a length the stream cannot
// deliver before reading or reserving anything for it.
func (f *framer) copyPayload(n uint64) error {
	//: a length beyond the bound is refused outright.
	if n > f.budget() {
		//: name the offset and the declared length.
		return unmarshalFault("declared length exceeds what the stream can still deliver",
			errs.Int(fieldOffset, len(f.out)), errs.Int64(fieldLen, int64(n)))
	}
	//: in bounded pieces, so memory follows the bytes that arrive.
	for n > 0 {
		chunk := min(n, frameChunk)
		//: the first failure wins.
		if err := f.read(chunk); err != nil {
			//: the stream ended inside the payload.
			return err
		}
		n -= chunk
	}
	//: copied.
	return nil
}

// read appends exactly n bytes from the stream to out.
func (f *framer) read(n uint64) error {
	//: bytes the read-ahead already holds are copied straight out of it.
	if n <= uint64(f.r.Buffered()) {
		//: one copy, no read call.
		return f.readBuffered(int(n))
	}
	start := len(f.out)
	f.out = slices.Grow(f.out, int(n))[:start+int(n)]
	//: all n bytes, or a failure.
	if _, err := io.ReadFull(f.r, f.out[start:]); err != nil {
		f.out = f.out[:start]
		//: an end inside a value is never clean.
		return f.readFailure(err, false)
	}
	//: read.
	return nil
}

// readBuffered appends n bytes the read-ahead is known to hold — the common
// case for the headers and short payloads of a document — without the read
// loop io.ReadFull runs.
func (f *framer) readBuffered(n int) error {
	p, err := f.r.Peek(n)
	//: unreachable while n ≤ Buffered(), kept so a change cannot hide it.
	if err != nil {
		//: classified like any read failure.
		return f.readFailure(err, false)
	}
	f.out = append(f.out, p...)
	//: consume what was copied.
	if _, derr := f.r.Discard(n); derr != nil {
		//: classified like any read failure.
		return f.readFailure(derr, false)
	}
	//: copied.
	return nil
}

// readFailure classifies a read error: io.EOF before a value's first byte is
// the clean end of the stream; an end anywhere else is a truncated value; any
// other error is the reader's, wrapped.
func (f *framer) readFailure(err error, atStart bool) error {
	//: the stream ended between two values.
	if atStart && errors.Is(err, io.EOF) {
		//: io.EOF, untouched, as codec.Decoder callers expect.
		return io.EOF
	}
	//: the stream ended inside a value.
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		//: name where.
		return unmarshalFault("stream ends inside a value", errs.Int(fieldOffset, len(f.out)))
	}
	//: the reader's own failure.
	return wrapUnmarshal(err, "reading the stream failed")
}
