// Package resilience — the sliding window's slots and totals.
package resilience

import (
	"testing"
	"time"
)

// Test_slidingWindow_advance pins the slot arithmetic: the totals are always
// the sum of the slots the window still reaches, a gap shorter than the
// window empties only the slots it reuses, and a clock set back counts in the
// latest slot rather than in one the window already left.
func Test_slidingWindow_advance(t *testing.T) {
	t.Parallel()
	origin := time.Unix(1_700_000_000, 0)
	settings, knob := resolveWindow(BreakerWindowValue{Size: 4 * time.Second, Buckets: 4, MinCalls: 1000})
	if knob != "" {
		t.Fatalf("refused %q", knob)
	}
	w := newWindow(settings, origin)
	at := func(d time.Duration) time.Time { return origin.Add(d) }

	type step struct {
		at                     time.Duration
		failed, slow           bool
		calls, failures, slows int
	}
	for i, s := range []step{
		{0, true, false, 1, 1, 0},
		{time.Second, false, true, 2, 1, 1},
		{3 * time.Second, true, true, 3, 2, 2},
		//: epoch 4 reuses epoch 0's slot: its failure leaves.
		{4 * time.Second, false, false, 3, 1, 2},
		//: a clock set back to epoch 1 counts in epoch 4's slot.
		{time.Second, true, false, 4, 2, 2},
		//: epoch 6 reuses the slots of epochs 1 and 2: epoch 1's slow call leaves.
		{6 * time.Second, false, false, 4, 2, 1},
		//: a gap of the whole window empties every slot.
		{time.Minute, false, false, 1, 0, 0},
	} {
		w.add(at(s.at), s.failed, s.slow)
		if w.calls != s.calls || w.failures != s.failures || w.slow != s.slows {
			t.Fatalf("step %d: totals %d/%d/%d, want %d/%d/%d", i, w.calls, w.failures, w.slow, s.calls, s.failures, s.slows)
		}
		var calls, failures, slows int
		for _, b := range w.buckets {
			calls, failures, slows = calls+b.calls, failures+b.failures, slows+b.slow
		}
		if calls != w.calls || failures != w.failures || slows != w.slow {
			t.Fatalf("step %d: the slots sum to %d/%d/%d, the totals say %d/%d/%d", i, calls, failures, slows, w.calls, w.failures, w.slow)
		}
	}
}
