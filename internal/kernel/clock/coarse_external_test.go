package clock_test

import (
	"runtime"
	"testing"
	"testing/synctest"
	"time"

	"github.com/kitsunium/sdk/internal/kernel/clock"
)

// awaitReading waits, on the real scheduler, until coarse reads want: the
// publishing goroutine runs on its own after a tick.
func awaitReading(t *testing.T, coarse *clock.CoarseClock, want time.Time) {
	t.Helper()
	for range 100_000 {
		if coarse.Now().Equal(want) {
			return
		}
		runtime.Gosched()
	}
	t.Fatalf("coarse reading %v never reached %v", coarse.Now(), want)
}

// TestCoarseLagsByAtMostOneResolution drives a CoarseClock from a
// ManualClock: between ticks it reads the last tick's instant, never more
// than one resolution behind, and a deadline it measures is seen no later
// than one resolution after it passed (#814).
func TestCoarseLagsByAtMostOneResolution(t *testing.T) {
	t.Parallel()
	const resolution = 10 * time.Millisecond
	base := clock.NewManualClock(testEpoch)
	coarse := clock.NewCoarse(base, resolution)
	defer coarse.Stop()
	if got := coarse.Now(); !got.Equal(testEpoch) {
		t.Fatalf("first reading = %v, want the base's %v", got, testEpoch)
	}
	deadline := testEpoch.Add(35 * time.Millisecond)
	for step := range 6 {
		base.BlockUntil(1)
		base.Advance(resolution)
		awaitReading(t, coarse, base.Now())
		if lag := base.Since(coarse.Now()); lag < 0 || lag >= resolution {
			t.Fatalf("step %d: lag %v outside [0, %v)", step, lag, resolution)
		}
		passed := !coarse.Now().Before(deadline)
		if base.Since(deadline) >= resolution && !passed {
			t.Fatalf("step %d: a deadline %v behind the base is not seen as passed", step, base.Since(deadline))
		}
	}
}

// TestCoarseStopJoinsItsGoroutine runs in a synctest bubble, which fails the
// test if any goroutine it started is still alive when it returns: Stop must
// end the publisher, and a stopped clock reads its base.
func TestCoarseStopJoinsItsGoroutine(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		coarse := clock.NewCoarse(clock.System, time.Millisecond)
		start := coarse.Now()
		time.Sleep(10*time.Millisecond + time.Microsecond)
		synctest.Wait()
		if got := coarse.Since(start); got < 9*time.Millisecond || got > 10*time.Millisecond {
			t.Errorf("after 10ms of bubble time the coarse clock moved %v", got)
		}
		coarse.Stop()
		coarse.Stop()
		time.Sleep(time.Hour)
		if got := coarse.Since(start); got < time.Hour {
			t.Errorf("a stopped clock read %v after an hour: it must read its base", got)
		}
	})
}

// TestNewCoarsePanicsOnANonPositiveResolution mirrors NewTicker's refusal.
func TestNewCoarsePanicsOnANonPositiveResolution(t *testing.T) {
	t.Parallel()
	defer func() {
		if recover() == nil {
			t.Fatal("NewCoarse(base, 0) did not panic")
		}
	}()
	clock.NewCoarse(clock.NewManualClock(testEpoch), 0)
}
