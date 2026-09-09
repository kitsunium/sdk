// Package clock — hosts manualWait, the record a [ManualClock] keeps for one
// armed wait, plus the three lock-free helpers that operate on it.
package clock

import "time"

// manualWait is one armed wait on a ManualClock: a timer (period == 0) or a
// ticker (period > 0). Every field is guarded by the owning clock's mutex
// except ch, which is created once at registration and never reassigned — that
// exception is what lets a caller select on the channel while holding nothing.
type manualWait struct {
	// ch delivers the fire instant. Buffered with capacity 1 and written with
	// a non-blocking send, because Advance MUST NOT block on a receiver that
	// may never come — see the ManualClock doc comment.
	ch chan time.Time
	// deadline is the absolute instant at which this wait is due.
	deadline time.Time
	// period is the ticker cadence; zero marks a one-shot timer.
	period time.Duration
	// linked reports whether this wait is currently in ManualClock.waits.
	linked bool
	// seq is the registration order, used to break deadline ties.
	seq uint64
}

// rearmWait moves a ticker's deadline to the first period strictly after
// target, in one step. Caller holds the owning clock's mu.
func rearmWait(w *manualWait, target time.Time) {
	//: how far the ticker is already behind the requested instant.
	elapsed := target.Sub(w.deadline)
	//: the +1 lands strictly after target even when the division is exact.
	steps := elapsed/w.period + 1
	//: one addition regardless of how many periods were skipped.
	w.deadline = w.deadline.Add(w.period * steps)
}

// fireWait delivers at on w's channel without blocking. Caller holds the
// owning clock's mu.
func fireWait(w *manualWait, at time.Time) {
	//: a full buffer means the previous tick is still undelivered; dropping it
	//: is exactly what time.Ticker does for a slow receiver.
	select {
	case w.ch <- at:
	default:
	}
}

// drainWait removes an undelivered value from w's channel. Caller holds the
// owning clock's mu.
func drainWait(w *manualWait) {
	//: a non-blocking receive: there is at most one buffered value.
	select {
	case <-w.ch:
	default:
	}
}
