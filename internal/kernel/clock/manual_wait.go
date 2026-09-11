// Package clock — hosts manualWait, the record a [ManualClock] keeps for one
// armed wait, plus the three lock-free helpers that operate on it.
package clock

import (
	"math"
	"time"
)

// maxDuration is the largest time.Duration, the bound rearmWait's single
// multiplication must stay under.
const maxDuration time.Duration = math.MaxInt64

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
//
// A ticker left behind by close to the whole Duration range is the exception:
// its deadline becomes target plus one period, which is still strictly after
// target and within one period of it, but no longer on the ticker's phase.
// time.Time.Sub saturates at ~292 years, so past that distance the phase
// cannot even be computed in a Duration — and the multiplication below would
// wrap NEGATIVE, move the deadline backwards, and leave advanceTo firing the
// same due wait forever while it holds the lock.
func rearmWait(w *manualWait, target time.Time) {
	//: how far the ticker is already behind the requested instant.
	elapsed := target.Sub(w.deadline)
	//: whole periods already missed; elapsed is non-negative because only a
	//: due wait is rearmed.
	skipped := elapsed / w.period
	//: skipped+1 periods must fit in a Duration, which is exactly this
	//: comparison — and a saturated Sub always fails it, so the unknowable
	//: phase is never guessed from a clipped distance.
	if skipped >= maxDuration/w.period {
		//: the first tick strictly after target that one addition can reach.
		w.deadline = target.Add(w.period)
		//: still O(1), however far the clock jumped.
		return
	}
	//: the +1 lands strictly after target even when the division is exact,
	//: and it is one addition regardless of how many periods were skipped.
	w.deadline = w.deadline.Add(w.period * (skipped + 1))
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
