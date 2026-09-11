package clock_test

import (
	"sync"
	"testing"
	"time"

	"github.com/kitsunium/sdk/internal/kernel/clock"
)

// pending drains one value from ch without blocking. Every assertion in this
// file uses it rather than a receive with a timeout: a ManualClock fires
// synchronously inside Advance, so by the time Advance has returned the value
// is either in the channel or it never will be. That is the determinism the
// whole type exists for — a test here never races and never sleeps.
func pending(ch <-chan time.Time) (time.Time, bool) {
	select {
	case v := <-ch:
		return v, true
	default:
		return time.Time{}, false
	}
}

func TestManualNowIsTheInjectedOrigin(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		start time.Time
	}{
		//: every one of these is unreachable with testing/synctest, whose
		//: bubble clock is fixed at 2000-01-01 UTC and only moves forward.
		{"the Unix epoch", time.Unix(0, 0).UTC()},
		{"before the Unix epoch", time.Date(1969, 7, 20, 20, 17, 40, 0, time.UTC)},
		{"the 32-bit time_t rollover", time.Date(2038, 1, 19, 3, 14, 7, 0, time.UTC)},
		{"a non-UTC location", time.Date(2031, 3, 7, 4, 5, 6, 789, time.FixedZone("UTC+7", 7*60*60))},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			m := clock.NewManualClock(tc.start)
			if got := m.Now(); !got.Equal(tc.start) {
				t.Errorf("Now() = %v, want %v", got, tc.start)
			}
			//: Since is measured against the controlled instant, never the wall.
			if got := m.Since(tc.start.Add(-time.Hour)); got != time.Hour {
				t.Errorf("Since(start-1h) = %v, want 1h0m0s", got)
			}
		})
	}
}

func TestManualAdvanceFiresOnlyDueTimers(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		delays    []time.Duration
		advance   time.Duration
		wantFired []bool
	}{
		{
			name:      "advancing past the first of three",
			delays:    []time.Duration{10 * time.Millisecond, 20 * time.Millisecond, 30 * time.Millisecond},
			advance:   15 * time.Millisecond,
			wantFired: []bool{true, false, false},
		},
		{
			name:      "advancing exactly onto a deadline fires it",
			delays:    []time.Duration{10 * time.Millisecond, 20 * time.Millisecond},
			advance:   20 * time.Millisecond,
			wantFired: []bool{true, true},
		},
		{
			name:      "advancing short of every deadline fires nothing",
			delays:    []time.Duration{time.Second, 2 * time.Second},
			advance:   999 * time.Millisecond,
			wantFired: []bool{false, false},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			m := clock.NewManualClock(testEpoch)
			timers := make([]clock.Timer, len(tc.delays))
			for i, d := range tc.delays {
				timers[i] = m.NewTimer(d)
			}
			m.Advance(tc.advance)
			for i, want := range tc.wantFired {
				at, fired := pending(timers[i].C())
				if fired != want {
					t.Errorf("timer[%d] fired = %v, want %v", i, fired, want)
					continue
				}
				//: a fired timer carries its OWN deadline, not the instant the
				//: clock happened to be advanced to.
				if fired {
					wantAt := testEpoch.Add(tc.delays[i])
					if !at.Equal(wantAt) {
						t.Errorf("timer[%d] delivered %v, want its deadline %v", i, at, wantAt)
					}
				}
			}
			//: the clock always settles on the requested instant.
			if got, want := m.Now(), testEpoch.Add(tc.advance); !got.Equal(want) {
				t.Errorf("Now() after Advance = %v, want %v", got, want)
			}
		})
	}
}

func TestManualTickerDeliversOneTickPerAdvance(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		period     time.Duration
		advances   []time.Duration
		wantTickAt []time.Duration // zero means "no tick expected on this advance"
	}{
		{
			name:       "a jump spanning three periods still delivers one tick",
			period:     10 * time.Millisecond,
			advances:   []time.Duration{35 * time.Millisecond, 5 * time.Millisecond},
			wantTickAt: []time.Duration{10 * time.Millisecond, 40 * time.Millisecond},
		},
		{
			name:       "one advance per period delivers one tick each",
			period:     time.Second,
			advances:   []time.Duration{time.Second, time.Second, time.Second},
			wantTickAt: []time.Duration{time.Second, 2 * time.Second, 3 * time.Second},
		},
		{
			name:       "an advance short of the period delivers nothing",
			period:     time.Second,
			advances:   []time.Duration{900 * time.Millisecond, 100 * time.Millisecond},
			wantTickAt: []time.Duration{0, time.Second},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			m := clock.NewManualClock(testEpoch)
			tk := m.NewTicker(tc.period)
			defer tk.Stop()
			var elapsed time.Duration
			for i, step := range tc.advances {
				m.Advance(step)
				elapsed += step
				at, ticked := pending(tk.C())
				want := tc.wantTickAt[i]
				if ticked != (want != 0) {
					t.Fatalf("advance %d (to +%v): ticked = %v, want %v", i, elapsed, ticked, want != 0)
				}
				if ticked && !at.Equal(testEpoch.Add(want)) {
					t.Errorf("advance %d: tick at %v, want %v", i, at, testEpoch.Add(want))
				}
			}
		})
	}
}

func TestManualTickerDropsUndeliveredTicks(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		period   time.Duration
		advances int
	}{
		{"three undrained advances collapse to one buffered tick", time.Second, 3},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			m := clock.NewManualClock(testEpoch)
			tk := m.NewTicker(tc.period)
			defer tk.Stop()
			//: never drain between advances — the cap-1 channel keeps the
			//: first tick and drops the rest, exactly like time.Ticker.
			for range tc.advances {
				m.Advance(tc.period)
			}
			at, ticked := pending(tk.C())
			if !ticked {
				t.Fatal("expected one buffered tick")
			}
			if want := testEpoch.Add(tc.period); !at.Equal(want) {
				t.Errorf("buffered tick = %v, want the FIRST tick %v", at, want)
			}
			if _, more := pending(tk.C()); more {
				t.Error("a second tick was queued; the channel must hold at most one")
			}
		})
	}
}

func TestManualTimerStop(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name          string
		delay         time.Duration
		advanceBefore time.Duration
		wantStop      bool
		wantPending   bool
	}{
		{"stopping before the deadline prevents the fire", time.Second, 0, true, false},
		{"stopping after the fire reports not-armed and drains", time.Second, time.Second, false, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			m := clock.NewManualClock(testEpoch)
			tm := m.NewTimer(tc.delay)
			m.Advance(tc.advanceBefore)
			if got := tm.Stop(); got != tc.wantStop {
				t.Errorf("Stop() = %v, want %v", got, tc.wantStop)
			}
			//: Go 1.23+ promises no stale value survives Stop; the manual timer
			//: keeps that promise by draining its one-slot buffer.
			if _, got := pending(tm.C()); got != tc.wantPending {
				t.Errorf("value pending after Stop = %v, want %v", got, tc.wantPending)
			}
			//: a stopped timer never fires, however far the clock moves.
			m.Advance(time.Hour)
			if _, got := pending(tm.C()); got {
				t.Error("a stopped timer fired on a later Advance")
			}
		})
	}
}

func TestManualTimerReset(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		delay     time.Duration
		advance   time.Duration
		reset     time.Duration
		wantArmed bool
		fireAfter time.Duration
	}{
		{"resetting an armed timer moves its deadline", time.Second, 0, 2 * time.Second, true, 2 * time.Second},
		{"resetting a fired timer re-arms it", time.Second, time.Second, time.Second, false, time.Second},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			m := clock.NewManualClock(testEpoch)
			tm := m.NewTimer(tc.delay)
			m.Advance(tc.advance)
			if got := tm.Reset(tc.reset); got != tc.wantArmed {
				t.Errorf("Reset() = %v, want %v", got, tc.wantArmed)
			}
			//: Reset drains, so the pre-reset firing is gone.
			if _, got := pending(tm.C()); got {
				t.Error("a value survived Reset; the previous arming must be discarded")
			}
			//: one nanosecond short of the new deadline changes nothing.
			m.Advance(tc.fireAfter - time.Nanosecond)
			if _, got := pending(tm.C()); got {
				t.Error("the timer fired before its reset deadline")
			}
			m.Advance(time.Nanosecond)
			if _, got := pending(tm.C()); !got {
				t.Error("the timer did not fire at its reset deadline")
			}
		})
	}
}

// TestManualTickerStopAndResetLeaveNoStaleTick holds the manual ticker to the
// ticker it doubles. Since Go 1.23 a time.Ticker's channel is synchronous, and
// the runtime guarantees that no tick prepared before a Stop or a Reset is
// received after it — TestSystemTickerLeavesNoStaleTickAcrossStopAndReset
// pins that on the real one. A double that kept the undelivered tick would
// hand a caller who stopped a ticker one more wake-up, and hand a caller who
// reset it a tick immediately instead of after the new period.
//
// Seen failing: with manualTicker's Stop and Reset restored to not draining,
// the two cases printed
//
//	a tick delivered before Stop was still receivable after it (at 2031-03-07
//	04:05:07.000000789 +0000 UTC)
//	a tick delivered before Reset was still receivable after it (at 2031-03-07
//	04:05:07.000000789 +0000 UTC) — the next one belongs after the new period
func TestManualTickerStopAndResetLeaveNoStaleTick(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		reset bool
	}{
		{"Stop", false},
		{"Reset", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			m := clock.NewManualClock(testEpoch)
			tk := m.NewTicker(time.Second)
			defer tk.Stop()
			//: a tick falls due and nobody receives it.
			m.Advance(time.Second)
			if !tc.reset {
				tk.Stop()
				if at, stale := pending(tk.C()); stale {
					t.Fatalf("a tick delivered before Stop was still receivable after it (at %v)", at)
				}
				//: and the ticker is halted from here on.
				m.Advance(10 * time.Second)
				if _, more := pending(tk.C()); more {
					t.Error("a stopped ticker kept ticking")
				}
				return
			}
			tk.Reset(time.Minute)
			if at, stale := pending(tk.C()); stale {
				t.Fatalf("a tick delivered before Reset was still receivable after it (at %v) — the "+
					"next one belongs after the new period", at)
			}
			//: nothing until the new period has elapsed in full.
			m.Advance(time.Minute - time.Nanosecond)
			if at, early := pending(tk.C()); early {
				t.Fatalf("a tick arrived at %v, before the new period elapsed", at)
			}
			m.Advance(time.Nanosecond)
			at, ticked := pending(tk.C())
			if !ticked {
				t.Fatal("no tick when the new period elapsed")
			}
			if want := testEpoch.Add(time.Second + time.Minute); !at.Equal(want) {
				t.Errorf("tick after Reset = %v, want %v", at, want)
			}
		})
	}
}

func TestManualSetMovesTimeBothWays(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		delay     time.Duration
		to        time.Duration // relative to the origin
		wantFired bool
	}{
		{"forward past the deadline fires", time.Second, 2 * time.Second, true},
		{"backwards fires nothing", time.Second, -time.Hour, false},
		{"forward but short of the deadline fires nothing", time.Second, 500 * time.Millisecond, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			m := clock.NewManualClock(testEpoch)
			tm := m.NewTimer(tc.delay)
			target := testEpoch.Add(tc.to)
			m.Set(target)
			if got := m.Now(); !got.Equal(target) {
				t.Errorf("Now() = %v, want %v", got, target)
			}
			if _, got := pending(tm.C()); got != tc.wantFired {
				t.Errorf("fired = %v, want %v", got, tc.wantFired)
			}
			//: deadlines are absolute — a rewound clock re-reaches them, so the
			//: timer still fires once time gets there again.
			m.Set(testEpoch.Add(tc.delay))
			if !tc.wantFired {
				if _, got := pending(tm.C()); !got {
					t.Error("the timer did not fire when time reached its deadline again")
				}
			}
		})
	}
}

func TestManualAdvanceNegativePanics(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		d    time.Duration
		want string
	}{
		{"one second back", -time.Second, "clock: ManualClock.Advance requires d >= 0, got -1s (use Set to move time backwards)"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			m := clock.NewManualClock(testEpoch)
			if got := recoverPanic(t, func() { m.Advance(tc.d) }); got != tc.want {
				t.Errorf("panic = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestManualPendingCountsArmedWaits(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
	}{
		{"registrations, fires and stops are all reflected"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			m := clock.NewManualClock(testEpoch)
			if got := m.Pending(); got != 0 {
				t.Fatalf("fresh clock Pending() = %d, want 0", got)
			}
			//: an already-elapsed delay fires at registration and never arms.
			m.After(0)
			if got := m.Pending(); got != 0 {
				t.Errorf("Pending() after After(0) = %d, want 0", got)
			}
			tm := m.NewTimer(time.Second)
			tk := m.NewTicker(time.Second)
			if got := m.Pending(); got != 2 {
				t.Fatalf("Pending() = %d, want 2", got)
			}
			//: a fired one-shot disarms; a ticker survives its tick.
			m.Advance(time.Second)
			if got := m.Pending(); got != 1 {
				t.Errorf("Pending() after the timer fired = %d, want 1", got)
			}
			tk.Stop()
			if got := m.Pending(); got != 0 {
				t.Errorf("Pending() after Ticker.Stop = %d, want 0", got)
			}
			//: Reset re-arms a disarmed timer.
			tm.Reset(time.Second)
			if got := m.Pending(); got != 1 {
				t.Errorf("Pending() after Timer.Reset = %d, want 1", got)
			}
		})
	}
}

// TestManualSleepWakesOnAdvance is the demonstration that a ManualClock makes
// a wait testable without sleeping. Goroutine lifecycle: exactly one, started
// inside the subtest, parked in Sleep until the test advances the clock past
// its deadline, and terminated by publishing on the buffered woke channel —
// buffered so the goroutine can finish even if the test has already failed.
func TestManualSleepWakesOnAdvance(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		sleep time.Duration
	}{
		{"a sleeping goroutine wakes when the clock reaches its deadline", time.Hour},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			m := clock.NewManualClock(testEpoch)
			woke := make(chan time.Time, 1)
			go func() {
				m.Sleep(tc.sleep)
				woke <- m.Now()
			}()
			//: BlockUntil closes the race the manual clock otherwise has with
			//: the goroutine it drives: advancing before Sleep has registered
			//: would lose the wake entirely.
			m.BlockUntil(1)
			//: one nanosecond short must NOT wake it.
			m.Advance(tc.sleep - time.Nanosecond)
			select {
			case <-woke:
				t.Fatal("Sleep returned before its deadline")
			case <-time.After(50 * time.Millisecond):
			}
			m.Advance(time.Nanosecond)
			select {
			case at := <-woke:
				if want := testEpoch.Add(tc.sleep); !at.Equal(want) {
					t.Errorf("woke with Now() = %v, want %v", at, want)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("Sleep did not return after the clock passed its deadline")
			}
		})
	}
}

// TestManualBlockUntilWaitsForRegistrations pins the anti-race guarantee.
// Goroutine lifecycle: tc.count sleepers, started inside the subtest, all
// parked in Sleep until the single Advance releases them, and all joined
// through the WaitGroup before the subtest returns — the test leaks none.
func TestManualBlockUntilWaitsForRegistrations(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		count int
	}{
		{"three concurrent registrations", 3},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			m := clock.NewManualClock(testEpoch)
			var wg sync.WaitGroup
			for range tc.count {
				wg.Go(func() {
					m.Sleep(time.Hour)
				})
			}
			//: returns only once every goroutine has armed its wait.
			m.BlockUntil(tc.count)
			if got := m.Pending(); got < tc.count {
				t.Errorf("Pending() = %d after BlockUntil(%d)", got, tc.count)
			}
			//: release the sleepers and join them, so the test leaks nothing.
			m.Advance(time.Hour)
			wg.Wait()
		})
	}
}

// TestManualIsSafeUnderConcurrentUse backs the "safe for concurrent use"
// claim in ManualClock's doc comment. Goroutine lifecycle: three per worker
// slot — a reader, a registrant and an advancer — all started inside the
// subtest, all bounded by a fixed step count so each returns on its own, and
// all joined through the WaitGroup before the final assertion runs.
func TestManualIsSafeUnderConcurrentUse(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		goroutines int
		steps      int
	}{
		{"readers, registrants and advancers race for the same clock", 8, 64},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			m := clock.NewManualClock(testEpoch)
			var wg sync.WaitGroup
			//: three roles hammer the same lock; the race detector is the
			//: assertion here, alongside "no panic, no deadlock".
			for range tc.goroutines {
				wg.Go(func() {
					for range tc.steps {
						m.Now()
						m.Since(testEpoch)
						m.Pending()
					}
				})
				wg.Go(func() {
					for range tc.steps {
						tm := m.NewTimer(time.Minute)
						tm.Stop()
					}
				})
				wg.Go(func() {
					for range tc.steps {
						m.Advance(time.Millisecond)
					}
				})
			}
			wg.Wait()
			//: every advance landed, whatever the interleaving.
			want := testEpoch.Add(time.Duration(tc.goroutines*tc.steps) * time.Millisecond)
			if got := m.Now(); !got.Equal(want) {
				t.Errorf("Now() = %v, want %v", got, want)
			}
		})
	}
}

// TestManualSetFarPastATickerDeadlineReturns pins the rearm against the one
// input that defeats its single multiplication: a ticker left behind by close
// to the whole time.Duration range. time.Time.Sub saturates at ~292 years, so
// a Set three centuries past a deadline hands rearmWait an elapsed of
// math.MaxInt64, and period*(elapsed/period+1) wraps NEGATIVE — the deadline
// moves backwards, stays due, and advanceTo fires it again, forever, holding
// the clock's lock. The wait below runs on the wall clock because the defect
// is a loop that never yields: nothing else could bound it, and a regression
// must FAIL here rather than hang the binary until its own timeout. Nothing
// deferred touches the clock either, since a deferred Stop would queue behind
// the spinning lock and turn the failure back into a hang.
//
// Goroutine lifecycle: one per subtest, started here to run Set and closing
// done when it returns. On the failure path it never returns and outlives the
// subtest, spinning until the binary exits — that is the defect reported, not
// a leak of the test's making.
//
// Seen failing: with rearmWait restored to its unguarded multiplication, both
// cases printed
//
//	Set(+300y) past a 1ns ticker's deadline did not return within 10s: the
//	rearm overflowed, moved the deadline backwards, and advanceTo kept firing it
//	under the lock
//
// (and "… past a 1h0m0s ticker's deadline …" for the second).
func TestManualSetFarPastATickerDeadlineReturns(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		period time.Duration
	}{
		//: every instant is a tick, so the next one is target+1ns whatever
		//: the rearm does — the delivered instant is exact.
		{"a nanosecond ticker", time.Nanosecond},
		//: a period that does not divide the jump: only "within one period
		//: after target" is a property of the contract.
		{"an hour ticker", time.Hour},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			m := clock.NewManualClock(testEpoch)
			tk := m.NewTicker(tc.period)
			target := testEpoch.AddDate(300, 0, 0)
			done := make(chan struct{})
			go func() {
				defer close(done)
				m.Set(target)
			}()
			select {
			case <-done:
			case <-time.After(10 * time.Second):
				t.Fatalf("Set(+300y) past a %v ticker's deadline did not return within 10s: the rearm "+
					"overflowed, moved the deadline backwards, and advanceTo kept firing it under the lock", tc.period)
			}
			defer tk.Stop()
			if got := m.Now(); !got.Equal(target) {
				t.Fatalf("Now() after Set = %v, want %v", got, target)
			}
			//: the one tick the jump delivers carries the deadline it was due at.
			at, ticked := pending(tk.C())
			if !ticked {
				t.Fatal("the jump delivered no tick; the ticker was due")
			}
			if want := testEpoch.Add(tc.period); !at.Equal(want) {
				t.Errorf("delivered tick = %v, want the original deadline %v", at, want)
			}
			//: still armed, strictly after target and within one period of it.
			if got := m.Pending(); got != 1 {
				t.Fatalf("Pending() after the jump = %d, want 1 — a ticker survives its tick", got)
			}
			m.Advance(tc.period)
			next, again := pending(tk.C())
			if !again {
				t.Fatalf("no tick within one period (%v) after target", tc.period)
			}
			if !next.After(target) || next.After(target.Add(tc.period)) {
				t.Errorf("next tick = %v, want one in (%v, %v]", next, target, target.Add(tc.period))
			}
		})
	}
}
