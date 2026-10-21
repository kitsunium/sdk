// Package resilience — the circuit-breaker state machine.
package resilience

import (
	"context"
	"errors"
	"testing"
	"time"

	coreres "github.com/kitsunium/sdk/internal/core/app/resilience"
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
// exactly ONE call is admitted as a probe, and every other call is rejected
// while it runs. Admitting all of them would hammer a dependency that may still
// be down; admitting none would leave the breaker open forever.
func Test_circuitBreaker_allow(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		//: the state and how long ago the breaker opened.
		state   breakerState
		probing bool
		elapsed time.Duration
		openFor time.Duration
		want    bool
		//: whether the admitted call is the HalfOpen trial.
		wantTrial bool
		//: the state the gate must leave behind.
		wantState   breakerState
		wantProbing bool
	}
	tests := []tc{
		{"closed admits", stateClosed, false, 0, time.Minute, true, false, stateClosed, false},
		{"half-open admits the probe", stateHalfOpen, false, 0, time.Minute, true, true, stateHalfOpen, true},
		{"half-open rejects while its probe runs", stateHalfOpen, true, 0, time.Minute, false, false, stateHalfOpen, true},
		{"open rejects within the cooldown", stateOpen, false, 30 * time.Second, time.Minute, false, false, stateOpen, false},
		{"open rejects at the very start", stateOpen, false, 0, time.Minute, false, false, stateOpen, false},
		{"open rejects one tick before the cooldown", stateOpen, false, time.Minute - time.Nanosecond, time.Minute, false, false, stateOpen, false},
		{"open half-opens once the cooldown elapses", stateOpen, false, time.Minute, time.Minute, true, true, stateHalfOpen, true},
		{"open half-opens well past the cooldown", stateOpen, false, time.Hour, time.Minute, true, true, stateHalfOpen, true},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		clk := &steppedClock{now: time.Unix(1_700_000_000, 0)}
		b := &circuitBreaker{
			clk:        clk,
			state:      c.state,
			probing:    c.probing,
			openFor:    c.openFor,
			openedAt:   clk.now,
			generation: 3,
		}
		clk.advance(c.elapsed)

		adm, got := b.allow()
		if got != c.want {
			t.Errorf("allow() = %v, want %v", got, c.want)
		}
		if adm.trial != c.wantTrial {
			t.Errorf("trial = %v, want %v", adm.trial, c.wantTrial)
		}
		//: an admitted call carries the generation it was admitted in.
		if got && adm.generation != 3 {
			t.Errorf("generation = %d, want 3", adm.generation)
		}
		if b.state != c.wantState {
			t.Errorf("state = %v after allow, want %v", b.state, c.wantState)
		}
		if b.probing != c.wantProbing {
			t.Errorf("probing = %v after allow, want %v", b.probing, c.wantProbing)
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
//
// An outcome from an earlier generation — a call admitted before the breaker
// last tripped — is ignored: its success must not close an Open breaker and its
// failure must not restart the cooldown (#467).
func Test_circuitBreaker_record(t *testing.T) {
	t.Parallel()
	const current uint64 = 2
	type tc struct {
		name      string
		state     breakerState
		failures  int
		threshold int
		adm       admission
		res       outcome
		wantState breakerState
		wantCount int
		//: whether the cooldown restarted (openedAt moved to now).
		wantRetrip bool
	}
	closed := admission{generation: current}
	trial := admission{generation: current, trial: true}
	stale := admission{generation: current - 1}
	tests := []tc{
		{"a success closes and clears", stateClosed, 3, 5, closed, outcomeSuccess, stateClosed, 0, false},
		{"a success from the half-open trial closes", stateHalfOpen, 0, 5, trial, outcomeSuccess, stateClosed, 0, false},
		{"a failure below the threshold counts", stateClosed, 1, 5, closed, outcomeFailure, stateClosed, 2, false},
		{"the threshold trips", stateClosed, 4, 5, closed, outcomeFailure, stateOpen, 5, true},
		{"a failure while open of the same generation changes nothing", stateOpen, 5, 5, closed, outcomeFailure, stateOpen, 5, false},
		{"a success while open never closes", stateOpen, 5, 5, closed, outcomeSuccess, stateOpen, 5, false},
		//: one failed probe is the whole answer.
		{"a failed probe re-opens at once", stateHalfOpen, 0, 5, trial, outcomeFailure, stateOpen, 0, true},
		{"a threshold of one trips immediately", stateClosed, 0, 1, closed, outcomeFailure, stateOpen, 1, true},
		{"a neutral outcome changes nothing", stateClosed, 3, 5, closed, outcomeNeutral, stateClosed, 3, false},
		{"a neutral trial leaves it half-open", stateHalfOpen, 0, 5, trial, outcomeNeutral, stateHalfOpen, 0, false},
		//: stragglers, admitted before the last trip.
		{"a straggler's success leaves it open", stateOpen, 1, 1, stale, outcomeSuccess, stateOpen, 1, false},
		{"a straggler's failure does not restart the cooldown", stateOpen, 1, 1, stale, outcomeFailure, stateOpen, 1, false},
		{"a straggler's success does not close the half-open breaker", stateHalfOpen, 0, 1, stale, outcomeSuccess, stateHalfOpen, 0, false},
		{"a straggler's failure does not re-open the half-open breaker", stateHalfOpen, 0, 1, stale, outcomeFailure, stateHalfOpen, 0, false},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		clk := &steppedClock{now: time.Unix(1_700_000_000, 0)}
		opened := clk.now
		clk.advance(time.Second)
		b := &circuitBreaker{
			clk:        clk,
			state:      c.state,
			failures:   c.failures,
			threshold:  c.threshold,
			cooldown:   time.Minute,
			openFor:    time.Minute,
			openedAt:   opened,
			generation: current,
			probing:    c.state == stateHalfOpen,
		}

		b.record(c.adm, c.res, clk.now, clk.now)

		if b.state != c.wantState {
			t.Errorf("state = %v, want %v", b.state, c.wantState)
		}
		if b.failures != c.wantCount {
			t.Errorf("failures = %d, want %d", b.failures, c.wantCount)
		}
		if retripped := !b.openedAt.Equal(opened); retripped != c.wantRetrip {
			t.Errorf("cooldown restarted = %v, want %v", retripped, c.wantRetrip)
		}
		//: the trial's end always frees the half-open slot; a straggler holds none.
		if c.adm.trial && b.probing {
			t.Error("the trial's slot was not freed")
		}
		if !c.adm.trial && c.wantState == stateHalfOpen && !b.probing {
			t.Error("a straggler freed a slot it never held")
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
			cooldown:  time.Minute,
			openFor:   time.Minute,
			state:     stateClosed,
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
		b := &circuitBreaker{clk: clk, threshold: 2, cooldown: time.Minute, openFor: time.Minute, state: stateClosed}

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
