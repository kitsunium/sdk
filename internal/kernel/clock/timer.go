// Package clock — hosts the [Timer] contract, the handle a [Waiter] returns
// for a one-shot wake-up.
package clock

import "time"

// Timer is a one-shot wake-up scheduled on some clock. It mirrors the usable
// surface of *time.Timer, with one unavoidable difference: the stdlib exposes
// the delivery channel as a FIELD (t.C), and no interface can require a field,
// so it is a method here.
//
// Implementations must be safe for concurrent use by multiple goroutines.
type Timer interface {
	// C returns the channel on which the fire instant is delivered. The same
	// channel is returned by every call; it is never closed.
	C() <-chan time.Time
	// Stop prevents a not-yet-fired Timer from firing and reports whether the
	// Timer was still armed. After Stop returns, no value from a previous
	// arming is left pending on C.
	Stop() bool
	// Reset re-arms the Timer to fire d from the clock's current instant and
	// reports whether it was still armed. After Reset returns, no value from a
	// previous arming is left pending on C. A non-positive d fires at once.
	Reset(d time.Duration) bool
}
