// Package multipart — adapts mime/multipart.Reader to codec.Decoder.
//
// This is the ONLY read path in the package: Unmarshal drives this decoder
// over a bytes.Reader, so a body parsed from memory and a body streamed off a
// socket go through identical code and identical bounds.
package multipart

import (
	"bufio"
	"bytes"
	stdjson "encoding/json"
	"errors"
	"io"
	"math"
	stdmp "mime/multipart"
	"reflect"

	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// multipartDecoder adapts *mime/multipart.Reader to codec.Decoder. One Decode
// call consumes exactly one part; the parts are streamed, so the decoder holds
// at most one part body in memory at a time regardless of how many arrive.
type multipartDecoder struct {
	// inner is the stdlib reader; nil when construction failed.
	inner *stdmp.Reader
	// count charges the bounds per part, identical to the encoder's set.
	count counter
	// pending is the one-part look-ahead so More can answer without consuming.
	pending *PartValue
	// pendErr is the one error waiting to be surfaced by the next Decode.
	// codec.Decoder's More returns a bare bool, so a failure discovered during
	// look-ahead is parked here rather than dropped.
	pendErr error
	// done latches once the stream is drained or has terminally failed.
	done bool
}

// Decode reads the next part into v.
//
// A *PartValue receives the part verbatim (headers plus body). Any other
// non-nil pointer receives encoding/json's decode of the part body — the read
// side of the JSON-mediated shape Encode writes. io.EOF is returned verbatim
// once the stream is drained, matching every other streaming codec here.
func (d *multipartDecoder) Decode(v any) error {
	//: fill the look-ahead slot when nothing is buffered.
	d.fetch()
	//: a parked failure is reported exactly once, then latches the stream.
	if d.pendErr != nil {
		//: hand it over and stop producing.
		err := d.pendErr
		//: clear so More stops promising another value.
		d.pendErr = nil
		//: terminal.
		d.done = true
		//: caller sees the typed error.
		return err
	}
	//: no part and no error means the stream drained cleanly.
	if d.pending == nil {
		//: propagate io.EOF verbatim per the Decoder contract.
		return io.EOF
	}
	//: consume the look-ahead slot.
	part := d.pending
	//: slot is free for the next fetch.
	d.pending = nil
	//: a *PartValue target takes the part verbatim, headers included.
	if target, ok := v.(*PartValue); ok {
		//: publish through the caller's pointer, or refuse a nil one.
		return publishPart(part, target)
	}
	//: every other target reads the JSON-mediated body.
	return assignPart(part, v)
}

// More reports whether another Decode call will produce a value or an error.
// A parked failure counts as "more": answering false there would let the
// idiomatic `for dec.More() { dec.Decode(&v) }` loop swallow it in silence.
func (d *multipartDecoder) More() bool {
	//: fill the look-ahead slot when nothing is buffered.
	d.fetch()
	//: either a part or an error is waiting for the next Decode.
	return d.pending != nil || d.pendErr != nil
}

// fetch populates the look-ahead slot from the reader. It is a no-op once the
// slot is occupied, an error is parked, or the stream has latched.
func (d *multipartDecoder) fetch() {
	//: nothing to do when the slot is busy or the stream is finished.
	if d.done || d.pending != nil || d.pendErr != nil {
		//: idempotent by design — More may be called repeatedly.
		return
	}
	//: a decoder built with an unusable boundary has no reader at all.
	if d.inner == nil {
		//: latch; the construction error was parked at build time.
		d.done = true
		//: nothing further to read.
		return
	}
	//: advance to the next part; io.EOF means the closing delimiter was read.
	next, nerr := d.inner.NextPart()
	//: clean end of stream.
	if errIsEOF(nerr) {
		//: latch so More stops promising values.
		d.done = true
		//: Decode will return io.EOF verbatim.
		return
	}
	//: a malformed header block or a truncated body.
	if nerr != nil {
		//: park the wrapped failure for the next Decode.
		d.pendErr = unmarshalFailed(nerr, "Decoder: reading the next part failed")
		//: nothing buffered.
		return
	}
	//: materialise the body under the configured bounds.
	d.readInto(next)
}

// readInto drains one *mime/multipart.Part into the look-ahead slot, refusing
// an over-budget body rather than materialising it.
func (d *multipartDecoder) readInto(src *stdmp.Part) {
	//: RFC 7578 §4.2: every part MUST carry a form-data name, and PartValue
	//: declares Name required — the encoder refuses to write a part without
	//: one, so a decode that produced one would hand back a value this codec
	//: cannot re-encode. Refused before the body is read, since it is not
	//: going to be kept.
	if src.FormName() == "" {
		//: a malformed body, not a caller mistake.
		d.pendErr = unmarshalFailed(nil, "Decoder: a part carries no form-data name (RFC 7578 §4.2)")
		//: nothing buffered.
		return
	}
	//: materialise the body under the byte budget and charge it.
	body, berr := d.readBody(src)
	//: a read fault or a crossed bound, already typed.
	if berr != nil {
		//: park it for the next Decode.
		d.pendErr = berr
		//: nothing buffered.
		return
	}
	//: publish the part into the look-ahead slot.
	d.pending = &PartValue{
		Name:        src.FormName(),
		FileName:    src.FileName(),
		ContentType: src.Header.Get("Content-Type"),
		Data:        body,
	}
}

// readBody materialises one part body under the tighter of the two byte
// bounds — MaxPartBytes, or what is left of MaxTotalBytes — and charges it
// against the counter. At most one byte past that budget is pulled from src,
// so the aggregate holds for the LAST part too, and an over-budget body is
// refused naming the bound that stopped it.
func (d *multipartDecoder) readBody(src io.Reader) (body []byte, err error) {
	//: the tighter bound, the knob that sets it, and that knob's ceiling.
	knob, ceiling, budget := d.count.readBudget()
	//: read one byte past the budget so an over-budget body is detected as
	//: overflow rather than silently truncated (transform/bounded.go's shape).
	read, overflow, rerr := readBounded(src, budget)
	//: a read fault mid-body is a wire failure.
	if rerr != nil {
		//: typed UNMARSHAL_FAILED.
		return nil, unmarshalFailed(rerr, "Decoder: reading a part body failed")
	}
	//: an over-budget body is refused by the bound, not by the reader.
	if overflow {
		//: the observed magnitude — the part, or the running total — is at
		//: least one past the ceiling of the knob that bound the read.
		return nil, limitExceeded(knob, ceiling, probeSize(ceiling))
	}
	//: charge the part count and the aggregate total.
	if lerr := d.count.admitPart(int64(len(read))); lerr != nil {
		//: typed LIMIT_EXCEEDED.
		return nil, lerr
	}
	//: within every bound.
	return read, nil
}

// readBounded drains r into a fresh buffer, refusing to materialise more than
// max bytes. Mirrors internal/service/transform.readAllBounded: the extra byte
// on the LimitReader is what separates a legitimately cap-sized payload from
// one that wanted to exceed the cap. See probeSize for the one max that gets
// no extra byte.
func readBounded(r io.Reader, max int64) (body []byte, overflow bool, err error) {
	//: LimitReader stops one byte past max so overflow is observable.
	limited := io.LimitReader(r, probeSize(max))
	//: ReadAll over the bounded reader is the single allocation point.
	buf, rerr := io.ReadAll(limited)
	//: a read fault is surfaced verbatim for the caller to wrap.
	if rerr != nil {
		//: hand the raw error back.
		return nil, false, rerr
	}
	//: more than the cap means the part tried to exceed its ceiling.
	if int64(len(buf)) > max {
		//: signal overflow; the caller builds the typed refusal.
		return nil, true, nil
	}
	//: within bounds.
	return buf, false, nil
}

// probeSize returns bound+1 — the read limit that makes a body over bound
// observable — or bound itself when bound is math.MaxInt64.
//
// bound+1 wraps to math.MinInt64 there, and io.LimitReader reads nothing at all
// through a negative limit: with MaxPartBytes at MaxInt64, which resolve
// accepts, every part decoded silently empty. No []byte can hold more than
// MaxInt64 bytes, so at that bound there is no overflow for the probe to see.
func probeSize(bound int64) int64 {
	//: below the int64 ceiling the probe byte fits.
	if bound < math.MaxInt64 {
		//: one past the bound.
		return bound + 1
	}
	//: saturate rather than wrap negative.
	return bound
}

// publishPart copies part through the caller's *PartValue, refusing a nil one.
func publishPart(part, target *PartValue) error {
	//: a nil pointer cannot receive anything.
	if target == nil {
		//: typed refusal.
		return valueInvalid("Decode called with a nil *PartValue")
	}
	//: publish through the caller's pointer.
	*target = *part
	//: nothing to wrap.
	return nil
}

// assignPart decodes a part body as JSON into the caller's value — the read
// side of the JSON-mediated shape.
func assignPart(part *PartValue, v any) error {
	//: encoding/json needs somewhere to write.
	if !isSettablePointer(v) {
		//: typed refusal — encoding/json would report this far less clearly.
		return valueInvalid("Decode target is not a non-nil pointer")
	}
	//: decode the part body as JSON into the caller's value.
	if uerr := stdjson.Unmarshal(part.Data, v); uerr != nil {
		//: wrap the stdlib error for reason-based matching.
		return unmarshalFailed(uerr, "Decoder: part body is not valid JSON")
	}
	//: decoded.
	return nil
}

// errIsEOF reports whether err is the clean end-of-stream signal the Decoder
// contract propagates verbatim.
func errIsEOF(err error) bool {
	//: io.EOF travels unwrapped through every streaming codec here.
	return errors.Is(err, io.EOF)
}

// isSettablePointer reports whether v is a non-nil pointer encoding/json can
// write through.
func isSettablePointer(v any) bool {
	//: obtain the reflect.Value without dereferencing.
	rv := reflect.ValueOf(v)
	//: a non-pointer or a nil pointer cannot receive a decoded value.
	return rv.Kind() == reflect.Pointer && !rv.IsNil()
}

// sniffReader wraps r in a bufio.Reader and recovers the boundary from the
// stream WITHOUT consuming it: Peek leaves the delimiter line in the buffer so
// mime/multipart.Reader still sees a complete body.
//
// It reads no further than the answer needs. The head is examined as it
// arrives, and the sniff returns once the first delimiter line is complete, so
// a producer that sends a short prefix and then pauses is not made to fill the
// whole window first — it used to be, which stalled NewDecoder on a live
// stream until 4 KiB or the end arrived.
func sniffReader(r io.Reader) (buffered *bufio.Reader, boundary string, err error) {
	//: size the buffer to the sniff window so the head always fits in it.
	br := bufio.NewReaderSize(r, boundarySniffWindow)
	//: Peek does not advance the reader; a short stream returns what it has.
	head, perr := peekHead(br)
	//: anything other than "the stream is shorter than the window" is fatal.
	if perr != nil && !errIsEOF(perr) && !errors.Is(perr, bufio.ErrBufferFull) {
		//: wrap the transport failure.
		return nil, "", unmarshalFailed(perr, "Decoder: reading the boundary failed")
	}
	//: recover the delimiter from the peeked head.
	found, berr := Boundary(head)
	//: propagate the typed BOUNDARY_INVALID verbatim.
	if berr != nil {
		//: caller parks it as the decoder's construction failure.
		return nil, "", berr
	}
	//: the buffered reader still holds every byte the caller supplied.
	return br, found, nil
}

// peekHead peeks at br until the first delimiter line is complete, the sniff
// window is full, or the stream ends — whichever comes first.
//
// A first line ending in "--" is not complete for this purpose. It can be a
// zero-part body's close delimiter or a boundary that itself ends in "--", and
// telling those apart needs the bytes after it, so that one line still waits
// for the window or the end of the stream, exactly as every line used to.
func peekHead(br peeker) (head []byte, err error) {
	want := 1
	//: grow the peek one arrival at a time; the window bounds the loop.
	for {
		head, err = br.Peek(want)
		//: a short stream, a full window or a transport failure ends it.
		if err != nil || len(head) >= boundarySniffWindow {
			//: the caller sorts EOF from failure.
			return head, err
		}
		//: take everything already buffered, not only what was asked for.
		head, err = br.Peek(br.Buffered())
		//: Peek within the buffered count cannot fail; stay defensive.
		if err != nil {
			//: surface it rather than guess.
			return head, err
		}
		//: a complete, unambiguous delimiter line is all Boundary reads.
		if delimiterLineComplete(head) {
			//: enough to answer.
			return head, nil
		}
		//: one byte more than is buffered: Peek blocks only until it arrives.
		want = len(head) + 1
	}
}

// peeker is the part of *bufio.Reader peekHead reads through: a look at the
// buffered bytes that consumes none of them.
type peeker interface {
	// Peek returns the next n bytes without advancing the reader.
	Peek(n int) ([]byte, error)
	// Buffered returns how many bytes can be peeked without a read.
	Buffered() int
}

// delimiterLineComplete reports whether head already holds the whole first
// "--" line — terminated by its LF — and that line does not end in "--".
func delimiterLineComplete(head []byte) bool {
	//: walk the complete lines only; a trailing partial line may still grow.
	for cursor := 0; ; {
		offset := bytes.IndexByte(head[cursor:], '\n')
		//: no LF yet — the line being scanned is not complete.
		if offset < 0 {
			//: keep reading.
			return false
		}
		line := bytes.TrimSuffix(head[cursor:cursor+offset], []byte{'\r'})
		//: the first delimiter-looking line decides.
		if candidate, ok := bytes.CutPrefix(line, []byte(delimiterPrefix)); ok {
			//: complete unless the candidate is the one ambiguous shape.
			return !bytes.HasSuffix(candidate, []byte(delimiterPrefix))
		}
		//: a preamble line; move past it.
		cursor += offset + 1
	}
}

// unmarshalFailed wraps cause with the UnmarshalFailed sentinel and a
// call-site specific Private diagnostic.
func unmarshalFailed(cause error, detail string) error {
	//: typed sentinel so callers route on CodeMultipartUnmarshalFailed.
	return errs.Wrap(cause, errs.WrapParams{
		Code:    CodeMultipartUnmarshalFailed,
		Reason:  "UNMARSHAL_FAILED",
		Public:  "multipart decoding failed",
		Private: "service/codec/multipart." + detail,
	})
}
