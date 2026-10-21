// Package resilience_test — HalfOpenTrials (#482): n successful trials in a
// row close the breaker, one at a time, and any failed one opens it again.
package resilience_test

import (
	"context"
	"testing"
	"time"

	coreres "github.com/kitsunium/sdk/internal/core/app/resilience"
	"github.com/kitsunium/sdk/internal/kernel/clock"
	kerrs "github.com/kitsunium/sdk/internal/kernel/errs"
	svcres "github.com/kitsunium/sdk/internal/service/app/resilience"
)

// tripped returns a breaker of trials HalfOpenTrials, opened by one failure,
// whose cooldown of one second has elapsed: the next call is its first trial.
func tripped(t *testing.T, trials int) (coreres.Runner, coreres.BreakerState, *clock.ManualClock) {
	t.Helper()
	manual := clock.NewManualClock(time.Unix(1_700_000_000, 0))
	b := svcres.NewCircuitBreaker(svcres.BreakerConfig{
		FailureThreshold: 1, OpenDuration: time.Second, Clock: manual, HalfOpenTrials: trials,
	})
	_ = b.Run(t.Context(), func(context.Context) error { return errDown })
	state := b.(coreres.BreakerState)
	if got := state.State().Phase; got != coreres.BreakerOpen {
		t.Fatalf("after a failure the breaker is %v, want open", got)
	}
	manual.Advance(time.Second)
	return b, state, manual
}

// TestBreakerClosesOnlyAfterHalfOpenTrialsSuccesses pins the count: with three
// trials asked for, the first two successes leave the breaker HalfOpen and the
// third closes it.
func TestBreakerClosesOnlyAfterHalfOpenTrialsSuccesses(t *testing.T) {
	t.Parallel()
	b, state, _ := tripped(t, 3)
	for trial := 1; trial <= 3; trial++ {
		if err := b.Run(t.Context(), func(context.Context) error { return nil }); err != nil {
			t.Fatalf("trial %d = %v, want admitted", trial, err)
		}
		want := coreres.BreakerHalfOpen
		if trial == 3 {
			want = coreres.BreakerClosed
		}
		if got := state.State().Phase; got != want {
			t.Fatalf("after trial %d the breaker is %v, want %v", trial, got, want)
		}
	}
}

// TestBreakerReopensOnAFailedTrialAfterSuccesses pins "in a row": a success
// then a failure opens the breaker again, and the next HalfOpen phase counts
// from zero.
func TestBreakerReopensOnAFailedTrialAfterSuccesses(t *testing.T) {
	t.Parallel()
	b, state, manual := tripped(t, 2)
	_ = b.Run(t.Context(), func(context.Context) error { return nil })
	_ = b.Run(t.Context(), func(context.Context) error { return errDown })
	if got := state.State().Phase; got != coreres.BreakerOpen {
		t.Fatalf("after a failed second trial the breaker is %v, want open", got)
	}
	manual.Advance(time.Second)
	_ = b.Run(t.Context(), func(context.Context) error { return nil })
	if got := state.State().Phase; got != coreres.BreakerHalfOpen {
		t.Fatalf("one success into a new HalfOpen phase the breaker is %v, want half-open", got)
	}
	_ = b.Run(t.Context(), func(context.Context) error { return nil })
	if got := state.State().Phase; got != coreres.BreakerClosed {
		t.Fatalf("after two successes in a row the breaker is %v, want closed", got)
	}
}

// TestBreakerHalfOpenTrialsZeroIsOne pins the zero and a negative value: the
// first successful trial closes the breaker, as before the field existed.
func TestBreakerHalfOpenTrialsZeroIsOne(t *testing.T) {
	t.Parallel()
	for _, trials := range []int{0, -3, 1} {
		b, state, _ := tripped(t, trials)
		_ = b.Run(t.Context(), func(context.Context) error { return nil })
		if got := state.State().Phase; got != coreres.BreakerClosed {
			t.Fatalf("HalfOpenTrials %d: after one success the breaker is %v, want closed", trials, got)
		}
	}
}

// TestBreakerHalfOpenTrialsRunOneAtATime pins that asking for several trials
// does not admit several at once: while a trial runs, every other call is
// rejected with CircuitOpen.
//
// GOROUTINE LIFECYCLE: the trial runs on a goroutine the test releases and
// joins before returning.
func TestBreakerHalfOpenTrialsRunOneAtATime(t *testing.T) {
	t.Parallel()
	b, _, _ := tripped(t, 3)
	entered, release := make(chan struct{}), make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- b.Run(t.Context(), func(context.Context) error { close(entered); <-release; return nil })
	}()
	<-entered
	err := b.Run(t.Context(), func(context.Context) error { t.Fatal("a second trial ran"); return nil })
	if !kerrs.HasCode(err, coreres.CodeCircuitOpen) {
		t.Fatalf("concurrent call = %v, want CircuitOpen", err)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatalf("trial = %v", err)
	}
}
