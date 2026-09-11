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

// Stop halts the ticker and discards a tick delivered but not yet received.
func (t *manualTicker) Stop() {
	//: drain, matching time.Ticker.Stop since Go 1.23: its channel is
	//: synchronous, so a tick nobody received before Stop is never received.
	t.clk.stopWait(t.w)
}

// Reset restarts the ticker with period d, panicking on a non-positive d.
func (t *manualTicker) Reset(d time.Duration) {
	//: identical refusal to systemTicker.Reset, message included.
	requirePositivePeriod("Ticker.Reset", d)
	//: drain, matching time.Ticker.Reset since Go 1.23: the next tick is the
	//: one the new period produces, not one left over from the old.
	t.clk.resetWait(t.w, d)
}
