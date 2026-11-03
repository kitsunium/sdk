package json

import (
	stdjson "encoding/json"
	"io"

	"github.com/kitsunium/sdk/internal/core/data/codec"
	corejson "github.com/kitsunium/sdk/internal/core/data/codec/json"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// fieldLimit names the byte ceiling a refused value exceeded.
const fieldLimit string = "limit"

// jsonDecoder wraps *encoding/json.Decoder so it satisfies codec.Decoder.
type jsonDecoder struct {
	inner *stdjson.Decoder
	// window bounds every value the decoder reads; nil for NewDecoder's
	// decoder, which has no ceiling.
	window *window
}

// window is the reader under a bounded decoder (#891): it hands over at most
// one byte more than max past the end of the value decoded last, so a value longer than
// max is refused after one byte more than max of them, however many the stream holds.
type window struct {
	// r is the stream.
	r io.Reader
	// max is the ceiling of one value.
	max int64
	// read is how many bytes r has handed over.
	read int64
	// end is how many bytes r may have handed over before a read is refused.
	end int64
	// hit latches once a value was refused: the stream cannot be resynchronised.
	hit bool
}

// NewBoundedDecoder implements codec.BoundedStreamingCodec (#891): a decoder
// refusing, with SIZE_EXCEEDED, a value longer than maxBytes counted from the
// end of the previous one, after reading at most one byte more than maxBytes of it. A
// ceiling of zero or below is refused: no bound means unlimited (ADR 0031).
func (*jsonCodec) NewBoundedDecoder(r io.Reader, maxBytes int64) (codec.Decoder, error) {
	//: a ceiling that bounds nothing.
	if maxBytes <= 0 {
		//: refused.
		return nil, errs.Wrap(codec.BoundInvalid, errs.WrapParams{}, errs.Int64(fieldLimit, maxBytes))
	}
	w := &window{r: r, max: maxBytes, end: maxBytes + 1}
	//: the stdlib decoder reads through the window.
	return &jsonDecoder{inner: stdjson.NewDecoder(w), window: w}, nil
}

// Read hands over what r returns, never past the window's end.
func (w *window) Read(p []byte) (int, error) {
	room := w.end - w.read
	//: the value under decode is longer than the ceiling.
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

// open starts the window of the value that begins at offset start.
func (w *window) open(start int64) {
	w.end = start + w.max + 1
}

// exceeded is the SIZE_EXCEEDED refusal of a value past the ceiling, with
// UNMARSHAL_FAILED's code joining the trail (#894), placed at the first byte
// past the ceiling — the stream's offset end-1, counted as encoding/json
// counts the offsets of its own refusals (#1125).
func (w *window) exceeded() error {
	//: the bound is the origin, where it was crossed and by how far.
	return errs.Wrap(corejson.SizeExceeded, errs.WrapParams{Code: corejson.CodeJSONUnmarshalFailed},
		append(offsetField(w.end-1), errs.Int64(fieldLimit, w.max))...)
}

// Decode reads the next value from the wrapped stdlib decoder.
func (d *jsonDecoder) Decode(v any) error {
	//: a nil or non-pointer target is the caller's error, refused first (#1101).
	if terr := codec.CheckTarget(v); terr != nil {
		return terr
	}
	//: a bounded decoder checks every value against its ceiling.
	if d.window != nil {
		//: within the window, or refused.
		return d.decodeBounded(v)
	}
	//: delegate to stdlib then wrap on error.
	return wrapDecodeErr(d.inner.Decode(v))
}

// decodeBounded decodes the next value inside its window: refused when a
// read past the window was needed, or when the value, read whole, is one
// byte longer than the ceiling.
func (d *jsonDecoder) decodeBounded(v any) error {
	//: a refused stream stays refused.
	if d.window.hit {
		//: the same refusal.
		return d.window.exceeded()
	}
	start := d.inner.InputOffset()
	d.window.open(start)
	jerr := d.inner.Decode(v)
	end := d.inner.InputOffset()
	//: the value needed bytes past its window, or reached max+1 whole.
	if d.window.hit || (jerr == nil && end-start > d.window.max) {
		d.window.hit = true
		//: refused.
		return d.window.exceeded()
	}
	//: More peeks at the next value: its window starts here.
	d.window.open(end)
	//: nil, or the stdlib error wrapped.
	return wrapDecodeErr(jerr)
}

// wrapDecodeErr wraps a stdlib decoding error as UNMARSHAL_FAILED; nil stays
// nil.
func wrapDecodeErr(jerr error) error {
	//: success fast-path.
	if jerr == nil {
		//: nothing to wrap.
		return nil
	}
	//: wrap the stdlib error.
	return errs.Wrap(jerr, errs.WrapParams{
		Code:    corejson.CodeJSONUnmarshalFailed,
		Reason:  "UNMARSHAL_FAILED",
		Public:  "JSON decoding failed",
		Private: "service/data/codec/json.Decoder.Decode: encoding/json returned an error",
	}, position(jerr)...)
}

// More reports whether another JSON value remains in the stream.
func (d *jsonDecoder) More() bool {
	//: a refused bounded stream has nothing more to give.
	if d.window != nil && d.window.hit {
		//: ended.
		return false
	}
	//: delegate to the stdlib bool.
	return d.inner.More()
}
