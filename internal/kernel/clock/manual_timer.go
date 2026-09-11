// Package clock — hosts manualTimer, the [Timer] handle a [ManualClock] hands
// out for a one-shot wait.
package clock

import "time"

// manualTimer is the [Timer] facade over one manualWait.
type manualTimer struct {
	// clk owns the lock and the armed set; the wait alone is not self-contained.
	clk *ManualClock
	// w is this timer's wait; its channel is fixed at registration.
	w *manualWait
}

// C returns the timer's delivery channel.
func (t *manualTimer) C() <-chan time.Time {
	//: ch is written once at registration and never reassigned, so this needs
	//: no lock — which matters, since callers select on it while holding none.
	return t.w.ch
}

// Stop disarms the timer, reporting whether it was still armed.
func (t *manualTimer) Stop() bool {
	//: drain, so no value from a previous arming survives Stop.
	return t.clk.stopWait(t.w)
}

// Reset re-arms the timer for d, reporting whether it was still armed.
func (t *manualTimer) Reset(d time.Duration) bool {
	//: drain, so no value from a previous arming survives Reset.
	return t.clk.resetWait(t.w, d)
}
