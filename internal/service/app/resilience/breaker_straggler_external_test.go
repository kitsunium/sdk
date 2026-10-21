// Package resilience_test — the circuit breaker under calls that outlive a trip.
package resilience_test

import (
	"context"
	"errors"
	"testing"
	"time"

	coreres "github.com/kitsunium/sdk/internal/core/app/resilience"
	"github.com/kitsunium/sdk/internal/kernel/clock"
	kerrs "github.com/kitsunium/sdk/internal/kernel/errs"
	svcres "github.com/kitsunium/sdk/internal/service/app/resilience"
)

// held is a call parked inside a breaker's Run: started is closed once the
// operation runs, the operation returns result once finish is closed, and done
// carries Run's error.
type held struct {
	started chan struct{}
	finish  chan struct{}
	done    chan error
}

// hold starts a call on b that blocks inside its operation until release, then
// returns result. It returns once the operation is running, so the call was
// admitted in the state the breaker was in at that point.
func hold(t *testing.T, b coreres.Runner, result error) *held {
	t.Helper()
	h := &held{started: make(chan struct{}), finish: make(chan struct{}), done: make(chan error, 1)}
	go func() {
		h.done <- b.Run(context.Background(), func(context.Context) error {
			close(h.started)
			<-h.finish
			return result
		})
	}()
	<-h.started
	return h
}

// release lets the held call return, and waits for Run to finish.
func (h *held) release() error {
	close(h.finish)
	return <-h.done
}

// probe runs one call that succeeds and reports whether it ran and what Run said.
func probe(b coreres.Runner) (bool, error) {
	ran := false
	err := b.Run(context.Background(), func(context.Context) error {
		ran = true
		return nil
	})
	return ran, err
}

// TestCircuitBreaker_StragglerSuccessKeepsItOpen pins #467's first scenario. A
// slow call admitted while the breaker was Closed, which succeeds after another
// call tripped it, describes the dependency before the trip: it must not close
// the breaker inside OpenDuration, with no HalfOpen trial.
func TestCircuitBreaker_StragglerSuccessKeepsItOpen(t *testing.T) {
	t.Parallel()
	clk := clock.NewManualClock(time.Unix(1_700_000_000, 0))
	b := svcres.NewCircuitBreaker(svcres.BreakerConfig{FailureThreshold: 1, OpenDuration: time.Hour, Clock: clk})

	slow := hold(t, b, nil)
	if err := b.Run(t.Context(), func(context.Context) error { return errors.New("down") }); err == nil {
		t.Fatal("a failing operation reported success")
	}
	if ran, err := probe(b); ran || !kerrs.HasCode(err, coreres.CodeCircuitOpen) {
		t.Fatalf("right after the trip: ran=%v err=%v, want CIRCUIT_OPEN", ran, err)
	}

	//: the straggler's own outcome still reaches its caller.
	if err := slow.release(); err != nil {
		t.Fatalf("the straggler = %v, want nil", err)
	}

	//: 0s into a 1h window, nothing is admitted.
	if ran, err := probe(b); ran || !kerrs.HasCode(err, coreres.CodeCircuitOpen) {
		t.Fatalf("after the straggler's success: ran=%v err=%v, want CIRCUIT_OPEN", ran, err)
	}
	//: one tick before the cooldown ends, still nothing.
	clk.Advance(time.Hour - time.Nanosecond)
	if ran, _ := probe(b); ran {
		t.Fatal("a call was admitted before OpenDuration elapsed")
	}
	//: at the cooldown, the trial runs and closes it.
	clk.Advance(time.Nanosecond)
	if ran, err := probe(b); !ran || err != nil {
		t.Fatalf("the HalfOpen trial: ran=%v err=%v, want it run and nil", ran, err)
	}
	if ran, err := probe(b); !ran || err != nil {
		t.Fatalf("after a successful trial: ran=%v err=%v, want Closed", ran, err)
	}
}

// TestCircuitBreaker_StragglerFailureKeepsTheCooldown pins #467's second
// scenario. A call admitted before the trip, which fails at t0+59s, must not
// restart the cooldown: the trial is admitted at t0+60s, OpenDuration after the
// trip that actually opened the breaker.
func TestCircuitBreaker_StragglerFailureKeepsTheCooldown(t *testing.T) {
	t.Parallel()
	clk := clock.NewManualClock(time.Unix(1_700_000_000, 0))
	b := svcres.NewCircuitBreaker(svcres.BreakerConfig{FailureThreshold: 1, OpenDuration: time.Minute, Clock: clk})

	late := errors.New("late")
	slow := hold(t, b, late)
	_ = b.Run(t.Context(), func(context.Context) error { return errors.New("trip") })

	clk.Advance(59 * time.Second)
	//: the straggler's error still reaches its caller, unchanged.
	if err := slow.release(); !errors.Is(err, late) {
		t.Fatalf("the straggler = %v, want its own error", err)
	}
	clk.Advance(time.Second)
	if ran, err := probe(b); !ran || err != nil {
		t.Fatalf("OpenDuration after the trip: ran=%v err=%v, want the trial admitted", ran, err)
	}
}

// TestCircuitBreaker_StragglerDuringTheTrial pins a straggler that finishes
// while the HalfOpen trial is running: its success does not close the breaker
// for the trial, and its failure does not re-open it under the trial.
func TestCircuitBreaker_StragglerDuringTheTrial(t *testing.T) {
	t.Parallel()
	for _, result := range []error{nil, errors.New("late")} {
		clk := clock.NewManualClock(time.Unix(1_700_000_000, 0))
		b := svcres.NewCircuitBreaker(svcres.BreakerConfig{FailureThreshold: 1, OpenDuration: time.Minute, Clock: clk})

		slow := hold(t, b, result)
		_ = b.Run(t.Context(), func(context.Context) error { return errors.New("trip") })
		clk.Advance(time.Minute)
		trial := hold(t, b, nil)

		_ = slow.release()
		//: the trial still holds the only HalfOpen slot.
		if ran, err := probe(b); ran || !kerrs.HasCode(err, coreres.CodeCircuitOpen) {
			t.Fatalf("straggler %v during the trial: ran=%v err=%v, want CIRCUIT_OPEN", result, ran, err)
		}
		if err := trial.release(); err != nil {
			t.Fatalf("the trial = %v", err)
		}
		//: the trial's success, not the straggler's outcome, decided.
		if ran, err := probe(b); !ran || err != nil {
			t.Fatalf("straggler %v, after a successful trial: ran=%v err=%v, want Closed", result, ran, err)
		}
	}
}

// TestCircuitBreaker_HalfOpenAdmitsOneTrial pins the one-trial promise of
// BreakerConfig.OpenDuration: while the trial runs every other call is rejected
// fast, without running.
func TestCircuitBreaker_HalfOpenAdmitsOneTrial(t *testing.T) {
	t.Parallel()
	clk := clock.NewManualClock(time.Unix(1_700_000_000, 0))
	b := svcres.NewCircuitBreaker(svcres.BreakerConfig{FailureThreshold: 1, OpenDuration: time.Minute, Clock: clk})
	_ = b.Run(t.Context(), func(context.Context) error { return errors.New("trip") })
	clk.Advance(time.Minute)

	trial := hold(t, b, nil)
	for i := range 4 {
		if ran, err := probe(b); ran || !kerrs.HasCode(err, coreres.CodeCircuitOpen) {
			t.Fatalf("call %d during the trial: ran=%v err=%v, want CIRCUIT_OPEN", i, ran, err)
		}
	}
	if err := trial.release(); err != nil {
		t.Fatalf("the trial = %v", err)
	}
}

// TestCircuitBreaker_TrialSettlesTheSlot pins what frees the HalfOpen slot: a
// trial whose error the classifier rejects says nothing, so the next call is the
// trial; a trial that panics frees it too, or the breaker would reject forever;
// a failed trial re-opens for a fresh OpenDuration.
func TestCircuitBreaker_TrialSettlesTheSlot(t *testing.T) {
	t.Parallel()
	transient := errors.New("transient")
	permanent := errors.New("permanent")
	isTransient := func(err error) bool { return errors.Is(err, transient) }

	type tc struct {
		name string
		//: the trial; it may panic.
		trial func(context.Context) error
		//: whether the call after the trial is admitted at once.
		wantNextRuns bool
	}
	tests := []tc{
		{"a deterministic error frees the slot", func(context.Context) error { return permanent }, true},
		{"a panic frees the slot", func(context.Context) error { panic("boom") }, true},
		{"a failure re-opens", func(context.Context) error { return transient }, false},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		clk := clock.NewManualClock(time.Unix(1_700_000_000, 0))
		b := svcres.NewCircuitBreaker(svcres.BreakerConfig{
			FailureThreshold: 1, OpenDuration: time.Minute, Clock: clk, Retryable: isTransient,
		})
		_ = b.Run(t.Context(), func(context.Context) error { return transient })
		clk.Advance(time.Minute)

		func() {
			defer func() { _ = recover() }()
			_ = b.Run(t.Context(), c.trial)
		}()

		ran, err := probe(b)
		if ran != c.wantNextRuns {
			t.Fatalf("the call after the trial ran=%v (err=%v), want %v", ran, err, c.wantNextRuns)
		}
		//: a failed trial restarted the cooldown from now.
		if !c.wantNextRuns {
			clk.Advance(time.Minute)
			if ran, err := probe(b); !ran || err != nil {
				t.Fatalf("OpenDuration after the failed trial: ran=%v err=%v, want the next trial", ran, err)
			}
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}
