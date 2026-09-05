// Package resilience — the circuit-breaker state machine.
package resilience

import (
	"context"
	"errors"
	"testing"
	"time"

	coreres "github.com/kitsunium/sdk/internal/core/resilience"
	kerrs "github.com/kitsunium/sdk/internal/kernel/errs"
)

// steppedClock is a settable clock so the cooldown can be crossed without
// sleeping through it.
type steppedClock struct{ now time.Time }

// Now returns the frozen instant.
func (c *steppedClock) Now() time.Time { return c.now }

// Since measures against the frozen instant.
func (c *steppedClock) Since(t time.Time) time.Duration { return c.now.Sub(t) }

// advance moves the frozen instant forward.
func (c *steppedClock) advance(d time.Duration) { c.now = c.now.Add(d) }

// Test_circuitBreaker_trip pins the transition into Open, including the
// timestamp. Without the stamp the cooldown would be measured from the zero
// time — an interval of decades — and the breaker would never re-probe.
func Test_circuitBreaker_trip(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		from breakerState
	}
	tests := []tc{
		{"from closed", stateClosed},
		{"from half-open", stateHalfOpen},
		{"from open", stateOpen},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		clk := &steppedClock{now: time.Unix(1_700_000_000, 0)}
		b := &circuitBreaker{clk: clk, state: c.from}

		b.trip()

		if b.state != stateOpen {
			t.Errorf("state = %v after trip, want open", b.state)
		}
		//: the cooldown is measured from this stamp; a zero one would make the
		//: breaker look permanently cooled down.
		if !b.openedAt.Equal(clk.now) {
			t.Errorf("openedAt = %v, want %v", b.openedAt, clk.now)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_circuitBreaker_allow pins the gate and the Open→HalfOpen transition.
//
// The half-open step is what lets a breaker ever close again: after the cooldown
// exactly ONE call is admitted as a probe. Admitting all of them would hammer a
// dependency that may still be down; admitting none would leave the breaker open
// forever.
func Test_circuitBreaker_allow(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		//: the state and how long ago the breaker opened.
		state   breakerState
		elapsed time.Duration
		openFor time.Duration
		want    bool
		//: the state the gate must leave behind.
		wantState breakerState
	}
	tests := []tc{
		{"closed admits", stateClosed, 0, time.Minute, true, stateClosed},
		{"half-open admits the probe", stateHalfOpen, 0, time.Minute, true, stateHalfOpen},
		{"open rejects within the cooldown", stateOpen, 30 * time.Second, time.Minute, false, stateOpen},
		{"open rejects at the very start", stateOpen, 0, time.Minute, false, stateOpen},
		{"open half-opens once the cooldown elapses", stateOpen, time.Minute, time.Minute, true, stateHalfOpen},
		{"open half-opens well past the cooldown", stateOpen, time.Hour, time.Minute, true, stateHalfOpen},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		clk := &steppedClock{now: time.Unix(1_700_000_000, 0)}
		b := &circuitBreaker{
			clk:      clk,
			state:    c.state,
			openFor:  c.openFor,
			openedAt: clk.now,
		}
		clk.advance(c.elapsed)

		if got := b.allow(); got != c.want {
			t.Errorf("allow() = %v, want %v", got, c.want)
		}
		if b.state != c.wantState {
			t.Errorf("state = %v after allow, want %v", b.state, c.wantState)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_circuitBreaker_record pins the counting rules.
//
// The threshold counts CONSECUTIVE failures, so any success resets it — a
// dependency that fails one call in ten is working, and a breaker that tripped
// on the tenth such failure would be measuring the wrong thing entirely.
//
// A failed probe in half-open re-opens immediately, without waiting for the
// threshold again: the probe exists precisely to answer "is it back yet", and
// one "no" is the whole answer.
func Test_circuitBreaker_record(t *testing.T) {
	t.Parallel()
	type tc struct {
		name      string
		state     breakerState
		failures  int
		threshold int
		success   bool
		wantState breakerState
		wantCount int
	}
	tests := []tc{
		{"a success closes and clears", stateClosed, 3, 5, true, stateClosed, 0},
		{"a success from half-open closes", stateHalfOpen, 0, 5, true, stateClosed, 0},
		{"a failure below the threshold counts", stateClosed, 1, 5, false, stateClosed, 2},
		{"the threshold trips", stateClosed, 4, 5, false, stateOpen, 5},
		{"past the threshold stays open", stateOpen, 5, 5, false, stateOpen, 6},
		//: one failed probe is the whole answer.
		{"a failed probe re-opens at once", stateHalfOpen, 0, 5, false, stateOpen, 0},
		{"a threshold of one trips immediately", stateClosed, 0, 1, false, stateOpen, 1},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		clk := &steppedClock{now: time.Unix(1_700_000_000, 0)}
		b := &circuitBreaker{
			clk:       clk,
			state:     c.state,
			failures:  c.failures,
			threshold: c.threshold,
			openFor:   time.Minute,
		}

		b.record(c.success)

		if b.state != c.wantState {
			t.Errorf("state = %v, want %v", b.state, c.wantState)
		}
		if b.failures != c.wantCount {
			t.Errorf("failures = %d, want %d", b.failures, c.wantCount)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_circuitBreaker_Run pins the end-to-end behaviour, including the one rule
// that keeps a breaker measuring what it claims to.
//
// A DETERMINISTIC error says nothing about the dependency's health — a 400 Bad
// Request means the caller sent something wrong, not that the service is down —
// so the classifier's rejects are passed through WITHOUT touching the state
// machine. A breaker that counted them would trip on a client-side bug and cut
// off a dependency that was working perfectly.
func Test_circuitBreaker_Run(t *testing.T) {
	t.Parallel()
	transient := errors.New("a transient failure")
	permanent := errors.New("a permanent failure")

	type tc struct {
		name string
		//: the errors each successive call returns; nil succeeds.
		outcomes  []error
		retryable func(error) bool
		threshold int
		//: the state the breaker must be in afterwards.
		wantState breakerState
		//: how many of the calls must actually reach the operation.
		wantRuns int
	}
	tests := []tc{
		{
			name:      "successes keep it closed",
			outcomes:  []error{nil, nil, nil},
			threshold: 2,
			wantState: stateClosed,
			wantRuns:  3,
		},
		{
			name:      "consecutive failures trip it",
			outcomes:  []error{transient, transient},
			threshold: 2,
			wantState: stateOpen,
			wantRuns:  2,
		},
		{
			//: the third call is rejected fast, so the operation never runs.
			name:      "an open breaker rejects without running the operation",
			outcomes:  []error{transient, transient, transient},
			threshold: 2,
			wantState: stateOpen,
			wantRuns:  2,
		},
		{
			name:      "a success resets the count",
			outcomes:  []error{transient, nil, transient},
			threshold: 2,
			wantState: stateClosed,
			wantRuns:  3,
		},
		{
			//: rejected by the classifier: never counted, never trips.
			name:      "deterministic failures never trip it",
			outcomes:  []error{permanent, permanent, permanent, permanent},
			retryable: func(err error) bool { return errors.Is(err, transient) },
			threshold: 2,
			wantState: stateClosed,
			wantRuns:  4,
		},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		clk := &steppedClock{now: time.Unix(1_700_000_000, 0)}
		b := &circuitBreaker{
			clk:       clk,
			retryable: c.retryable,
			threshold: c.threshold,
			openFor:   time.Minute,
		}

		runs := 0
		for i, want := range c.outcomes {
			err := b.Run(t.Context(), func(context.Context) error {
				runs++
				return want
			})
			//: an open breaker's rejection is typed so a caller can shed load.
			if kerrs.HasCode(err, coreres.CodeCircuitOpen) {
				continue
			}
			//: otherwise the operation's own outcome passes through untouched.
			if !errors.Is(err, want) {
				t.Errorf("call %d = %v, want %v", i, err, want)
			}
		}

		if runs != c.wantRuns {
			t.Errorf("the operation ran %d times, want %d", runs, c.wantRuns)
		}
		b.mu.Lock()
		state := b.state
		b.mu.Unlock()
		if state != c.wantState {
			t.Errorf("state = %v, want %v", state, c.wantState)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_circuitBreaker_RunRecovers pins the whole cycle: trip, reject through the
// cooldown, admit one probe, and close on its success. That last step is what
// makes a breaker a breaker rather than a fuse.
func Test_circuitBreaker_RunRecovers(t *testing.T) {
	t.Parallel()
	failure := errors.New("down")

	type tc struct {
		name string
		//: whether the probe after the cooldown succeeds.
		probeSucceeds bool
		wantState     breakerState
	}
	tests := []tc{
		{"a successful probe closes it", true, stateClosed},
		{"a failed probe re-opens it", false, stateOpen},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		clk := &steppedClock{now: time.Unix(1_700_000_000, 0)}
		b := &circuitBreaker{clk: clk, threshold: 2, openFor: time.Minute}

		//: trip it. The failures are the point; their errors are the
		//: operation's own and are asserted by the state below.
		for range 2 {
			if err := b.Run(t.Context(), func(context.Context) error { return failure }); err == nil {
				t.Fatal("a failing operation reported success")
			}
		}
		//: within the cooldown every call is rejected without running.
		ran := false
		err := b.Run(t.Context(), func(context.Context) error {
			ran = true
			return nil
		})
		if !kerrs.HasCode(err, coreres.CodeCircuitOpen) {
			t.Fatalf("a call within the cooldown = %v, want CIRCUIT_OPEN", err)
		}
		if ran {
			t.Fatal("an open breaker still ran the operation")
		}

		//: cross the cooldown; exactly one probe is admitted.
		clk.advance(time.Minute)
		probeRan := false
		probeErr := b.Run(t.Context(), func(context.Context) error {
			probeRan = true
			if c.probeSucceeds {
				return nil
			}
			return failure
		})
		//: the probe's own outcome passes through; the state below is what the
		//: breaker made of it.
		if c.probeSucceeds && probeErr != nil {
			t.Fatalf("the probe = %v, want nil", probeErr)
		}
		if !probeRan {
			t.Fatal("the probe was not admitted after the cooldown")
		}

		b.mu.Lock()
		state := b.state
		b.mu.Unlock()
		if state != c.wantState {
			t.Errorf("state = %v after the probe, want %v", state, c.wantState)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}
