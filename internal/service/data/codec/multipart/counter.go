package multipart

import "io"

// counter tracks the running part count and aggregate byte total against a
// resolved LimitsConfig. Shared by the encode and decode paths so both refuse
// on exactly the same arithmetic — a bound the encoder ignored would produce
// bodies this package cannot read back.
type counter struct {
	// limits is the resolved ceiling set; never a raw caller value.
	limits LimitsConfig
	// parts is the number of parts admitted so far.
	parts int
	// total is the sum of the admitted part bodies.
	total int64
}

// readBudget returns how many body bytes the next part may materialise — the
// tighter of the per-part cap and what is left of the aggregate — with the
// knob that sets it and that knob's configured ceiling, so a refusal names the
// bound that actually stopped the read.
//
// The decoder used to read a part up to MaxPartBytes and only then charge the
// aggregate, so the last part could overrun MaxTotalBytes by up to a whole
// part: 1.5× the aggregate with the defaults, and without limit when
// MaxPartBytes exceeds MaxTotalBytes, which resolve accepts. On a tie the
// per-part cap is named, matching the order admitPart checks them in.
func (c *counter) readBudget() (knob string, ceiling, budget int64) {
	//: never negative: a part that crossed the aggregate was refused, and a
	//: refusal latches the decoder before another part is read.
	remaining := c.limits.MaxTotalBytes - c.total
	//: the aggregate is the tighter bound, so it is the one that binds.
	if remaining < c.limits.MaxPartBytes {
		//: name the aggregate knob; the budget is what is left of it.
		return "MaxTotalBytes", c.limits.MaxTotalBytes, remaining
	}
	//: the per-part cap binds, or ties with the aggregate.
	return "MaxPartBytes", c.limits.MaxPartBytes, c.limits.MaxPartBytes
}

// admitPart charges one part of size n against the counter, returning a typed
// LimitExceeded error when any of the three ceilings is crossed.
func (c *counter) admitPart(n int64) error {
	//: charge the part slot first — a count overflow is cheaper to detect.
	c.parts++
	//: part-count ceiling.
	if c.parts > c.limits.MaxParts {
		//: refuse with the knob and its ceiling; the payload is never echoed.
		return limitExceeded("MaxParts", int64(c.limits.MaxParts), int64(c.parts))
	}
	//: per-part ceiling.
	if n > c.limits.MaxPartBytes {
		//: refuse naming the offending part size against its bound.
		return limitExceeded("MaxPartBytes", c.limits.MaxPartBytes, n)
	}
	//: aggregate ceiling.
	c.total += n
	//: total across every part seen so far.
	if c.total > c.limits.MaxTotalBytes {
		//: refuse naming the running total against its bound.
		return limitExceeded("MaxTotalBytes", c.limits.MaxTotalBytes, c.total)
	}
	//: within every bound.
	return nil
}

// wireBudget charges every byte mime/multipart pulls from the body —
// delimiters, header blocks, bodies and the reader's look-ahead alike —
// against MaxTotalBytes (#908). The part-body bounds above are charged after
// NextPart returned; a header block is read by mime/multipart itself, whose
// only bounds are the GODEBUG-governed multipartmaxheaders defaults, which a
// process can lift at run time. Under this reader a body cannot pull more
// than one byte past the ceiling, whatever its parts' headers hold.
type wireBudget struct {
	// r is the body.
	r io.Reader
	// err is the refusal, latched once the ceiling is crossed.
	err error
	// limit is MaxTotalBytes.
	limit int64
	// read counts the bytes pulled from r.
	read int64
}

// Read pulls from the body no further than one byte past the ceiling, and
// refuses once that byte arrives.
func (w *wireBudget) Read(p []byte) (int, error) {
	//: a crossed ceiling stays crossed.
	if w.err != nil {
		//: latched.
		return 0, w.err
	}
	//: never ask the body for more than the probe byte past the ceiling.
	if room := probeSize(w.limit) - w.read; int64(len(p)) > room {
		p = p[:room]
	}
	n, err := w.r.Read(p)
	w.read += int64(n)
	//: the byte past the ceiling arrived: the body is over it.
	if w.read > w.limit {
		w.err = limitExceeded("MaxTotalBytes", w.limit, w.read)
		//: the bytes up to the ceiling were legitimately read.
		return n, w.err
	}
	//: within the ceiling.
	return n, err
}

// admitEncodedPart charges one part the encoder is about to write: the part
// against MaxParts, its body against MaxPartBytes, and every byte it puts on
// the wire — delimiter and header block included — against MaxTotalBytes,
// keeping room for the closing delimiter, so the encoder emits no body the
// decoder's wireBudget would refuse and Close never crosses the ceiling (#908).
func (c *counter) admitEncodedPart(body, wire, closing int64) error {
	//: charge the part slot first — a count overflow is cheaper to detect.
	c.parts++
	//: part-count ceiling.
	if c.parts > c.limits.MaxParts {
		//: refuse with the knob and its ceiling; the payload is never echoed.
		return limitExceeded("MaxParts", int64(c.limits.MaxParts), int64(c.parts))
	}
	//: per-part ceiling, on the body alone.
	if body > c.limits.MaxPartBytes {
		//: refuse naming the offending part size against its bound.
		return limitExceeded("MaxPartBytes", c.limits.MaxPartBytes, body)
	}
	c.total += wire
	//: the whole body so far, plus the delimiter that will close it.
	if c.total+closing > c.limits.MaxTotalBytes {
		//: refuse naming the body's size against its bound.
		return limitExceeded("MaxTotalBytes", c.limits.MaxTotalBytes, c.total+closing)
	}
	//: within every bound.
	return nil
}

// admitClosing checks that the closing delimiter still fits under
// MaxTotalBytes — it always does after a part, which reserved it; a body
// with no part has only it to charge.
func (c *counter) admitClosing(closing int64) error {
	//: the body plus its closing delimiter.
	if c.total+closing > c.limits.MaxTotalBytes {
		//: refuse naming the body's size against its bound.
		return limitExceeded("MaxTotalBytes", c.limits.MaxTotalBytes, c.total+closing)
	}
	//: within the bound.
	return nil
}
