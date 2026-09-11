package clock_test

import (
	"testing"
	"testing/synctest"
	"time"

	"github.com/kitsunium/sdk/internal/kernel/clock"
)

// The two tests below exist to keep CLAUDE.md §"clock vs testing/synctest"
// honest. Both of its load-bearing claims are asserted here rather than
// asserted in prose, because the interesting half of that comparison is the
// half that says the manual clock is NOT needed.
//
// Neither test calls t.Parallel: synctest.Test forbids T.Parallel, T.Run and
// T.Deadline on the bubbled *testing.T, and the outer T is the one it bubbles.

// TestSystemObservesSynctestFakeClock pins the first claim: inside a bubble,
// package time is fake, and clock.System delegates to package time — so an
// injected System ALREADY reads simulated time. Nothing needs replacing, and
// any documentation that says otherwise is wrong.
func TestSystemObservesSynctestFakeClock(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		start := clock.System.Now()
		//: the bubble's clock is documented to start at midnight UTC 2000-01-01.
		if got := start.UTC().Year(); got != 2000 {
			t.Fatalf("clock.System.Now().Year() inside a bubble = %d, want 2000", got)
		}
		//: an hour of bubble time passes with no wall-clock cost at all.
		clock.System.Sleep(time.Hour)
		if got := clock.System.Since(start); got != time.Hour {
			t.Errorf("clock.System.Since(start) after Sleep(1h) = %v, want 1h0m0s", got)
		}
		//: and the waiting half is fake too — this returns instantly.
		<-clock.System.After(24 * time.Hour)
		if got := clock.System.Since(start); got != 25*time.Hour {
			t.Errorf("clock.System.Since(start) after a further After(24h) = %v, want 25h0m0s", got)
		}
	})
}

// TestManualClockIgnoresSynctestBubble pins the second claim: a ManualClock is
// a wholly independent time source. A bubble advancing by an hour does not move
// it, and it still answers to Advance — which is why the two mechanisms compose
// rather than compete, and why a ManualClock is the right tool wherever a
// bubble cannot go (real I/O, parallel subtests, a non-2000 epoch).
func TestManualClockIgnoresSynctestBubble(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		//: an origin the bubble clock can never produce.
		m := clock.NewManualClock(testEpoch)
		tm := m.NewTimer(time.Minute)
		//: burn an hour of bubble time.
		time.Sleep(time.Hour)
		if got := m.Now(); !got.Equal(testEpoch) {
			t.Errorf("ManualClock moved with the bubble: Now() = %v, want %v", got, testEpoch)
		}
		if _, fired := pending(tm.C()); fired {
			t.Error("a ManualClock timer fired on bubble time")
		}
		//: only the caller moves it.
		m.Advance(time.Minute)
		at, fired := pending(tm.C())
		if !fired {
			t.Fatal("the timer did not fire when the ManualClock was advanced")
		}
		if want := testEpoch.Add(time.Minute); !at.Equal(want) {
			t.Errorf("timer delivered %v, want %v", at, want)
		}
	})
}
