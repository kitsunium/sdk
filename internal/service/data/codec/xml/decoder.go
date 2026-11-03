package xml

import (
	stdxml "encoding/xml"
	"errors"
	"io"

	"github.com/kitsunium/sdk/internal/core/data/codec"
	corexml "github.com/kitsunium/sdk/internal/core/data/codec/xml"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// fieldLimit names the byte ceiling a refused value exceeded.
const fieldLimit string = "limit"

// xmlDecoder wraps *encoding/xml.Decoder so it satisfies codec.Decoder.
// A sticky done latch mirrors the contract of the other streaming codecs:
// More() returns false once Decode has surfaced io.EOF, without consuming
// any additional tokens from the underlying stream.
type xmlDecoder struct {
	inner *stdxml.Decoder
	//: sticky EOF flag so More() returns false after the stream drains.
	done bool
	// window bounds every element the decoder reads; nil for NewDecoder's
	// decoder, which has no ceiling.
	window *window
}

// window is the reader under a bounded decoder (#891): it hands over at most
// one byte more than max past the end of the element decoded last, so an element longer
// than max is refused after one byte more than max of them, however many the stream holds.
type window struct {
	// r is the stream.
	r io.Reader
	// max is the ceiling of one element.
	max int64
	// read is how many bytes r has handed over.
	read int64
	// end is how many bytes r may have handed over before a read is refused.
	end int64
	// hit latches once an element was refused: the stream cannot be
	// resynchronised.
	hit bool
}

// NewBoundedDecoder implements codec.BoundedStreamingCodec (#891): a decoder
// refusing, with SIZE_EXCEEDED, an element longer than maxBytes counted from
// the end of the previous one, after reading at most one byte more than maxBytes of it.
// A ceiling of zero or below is refused: no bound means unlimited (ADR 0031).
func (*xmlCodec) NewBoundedDecoder(r io.Reader, maxBytes int64) (codec.Decoder, error) {
	//: a ceiling that bounds nothing.
	if maxBytes <= 0 {
		//: refused.
		return nil, errs.Wrap(codec.BoundInvalid, errs.WrapParams{}, errs.Int64(fieldLimit, maxBytes))
	}
	w := &window{r: r, max: maxBytes, end: maxBytes + 1}
	//: the stdlib decoder reads through the window.
	return &xmlDecoder{inner: stdxml.NewDecoder(w), window: w}, nil
}

// Read hands over what r returns, never past the window's end.
func (w *window) Read(p []byte) (int, error) {
	room := w.end - w.read
	//: the element under decode is longer than the ceiling.
	if room <= 0 {
		w.hit = true
		//: the refusal, which the decoder reports in its own words.
		return 0, w.exceeded()
	}
	//: never more than the room left.
	if int64(len(p)) > room {
		p = p[:room]
	}
	n, err := w.r.Read(p)
	w.read += int64(n)
	//: what the stream said.
	return n, err
}

// open starts the window of the element that begins at offset start.
func (w *window) open(start int64) {
	w.end = start + w.max + 1
}

// exceeded is the SIZE_EXCEEDED refusal of an element past the ceiling, with
// UNMARSHAL_FAILED's code joining the trail (#894), placed at the first byte
// past the ceiling — the stream's offset end-1 (#1125). The window counts
// bytes, not lines: the offset is the coordinate it knows.
func (w *window) exceeded() error {
	//: the bound is the origin, where it was crossed and by how far.
	return errs.Wrap(corexml.SizeExceeded, errs.WrapParams{Code: corexml.CodeXMLUnmarshalFailed},
		append(offsetField(w.end-1), errs.Int64(fieldLimit, w.max))...)
}

// Decode reads the next value from the wrapped stdlib decoder.
// (unwrapped) once the stream is drained; nil otherwise.
func (d *xmlDecoder) Decode(v any) error {
	//: a nil or non-pointer target is the caller's error, refused first (#1101).
	if terr := codec.CheckTarget(v); terr != nil {
		return terr
	}
	//: drained latch short-circuits additional reads.
	if d.done {
		//: mirror stdlib stream semantics.
		return io.EOF
	}
	//: a bounded decoder checks every element against its ceiling.
	if d.window != nil {
		//: within the window, or refused.
		return d.decodeBounded(v)
	}
	//: delegate.
	return d.settle(d.inner.Decode(v))
}

// decodeBounded decodes the next element inside its window: refused when a
// read past the window was needed, or when the element, read whole, is one
// byte longer than the ceiling.
func (d *xmlDecoder) decodeBounded(v any) error {
	//: a refused stream stays refused.
	if d.window.hit {
		//: the same refusal.
		return d.window.exceeded()
	}
	start := d.inner.InputOffset()
	d.window.open(start)
	xerr := d.inner.Decode(v)
	//: the element needed bytes past its window, or reached max+1 whole.
	if d.window.hit || (xerr == nil && d.inner.InputOffset()-start > d.window.max) {
		d.window.hit = true
		//: refused.
		return d.window.exceeded()
	}
	//: io.EOF, nil, or the stdlib error wrapped.
	return d.settle(xerr)
}

// settle latches io.EOF, returned untouched, and wraps any other stdlib
// error as UNMARSHAL_FAILED; nil stays nil.
func (d *xmlDecoder) settle(xerr error) error {
	//: EOF latch toggles the done flag for subsequent More() calls.
	if errors.Is(xerr, io.EOF) {
		//: remember that we reached the stream end.
		d.done = true
		//: return io.EOF untouched.
		return io.EOF
	}
	//: success fast-path.
	if xerr == nil {
		//: nothing to wrap.
		return nil
	}
	//: wrap.
	return errs.Wrap(xerr, errs.WrapParams{
		Code:    corexml.CodeXMLUnmarshalFailed,
		Reason:  "UNMARSHAL_FAILED",
		Public:  "XML decoding failed",
		Private: "service/data/codec/xml.Decoder.Decode: encoding/xml returned an error",
	}, position(xerr)...)
}

// More reports whether another XML element can be decoded.
func (d *xmlDecoder) More() bool {
	//: reflect the sticky EOF latch — no token consumed; a refused bounded
	//: stream has nothing more to give either.
	return !d.done && (d.window == nil || !d.window.hit)
}
