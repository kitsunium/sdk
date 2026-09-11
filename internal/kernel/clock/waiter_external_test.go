package clock_test

import (
	"testing"
	"time"

	"github.com/kitsunium/sdk/internal/kernel/clock"
)

// testEpoch is an arbitrary, deliberately non-round origin for the manual
// clocks built below — it is neither the Unix epoch nor synctest's 2000-01-01,
// so a test that accidentally reads a real or bubbled clock fails loudly.
var testEpoch = time.Date(2031, 3, 7, 4, 5, 6, 789, time.UTC)

// recvWithin receives from ch, giving up after budget. The budget is the
// TEST's own wall-clock bound, never the clock under test: on a ManualClock
// every delivery is already in the channel by the time Advance returns, and on
// System the durations under test are non-positive, so a timeout here always
// means a real failure rather than a slow machine.
func recvWithin(t *testing.T, ch <-chan time.Time, budget time.Duration) (time.Time, bool) {
	t.Helper()
	select {
	case v := <-ch:
		return v, true
	case <-time.After(budget):
		return time.Time{}, false
	}
}

// timedImpl names one implementation of the full time port.
type timedImpl struct {
	name string
	clk  clock.Timed
}

// timedClocks returns the two Timed implementations under one table, so every
// contract below is asserted against BOTH. That is the point of the pair: a
// ManualClock that refuses different inputs than System is not a test double,
// it is a second implementation with its own bugs.
func timedClocks() []timedImpl {
	return []timedImpl{
		{"System", clock.System},
		{"ManualClock", clock.NewManualClock(testEpoch)},
	}
}

func TestAfterNonPositiveFiresImmediately(t *testing.T) {
	t.Parallel()
	for _, impl := range timedClocks() {
		for _, d := range []time.Duration{0, -time.Nanosecond, -time.Hour} {
			t.Run(impl.name+"/"+d.String(), func(t *testing.T) {
				t.Parallel()
				//: a non-positive delay is already elapsed — the value must be
				//: there without anyone advancing or sleeping.
				if _, ok := recvWithin(t, impl.clk.After(d), 2*time.Second); !ok {
					t.Errorf("After(%v) did not deliver immediately", d)
				}
			})
		}
	}
}

func TestNewTimerNonPositiveFiresImmediately(t *testing.T) {
	t.Parallel()
	for _, impl := range timedClocks() {
		for _, d := range []time.Duration{0, -time.Millisecond} {
			t.Run(impl.name+"/"+d.String(), func(t *testing.T) {
				t.Parallel()
				tm := impl.clk.NewTimer(d)
				if _, ok := recvWithin(t, tm.C(), 2*time.Second); !ok {
					t.Errorf("NewTimer(%v) did not fire immediately", d)
				}
			})
		}
	}
}

// TestSleepNonPositiveReturnsImmediately runs each Sleep on its own goroutine
// so a Sleep that wrongly blocks fails the assertion instead of hanging the
// suite. Goroutine lifecycle: one per subtest, started inside the subtest,
// terminated by Sleep returning and signalled by closing done. Should Sleep
// block forever the goroutine outlives the subtest — that is the failure the
// test reports, and the binary's own timeout bounds it.
func TestSleepNonPositiveReturnsImmediately(t *testing.T) {
	t.Parallel()
	for _, impl := range timedClocks() {
		for _, d := range []time.Duration{0, -time.Second} {
			t.Run(impl.name+"/"+d.String(), func(t *testing.T) {
				t.Parallel()
				done := make(chan struct{})
				go func() {
					defer close(done)
					impl.clk.Sleep(d)
				}()
				select {
				case <-done:
				case <-time.After(2 * time.Second):
					t.Errorf("Sleep(%v) blocked; a non-positive sleep must return at once", d)
				}
			})
		}
	}
}

func TestNewTickerNonPositivePanics(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		d    time.Duration
		want string
	}{
		{"zero", 0, "clock: NewTicker requires a period > 0, got 0s"},
		{"negative", -time.Second, "clock: NewTicker requires a period > 0, got -1s"},
	}
	for _, impl := range timedClocks() {
		for _, tc := range tests {
			t.Run(impl.name+"/"+tc.name, func(t *testing.T) {
				t.Parallel()
				got := recoverPanic(t, func() { impl.clk.NewTicker(tc.d) })
				//: the message is asserted verbatim, and asserted for BOTH
				//: implementations, so the refusal cannot drift apart.
				if got != tc.want {
					t.Errorf("panic = %q, want %q", got, tc.want)
				}
			})
		}
	}
}

func TestTickerResetNonPositivePanics(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		d    time.Duration
		want string
	}{
		{"zero", 0, "clock: Ticker.Reset requires a period > 0, got 0s"},
		{"negative", -time.Minute, "clock: Ticker.Reset requires a period > 0, got -1m0s"},
	}
	for _, impl := range timedClocks() {
		for _, tc := range tests {
			t.Run(impl.name+"/"+tc.name, func(t *testing.T) {
				t.Parallel()
				//: an hour-long period never fires during the test, on either clock.
				tk := impl.clk.NewTicker(time.Hour)
				defer tk.Stop()
				got := recoverPanic(t, func() { tk.Reset(tc.d) })
				if got != tc.want {
					t.Errorf("panic = %q, want %q", got, tc.want)
				}
			})
		}
	}
}

func TestTickerResetChangesThePeriod(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		period time.Duration
	}{
		{"a stalled ticker resumes on the new period", time.Millisecond},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			//: System is exercised with a real (tiny) period; ManualClock with
			//: an hour, since only Advance can make its time pass.
			sys := clock.System.NewTicker(time.Hour)
			defer sys.Stop()
			sys.Reset(tc.period)
			if _, ok := recvWithin(t, sys.C(), 5*time.Second); !ok {
				t.Error("System ticker delivered no tick after Reset")
			}

			m := clock.NewManualClock(testEpoch)
			man := m.NewTicker(time.Hour)
			defer man.Stop()
			man.Reset(time.Minute)
			//: the old hour-long deadline is gone; the new minute one is due.
			m.Advance(time.Minute)
			at, ok := pending(man.C())
			if !ok {
				t.Fatal("ManualClock ticker delivered no tick after Reset")
			}
			if want := testEpoch.Add(time.Minute); !at.Equal(want) {
				t.Errorf("tick = %v, want %v", at, want)
			}
		})
	}
}

func TestManualTimerResetNonPositiveFiresImmediately(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		reset time.Duration
	}{
		{"reset to zero", 0},
		{"reset to a negative delay", -time.Hour},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			m := clock.NewManualClock(testEpoch)
			tm := m.NewTimer(time.Hour)
			//: a non-positive reset is already elapsed on the manual clock too,
			//: so the value is there before Reset returns — no Advance needed.
			if got := tm.Reset(tc.reset); !got {
				t.Error("Reset() on an armed timer = false, want true")
			}
			at, fired := pending(tm.C())
			if !fired {
				t.Fatalf("Reset(%v) did not fire immediately", tc.reset)
			}
			if !at.Equal(testEpoch) {
				t.Errorf("fired at %v, want the clock's current instant %v", at, testEpoch)
			}
			//: and it is disarmed, exactly like any fired one-shot.
			if got := m.Pending(); got != 0 {
				t.Errorf("Pending() = %d after an immediate Reset, want 0", got)
			}
		})
	}
}

// recoverPanic runs fn and returns the recovered panic value as a string,
// failing the test when fn did not panic at all.
func recoverPanic(t *testing.T, fn func()) (msg string) {
	t.Helper()
	defer func() {
		r := recover()
		if r == nil {
			t.Error("expected a panic, got none")
			return
		}
		s, ok := r.(string)
		if !ok {
			t.Errorf("panic value %v is not a string", r)
			return
		}
		msg = s
	}()
	fn()
	return ""
}

func TestSystemTimerFiresAndStops(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name          string
		arm           time.Duration
		wantFirstStop bool
	}{
		{"an hour-long timer is still armed at the first Stop", time.Hour, true},
		{"a minute-long timer is still armed at the first Stop", time.Minute, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			tm := clock.System.NewTimer(tc.arm)
			if got := tm.Stop(); got != tc.wantFirstStop {
				t.Errorf("Stop() = %v, want %v", got, tc.wantFirstStop)
			}
			//: a second Stop must report "was not armed".
			if got := tm.Stop(); got {
				t.Error("second Stop() = true, want false")
			}
			//: Reset re-arms a stopped timer and reports it was not armed.
			if got := tm.Reset(tc.arm); got {
				t.Error("Reset() on a stopped timer = true, want false")
			}
			tm.Stop()
		})
	}
}

func TestSystemTickerDelivers(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		period time.Duration
	}{
		{"a millisecond ticker delivers a tick", time.Millisecond},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			tk := clock.System.NewTicker(tc.period)
			defer tk.Stop()
			//: a generous budget: this asserts delivery happens, not when.
			if _, ok := recvWithin(t, tk.C(), 5*time.Second); !ok {
				t.Errorf("ticker of period %v delivered no tick", tc.period)
			}
		})
	}
}

// TestSystemTickerLeavesNoStaleTickAcrossStopAndReset pins, on the real
// ticker, the premise TestManualTickerStopAndResetLeaveNoStaleTick holds the
// double to. Since Go 1.23 a time.Ticker's channel is synchronous: a tick that
// fell due while nobody was receiving is not buffered, and after Stop or Reset
// returns it is never received. The sleep only makes a tick fall due; the
// assertion does not depend on how many did, so it cannot flake on a slow
// machine — at worst it stops exercising the stale case.
//
// Seen failing: with systemTicker.Stop made a no-op, the Stop case printed
// "a tick was receivable after Stop", because the ticker kept ticking.
func TestSystemTickerLeavesNoStaleTickAcrossStopAndReset(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		after func(clock.Ticker)
	}{
		{"Stop", func(tk clock.Ticker) { tk.Stop() }},
		{"Reset", func(tk clock.Ticker) { tk.Reset(time.Hour) }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			tk := clock.System.NewTicker(time.Millisecond)
			defer tk.Stop()
			//: twenty periods with nobody receiving.
			time.Sleep(20 * time.Millisecond)
			tc.after(tk)
			//: a moment for a tick that should not exist to show up anyway.
			time.Sleep(5 * time.Millisecond)
			if _, stale := pending(tk.C()); stale {
				t.Fatalf("a tick was receivable after %s", tc.name)
			}
		})
	}
}

// twoMethodDouble is the shape every downstream hand-written clock double has:
// Now and Since, nothing else. pkg/v1/cache.Config is a type alias whose Clock
// field carries clock.Clock, so this exact shape is compilable by consumers of
// the published module.
type twoMethodDouble struct{ at time.Time }

func (d twoMethodDouble) Now() time.Time                  { return d.at }
func (d twoMethodDouble) Since(t time.Time) time.Duration { return d.at.Sub(t) }

func TestTwoMethodDoubleStillSatisfiesClock(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		at   time.Time
	}{
		{"a bare Now/Since double is still a Clock", testEpoch},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			//: THIS is the compatibility gate. Adding a method to Clock breaks
			//: this line, and it breaks it in exactly the way every downstream
			//: double would break — which is why the waiting half lives on a
			//: separate interface instead.
			var c clock.Clock = twoMethodDouble{at: tc.at}
			if got := c.Now(); !got.Equal(tc.at) {
				t.Errorf("Now() = %v, want %v", got, tc.at)
			}
			//: System must remain assignable to the narrow half as well.
			var s clock.Clock = clock.System
			if s.Now().IsZero() {
				t.Error("clock.System.Now() through the narrow Clock returned zero")
			}
		})
	}
}
