// Package resilience_test — what a circuit breaker reports, what an operator
// forces on it, and the changes of phase it delivers (#480).
package resilience_test

import (
	"context"
	"sync"
	"testing"
	"time"

	coreres "github.com/kitsunium/sdk/internal/core/app/resilience"
	"github.com/kitsunium/sdk/internal/kernel/clock"
	svcres "github.com/kitsunium/sdk/internal/service/app/resilience"
)

// change is one call OnStateChange received.
type change struct{ from, to coreres.BreakerPhase }

// changeLog records what OnStateChange hears, safe for concurrent use.
type changeLog struct {
	mu   sync.Mutex
	seen []change
}

// hear is an OnStateChange.
func (l *changeLog) hear(from, to coreres.BreakerPhase) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.seen = append(l.seen, change{from, to})
}

// all returns a copy of what was heard.
func (l *changeLog) all() []change {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]change(nil), l.seen...)
}

// sameChanges reports whether two sequences of changes are equal.
func sameChanges(got, want []change) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

// TestBreakerPhaseString pins the stable spellings, and that the zero value
// is none of the three.
func TestBreakerPhaseString(t *testing.T) {
	t.Parallel()
	for phase, want := range map[coreres.BreakerPhase]string{
		coreres.BreakerClosed:   "closed",
		coreres.BreakerOpen:     "open",
		coreres.BreakerHalfOpen: "half-open",
		0:                       "unknown",
		42:                      "unknown",
	} {
		if got := phase.String(); got != want {
			t.Errorf("BreakerPhase(%d).String() = %q, want %q", phase, got, want)
		}
	}
}

// TestBreakerStateFollowsTheMachine pins State through a whole cycle of the
// consecutive breaker: its count, its trips, and when the cooldown ends.
func TestBreakerStateFollowsTheMachine(t *testing.T) {
	t.Parallel()
	clk := clock.NewManualClock(epoch)
	b := svcres.NewCircuitBreaker(svcres.BreakerConfig{FailureThreshold: 2, OpenDuration: time.Minute, Clock: clk})
	state := stateOf(t, b)
	if got := state.State(); got != (coreres.BreakerStateValue{Phase: coreres.BreakerClosed}) {
		t.Fatalf("a fresh breaker: %+v", got)
	}
	_ = b.Run(t.Context(), fail)
	if got := state.State(); got.Failures != 1 || got.Phase != coreres.BreakerClosed {
		t.Fatalf("after one failure: %+v", got)
	}
	_ = b.Run(t.Context(), fail)
	want := coreres.BreakerStateValue{Phase: coreres.BreakerOpen, Failures: 2, Trips: 1, OpenUntil: epoch.Add(time.Minute)}
	if got := state.State(); got != want {
		t.Fatalf("after the trip: %+v, want %+v", got, want)
	}
	//: State changes nothing: past the cooldown it still reads Open.
	clk.Advance(time.Minute)
	if got := state.State(); got.Phase != coreres.BreakerOpen {
		t.Fatalf("past the cooldown, before any call: %+v, want still open", got)
	}
	_ = b.Run(t.Context(), succeed)
	if got := state.State(); got != (coreres.BreakerStateValue{Phase: coreres.BreakerClosed, Trips: 1}) {
		t.Fatalf("after a successful trial: %+v", got)
	}
}

// TestBreakerIsolateHoldsUntilReset pins the manual control: an isolated
// breaker rejects past any cooldown and admits no trial, until Reset closes it
// and clears its counts.
func TestBreakerIsolateHoldsUntilReset(t *testing.T) {
	t.Parallel()
	clk := clock.NewManualClock(epoch)
	b := svcres.NewCircuitBreaker(svcres.BreakerConfig{FailureThreshold: 3, OpenDuration: time.Second, Clock: clk})
	state := stateOf(t, b)
	_ = b.Run(t.Context(), fail)
	state.Isolate()
	clk.Advance(time.Hour)
	if !rejected(b) {
		t.Fatal("an isolated breaker admitted a call an hour later")
	}
	want := coreres.BreakerStateValue{Phase: coreres.BreakerOpen, Isolated: true, Failures: 1}
	if got := state.State(); got != want {
		t.Fatalf("isolated: %+v, want %+v", got, want)
	}
	state.Reset()
	if got := state.State(); got != (coreres.BreakerStateValue{Phase: coreres.BreakerClosed}) {
		t.Fatalf("after Reset: %+v, want closed and cleared", got)
	}
	if rejected(b) {
		t.Fatal("a reset breaker rejected a call")
	}
}

// TestBreakerIsolateAndResetIgnoreCallsInFlight pins the generation rule for
// the two controls: a call admitted before either describes the dependency
// before the operator acted.
func TestBreakerIsolateAndResetIgnoreCallsInFlight(t *testing.T) {
	t.Parallel()
	clk := clock.NewManualClock(epoch)
	b := svcres.NewCircuitBreaker(svcres.BreakerConfig{FailureThreshold: 1, Clock: clk})
	state := stateOf(t, b)

	//: a success admitted before Isolate does not close the isolated breaker.
	before := hold(t, b, nil)
	state.Isolate()
	_ = before.release()
	if got := state.State(); got.Phase != coreres.BreakerOpen || !got.Isolated {
		t.Fatalf("after a straggler's success: %+v, want still isolated", got)
	}

	//: a failure admitted before Reset does not trip the reset breaker.
	state.Reset()
	late := hold(t, b, errDown)
	state.Reset()
	_ = late.release()
	if got := state.State(); got.Phase != coreres.BreakerClosed || got.Failures != 0 {
		t.Fatalf("after a straggler's failure: %+v, want closed", got)
	}
}

// TestBreakerOnStateChangeHearsEveryChangeInOrder pins the listener on one
// goroutine: each change of phase once, in order, Isolate and Reset included,
// and a Reset of a closed breaker changing nothing.
func TestBreakerOnStateChangeHearsEveryChangeInOrder(t *testing.T) {
	t.Parallel()
	clk := clock.NewManualClock(epoch)
	var log changeLog
	b := svcres.NewCircuitBreaker(svcres.BreakerConfig{
		FailureThreshold: 1, OpenDuration: time.Minute, Clock: clk, OnStateChange: log.hear,
	})
	state := stateOf(t, b)
	_ = b.Run(t.Context(), fail)    // Closed → Open
	clk.Advance(time.Minute)        //
	_ = b.Run(t.Context(), fail)    // Open → HalfOpen → Open
	clk.Advance(time.Minute)        //
	_ = b.Run(t.Context(), succeed) // Open → HalfOpen → Closed
	state.Reset()                   // nothing: already closed
	state.Isolate()                 // Closed → Open
	state.Isolate()                 // nothing: already open
	state.Reset()                   // Open → Closed
	closed, open, half := coreres.BreakerClosed, coreres.BreakerOpen, coreres.BreakerHalfOpen
	want := []change{
		{closed, open},
		{open, half},
		{half, open},
		{open, half},
		{half, closed},
		{closed, open},
		{open, closed},
	}
	if got := log.all(); !sameChanges(got, want) {
		t.Fatalf("heard %v, want %v", got, want)
	}
}

// TestBreakerOnStateChangeRunsOutsideTheLock pins that the listener may call
// the breaker back — State, and even Reset — without deadlocking, and that a
// change it causes is delivered after the one it is hearing.
func TestBreakerOnStateChangeRunsOutsideTheLock(t *testing.T) {
	t.Parallel()
	var (
		log   changeLog
		state coreres.BreakerState
		seen  []coreres.BreakerPhase
	)
	b := svcres.NewCircuitBreaker(svcres.BreakerConfig{
		FailureThreshold: 1, Clock: clock.NewManualClock(epoch),
		OnStateChange: func(from, to coreres.BreakerPhase) {
			log.hear(from, to)
			seen = append(seen, state.State().Phase)
			//: an operator's reflex: close it again at once.
			if to == coreres.BreakerOpen {
				state.Reset()
			}
		},
	})
	state = stateOf(t, b)
	_ = b.Run(t.Context(), fail)
	want := []change{{coreres.BreakerClosed, coreres.BreakerOpen}, {coreres.BreakerOpen, coreres.BreakerClosed}}
	if got := log.all(); !sameChanges(got, want) {
		t.Fatalf("heard %v, want %v", got, want)
	}
	//: the listener read State without deadlocking; by the time it heard the
	//: second change, the Reset it made had been applied.
	if len(seen) != 2 || seen[1] != coreres.BreakerClosed {
		t.Fatalf("State read from the listener: %v", seen)
	}
}

// TestBreakerOnStateChangeSurvivesAPanickingListener pins that a listener
// which panics loses the change it panicked on, and nothing after it.
func TestBreakerOnStateChangeSurvivesAPanickingListener(t *testing.T) {
	t.Parallel()
	clk := clock.NewManualClock(epoch)
	var log changeLog
	b := svcres.NewCircuitBreaker(svcres.BreakerConfig{
		FailureThreshold: 1, OpenDuration: time.Minute, Clock: clk,
		OnStateChange: func(from, to coreres.BreakerPhase) {
			log.hear(from, to)
			//: the first change only.
			if len(log.all()) == 1 {
				panic("listener")
			}
		},
	})
	func() {
		defer func() {
			if recover() == nil {
				t.Error("the listener's panic did not reach the caller")
			}
		}()
		_ = b.Run(t.Context(), fail)
	}()
	stateOf(t, b).Reset()
	want := []change{{coreres.BreakerClosed, coreres.BreakerOpen}, {coreres.BreakerOpen, coreres.BreakerClosed}}
	if got := log.all(); !sameChanges(got, want) {
		t.Fatalf("heard %v, want %v", got, want)
	}
}

// TestBreakerOnStateChangeUnderConcurrentCalls pins the delivery under load:
// whatever the interleaving, the changes heard form one chain — each starts
// where the previous ended — and end where the breaker stands.
func TestBreakerOnStateChangeUnderConcurrentCalls(t *testing.T) {
	t.Parallel()
	var log changeLog
	b := svcres.NewCircuitBreaker(svcres.BreakerConfig{
		Clock: clock.NewManualClock(epoch), OnStateChange: log.hear,
		Window: &svcres.BreakerWindowValue{MinCalls: 2, FailureRatio: 0.5},
	})
	state := stateOf(t, b)
	var wg sync.WaitGroup
	for g := range 8 {
		wg.Go(func() {
			for i := range 50 {
				switch (g + i) % 5 {
				case 0:
					state.Reset()
				case 1:
					_ = b.Run(context.Background(), fail)
				default:
					_ = b.Run(context.Background(), succeed)
				}
			}
		})
	}
	wg.Wait()
	got := log.all()
	at := coreres.BreakerClosed
	for i, c := range got {
		if c.from != at || c.from == c.to {
			t.Fatalf("change %d is %v, after the breaker reached %v: %v", i, c, at, got)
		}
		at = c.to
	}
	if now := state.State().Phase; now != at {
		t.Fatalf("the last change heard ends at %v, the breaker stands at %v", at, now)
	}
}

// TestBreakerOpenBackoffGrowsWhileTrialsFail pins OpenBackoff: each opening
// since the breaker last closed lasts the curve's next delay, held at its
// ceiling, and a trial that closes the breaker starts it over.
func TestBreakerOpenBackoffGrowsWhileTrialsFail(t *testing.T) {
	t.Parallel()
	clk := clock.NewManualClock(epoch)
	b := svcres.NewCircuitBreaker(svcres.BreakerConfig{
		FailureThreshold: 1, Clock: clk,
		OpenBackoff: &svcres.BackoffValue{BaseDelay: time.Second, MaxDelay: 4 * time.Second, Multiplier: 2},
	})
	state := stateOf(t, b)
	_ = b.Run(t.Context(), fail)
	for i, want := range []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 4 * time.Second} {
		if got := state.State().OpenUntil.Sub(clk.Now()); got != want {
			t.Fatalf("opening %d lasts %v, want %v", i+1, got, want)
		}
		clk.Advance(want - time.Nanosecond)
		if !rejected(b) {
			t.Fatalf("opening %d admitted a call a tick early", i+1)
		}
		clk.Advance(time.Nanosecond)
		_ = b.Run(t.Context(), fail) // the trial fails: open again, longer
	}
	clk.Advance(4 * time.Second)
	_ = b.Run(t.Context(), succeed)
	_ = b.Run(t.Context(), fail)
	if got := state.State().OpenUntil.Sub(clk.Now()); got != time.Second {
		t.Fatalf("the first opening after a close lasts %v, want the base again", got)
	}
}
