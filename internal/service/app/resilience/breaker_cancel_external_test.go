// Package resilience_test — the circuit breaker under callers that leave.
package resilience_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	coreres "github.com/kitsunium/sdk/internal/core/app/resilience"
	"github.com/kitsunium/sdk/internal/kernel/clock"
	kerrs "github.com/kitsunium/sdk/internal/kernel/errs"
	svcres "github.com/kitsunium/sdk/internal/service/app/resilience"
)

// cancelledContext returns a context the caller has already cancelled.
func cancelledContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	return ctx
}

// TestCircuitBreaker_CallerCancellationIsNeutral pins #477: a call that ends in
// context.Canceled because its caller went away says nothing about the
// dependency. With a threshold of one, a single counted failure would open the
// breaker; a hundred disconnects must not.
func TestCircuitBreaker_CallerCancellationIsNeutral(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		//: what the operation returns once its caller cancelled.
		result error
	}
	tests := []tc{
		{name: "the bare cancellation", result: context.Canceled},
		{name: "a wrapped cancellation", result: fmt.Errorf("dial: %w", context.Canceled)},
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			b := svcres.NewCircuitBreaker(svcres.BreakerConfig{
				FailureThreshold: 1, OpenDuration: time.Hour,
				Clock: clock.NewManualClock(time.Unix(1_700_000_000, 0)),
				//: the classifier is not asked: a caller who left is neutral
				//: whatever it would have said.
				Retryable: func(err error) bool {
					if errors.Is(err, context.Canceled) {
						t.Errorf("Retryable was consulted for a caller's cancellation: %v", err)
					}
					return true
				},
			})
			for range 100 {
				err := b.Run(cancelledContext(t), func(context.Context) error { return c.result })
				//: the caller still receives the operation's own outcome.
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("Run = %v, want the operation's cancellation", err)
				}
			}
			if ran, err := probe(b); !ran || err != nil {
				t.Fatalf("after 100 cancelled calls: ran=%v err=%v, want the breaker still Closed", ran, err)
			}
		})
	}
}

// TestCircuitBreaker_FailuresThatAreNotACallerLeaving pins the two errors that
// look like a cancellation and still count. A DeadlineExceeded is a dependency
// too slow for its caller's deadline; a Canceled the operation returns while
// its caller is still there was cancelled by something else — the dependency
// side — and is a failure like any other.
func TestCircuitBreaker_FailuresThatAreNotACallerLeaving(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		ctx  func(t *testing.T) context.Context
		err  error
	}
	expired := func(t *testing.T) context.Context {
		t.Helper()
		ctx, cancel := context.WithDeadline(t.Context(), time.Unix(0, 0))
		t.Cleanup(cancel)
		return ctx
	}
	live := func(t *testing.T) context.Context {
		t.Helper()
		return t.Context()
	}
	tests := []tc{
		{name: "a deadline the caller set and the dependency missed", ctx: expired, err: context.DeadlineExceeded},
		{name: "a cancellation under a live caller", ctx: live, err: context.Canceled},
		{name: "a deadline under a cancelled caller", ctx: cancelledContext, err: context.DeadlineExceeded},
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			b := svcres.NewCircuitBreaker(svcres.BreakerConfig{
				FailureThreshold: 1, OpenDuration: time.Hour,
				Clock: clock.NewManualClock(time.Unix(1_700_000_000, 0)),
			})
			_ = b.Run(c.ctx(t), func(context.Context) error { return c.err })
			if ran, err := probe(b); ran || !kerrs.HasCode(err, coreres.CodeCircuitOpen) {
				t.Fatalf("after one %v: ran=%v err=%v, want CIRCUIT_OPEN", c.err, ran, err)
			}
		})
	}
}

// TestCircuitBreaker_CancelledTrialFreesTheSlot pins the HalfOpen half of
// #477: a trial whose caller leaves is neither the success that closes the
// breaker nor the failure that re-opens it, and the next call is the trial.
func TestCircuitBreaker_CancelledTrialFreesTheSlot(t *testing.T) {
	t.Parallel()
	clk := clock.NewManualClock(time.Unix(1_700_000_000, 0))
	b := svcres.NewCircuitBreaker(svcres.BreakerConfig{FailureThreshold: 1, OpenDuration: time.Minute, Clock: clk})
	_ = b.Run(t.Context(), func(context.Context) error { return errors.New("down") })
	clk.Advance(time.Minute)

	//: the trial's caller leaves.
	trialRan := false
	err := b.Run(cancelledContext(t), func(ctx context.Context) error {
		trialRan = true
		return ctx.Err()
	})
	if !trialRan || !errors.Is(err, context.Canceled) {
		t.Fatalf("the trial: ran=%v err=%v, want it to run and report the cancellation", trialRan, err)
	}
	//: no cooldown restarted, no slot held: the next call is the trial, and
	//: its success closes the breaker.
	if ran, err := probe(b); !ran || err != nil {
		t.Fatalf("the next call: ran=%v err=%v, want it admitted as the trial", ran, err)
	}
	if ran, err := probe(b); !ran || err != nil {
		t.Fatalf("after the trial's success: ran=%v err=%v, want Closed", ran, err)
	}
}
