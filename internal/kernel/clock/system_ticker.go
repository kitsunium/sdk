// Package clock — hosts systemTicker, the adapter that presents *time.Ticker
// through the [Ticker] interface.
package clock

import "time"

// systemTicker adapts *time.Ticker to the [Ticker] interface. Same one-pointer
// shape as systemTimer, and the same allocation-free boxing.
type systemTicker struct {
	// tk is the wrapped stdlib ticker; never nil once constructed.
	tk *time.Ticker
}

// C returns the stdlib ticker's delivery channel.
func (t systemTicker) C() <-chan time.Time {
	//: expose the stdlib field as the method the interface requires.
	return t.tk.C
}

// Stop halts the ticker without closing its channel.
func (t systemTicker) Stop() {
	//: delegate; since Go 1.23 the runtime itself guarantees that a tick
	//: nobody received before Stop is never received after it.
	t.tk.Stop()
}

// Reset restarts the ticker with period d, panicking on a non-positive d.
func (t systemTicker) Reset(d time.Duration) {
	//: own the refusal so the message matches ManualClock's exactly.
	requirePositivePeriod("Ticker.Reset", d)
	//: delegate the re-arming to the stdlib ticker.
	t.tk.Reset(d)
}
