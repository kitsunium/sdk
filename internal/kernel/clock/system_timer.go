// Package clock — hosts systemTimer, the adapter that presents *time.Timer
// through the [Timer] interface.
package clock

import "time"

// systemTimer adapts *time.Timer to the [Timer] interface. It is a value type
// holding exactly one pointer, so converting it to Timer is allocation-free
// (the runtime stores a pointer-shaped value directly in the interface word).
type systemTimer struct {
	// tm is the wrapped stdlib timer; never nil once constructed.
	tm *time.Timer
}

// C returns the stdlib timer's delivery channel.
func (t systemTimer) C() <-chan time.Time {
	//: expose the stdlib field as the method the interface requires.
	return t.tm.C
}

// Stop halts the timer, reporting whether it was still armed.
func (t systemTimer) Stop() bool {
	//: since Go 1.23 the channel is unbuffered, so Stop leaves nothing stale.
	return t.tm.Stop()
}

// Reset re-arms the timer for d, reporting whether it was still armed.
func (t systemTimer) Reset(d time.Duration) bool {
	//: delegate; the stdlib guarantees no stale value survives the reset.
	return t.tm.Reset(d)
}
