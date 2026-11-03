package cbor

import (
	"errors"
	"io"
	"math"
	"slices"

	"github.com/kitsunium/sdk/internal/core/data/codec"
	corecbor "github.com/kitsunium/sdk/internal/core/data/codec/cbor"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// The reading policy of the stream decoder.
const (
	// minReadSize is the least free space handed to a Read.
	minReadSize int = 512
	// maxEmptyReads is how many Reads may return no byte and no error before
	// the reader is treated as stuck, as bufio treats one.
	maxEmptyReads int = 100
)

// cborDecoder reads CBOR data items from a reader, one per Decode.
type cborDecoder struct {
	// r is the stream.
	r io.Reader
	// err is the failure that ends the stream: a malformed item or a failing
	// reader. Decode returns it from then on.
	err error
	// buf holds bytes read and not yet decoded, from buf[off] on.
	buf []byte
	// off is where the next item starts in buf.
	off int
	// done is set by the first failure or the end of the stream: More reports
	// false from then on, as it always has.
	done bool
	// walk validates the next item, keeping its place across reads.
	walk validator
	// mode decodes every item as the instance's Unmarshal does: NewStrict's,
	// NewDeterministic's or the registered codec's.
	mode decodeMode
	// limit is the longest item NewBoundedDecoder's decoder reads, and how
	// many bytes its buffer ever holds; 0 for NewDecoder's, which has none.
	limit int
}

// newBoundedDecoder is NewBoundedDecoder of every instance (#891): a decoder
// in mode whose items are at most maxBytes long, refused with SIZE_EXCEEDED
// once maxBytes of an unfinished item are buffered — never more read. A
// ceiling of zero or below is refused: no bound means unlimited (ADR 0031).
func newBoundedDecoder(r io.Reader, maxBytes int64, mode decodeMode) (codec.Decoder, error) {
	//: a ceiling that bounds nothing.
	if maxBytes <= 0 {
		//: refused.
		return nil, errs.Wrap(codec.BoundInvalid, errs.WrapParams{}, errs.Int64("limit", maxBytes))
	}
	//: a ceiling past what a buffer can hold is the buffer's own bound.
	limit := int(min(maxBytes, int64(math.MaxInt)))
	//: the validator resumes across reads, inside the ceiling.
	return &cborDecoder{r: r, walk: validator{limit: maxCBORNestedLevels}, mode: mode, limit: limit}, nil
}

// Decode reads the next data item into v. It returns io.EOF, unwrapped, when
// the stream ends between items, and UNMARSHAL_FAILED when it ends inside
// one. An item that is well-formed but does not fit v is consumed, so the
// next Decode reads the item after it.
func (d *cborDecoder) Decode(v any) error {
	//: a nil or non-pointer target is the caller's error, refused first (#1101).
	if terr := codec.CheckTarget(v); terr != nil {
		return terr
	}
	//: a malformed stream cannot be resynchronised.
	if d.err != nil {
		//: the same failure.
		return d.err
	}
	n, err := d.nextItem()
	//: the end of the stream, or a failure that ends it.
	if err != nil {
		d.done = true
		//: io.EOF is a clean end, returned as is.
		if !errors.Is(err, io.EOF) {
			d.err = err
		}
		//: EOF or the failure.
		return err
	}
	item := d.buf[d.off : d.off+n]
	d.off += n
	d.walk = validator{limit: maxCBORNestedLevels}
	err = unmarshalItemMode(item, v, d.mode)
	//: a failure ends More, as it always has.
	if err != nil {
		d.done = true
	}
	//: decoded, or the first item that did not fit.
	return err
}

// More reports whether Decode may still return an item: false once the
// stream has ended or a Decode has failed.
func (d *cborDecoder) More() bool {
	//: the sticky end-of-stream latch.
	return !d.done
}

// nextItem validates the next item, reading until it is whole, and returns
// its length.
func (d *cborDecoder) nextItem() (int, error) {
	//: validate what is buffered, read more, repeat.
	for {
		complete, err := d.walk.resume(d.buf[d.off:])
		//: malformed, or past a bound.
		if err != nil {
			//: ends the stream.
			return 0, err
		}
		//: the whole item is buffered and valid.
		if complete {
			//: its length.
			return d.walk.off, nil
		}
		//: an unfinished item already as long as the ceiling is longer.
		if d.limit > 0 && len(d.buf)-d.off >= d.limit {
			//: refused, before any more is read.
			return 0, exceeded(corecbor.SizeExceeded, len(d.buf)-d.off, "a data item in the stream is longer than the decoder's byte ceiling")
		}
		//: more bytes, or the reason there are none.
		if err := d.fill(); err != nil {
			//: EOF, truncation, or a read failure.
			return 0, err
		}
	}
}

// fill reads more of the stream into the buffer.
func (d *cborDecoder) fill() error {
	d.compact()
	n, err := d.read()
	//: bytes first: a read error is reported once nothing more comes.
	if n > 0 {
		//: progress.
		return nil
	}
	//: the stream ended.
	if errors.Is(err, io.EOF) {
		//: between items: a clean end.
		if len(d.buf) == d.off {
			//: io.EOF, as io.Reader reports it.
			return io.EOF
		}
		//: inside an item.
		return malformed(len(d.buf)-d.off, "the stream ends inside a data item")
	}
	//: the reader failed.
	return decodeCause(err, "reading the stream failed")
}

// compact moves the unread bytes to the front of the buffer. It moves each
// item at most once: after the first read of an item, it starts at 0.
func (d *cborDecoder) compact() {
	//: already at the front.
	if d.off == 0 {
		//: nothing to move.
		return
	}
	n := copy(d.buf, d.buf[d.off:])
	d.buf = d.buf[:n]
	d.off = 0
}

// growth is how many bytes the buffer grows by: its capacity again, at least
// minReadSize, and for a bounded decoder never past its ceiling.
func (d *cborDecoder) growth() int {
	n := max(minReadSize, cap(d.buf))
	//: the room left under the ceiling, which nextItem keeps above zero.
	if d.limit > 0 {
		n = min(n, d.limit-len(d.buf))
	}
	//: the growth.
	return n
}

// read reads once into the buffer's free space, which it doubles when it
// runs short. A reader returning no byte and no error is retried, then
// reported as stuck.
func (d *cborDecoder) read() (int, error) {
	//: room for at least minReadSize bytes, the capacity doubling.
	if cap(d.buf)-len(d.buf) < minReadSize {
		d.buf = slices.Grow(d.buf, d.growth())
	}
	free := d.buf[len(d.buf):cap(d.buf)]
	//: a bounded decoder never buffers past its ceiling.
	if d.limit > 0 && len(free) > d.limit-len(d.buf) {
		free = free[:d.limit-len(d.buf)]
	}
	//: an io.Reader may return 0, nil; it may not do so forever.
	for range maxEmptyReads {
		n, err := d.r.Read(free)
		d.buf = d.buf[:len(d.buf)+n]
		//: bytes, or an error.
		if n > 0 || err != nil {
			//: what the reader said.
			return n, err
		}
	}
	//: a stuck reader.
	return 0, io.ErrNoProgress
}
