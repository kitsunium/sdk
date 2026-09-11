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
