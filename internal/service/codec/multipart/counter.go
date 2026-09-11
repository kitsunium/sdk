// Package multipart — the running tally a LimitsConfig is checked against.
package multipart

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
