// Package clock — hosts manualTicker, the [Ticker] handle a [ManualClock]
// hands out for a repeating wait.
package clock

import "time"

// manualTicker is the [Ticker] facade over one manualWait.
type manualTicker struct {
	// clk owns the lock and the armed set.
	clk *ManualClock
	// w is this ticker's wait; its channel is fixed at registration.
	w *manualWait
}

// C returns the ticker's delivery channel.
func (t *manualTicker) C() <-chan time.Time {
	//: same fixed-at-registration rationale as manualTimer.C.
	return t.w.ch
}

// Stop halts the ticker without draining an already-delivered tick.
func (t *manualTicker) Stop() {
	//: no drain — time.Ticker.Stop leaves a delivered tick for the receiver.
	t.clk.stopWait(t.w, false)
}

// Reset restarts the ticker with period d, panicking on a non-positive d.
func (t *manualTicker) Reset(d time.Duration) {
	//: identical refusal to systemTicker.Reset, message included.
	requirePositivePeriod("Ticker.Reset", d)
	//: no drain, matching time.Ticker.Reset.
	t.clk.resetWait(t.w, d, false)
}
