// Package clock — hosts the waiting half of the time port: the [Waiter]
// contract, plus the single guard both implementations funnel their
// non-positive-period refusal through.
package clock

import "time"

// Waiter suspends a caller until a duration has elapsed on some clock. It is
// the half of the time abstraction that [Clock] deliberately does not carry.
//
// Implementations must be safe for concurrent use by multiple goroutines.
type Waiter interface {
	// After returns a channel that delivers the fire instant once d has
	// elapsed. A non-positive d delivers at once.
	After(d time.Duration) <-chan time.Time
	// NewTimer returns a one-shot [Timer] due d from now. A non-positive d
	// fires at once.
	NewTimer(d time.Duration) Timer
	// NewTicker returns a repeating [Ticker] of period d. It PANICS on a
	// non-positive d; see the package comment.
	NewTicker(d time.Duration) Ticker
	// Sleep blocks the calling goroutine until d has elapsed on this clock.
	// A non-positive d returns at once.
	Sleep(d time.Duration)
}

// requirePositivePeriod panics unless d is a usable ticker period. Both
// implementations funnel through it so the refusal is identical whichever
// clock is installed — a test that pins the message pins it for production.
func requirePositivePeriod(op string, d time.Duration) {
	//: a non-positive period is a programmer error, not a runtime condition:
	//: there is no cadence to run and no non-arbitrary value to substitute.
	if d <= 0 {
		//: name the package and the offending value so the stack is enough.
		panic("clock: " + op + " requires a period > 0, got " + d.String())
	}
}
