// Package resilience_test — the rate limiter that waits (#482): a token due
// within MaxWait is reserved and waited for on the injected clock.
package resilience_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	coreres "github.com/kitsunium/sdk/internal/core/app/resilience"
	"github.com/kitsunium/sdk/internal/kernel/clock"
	kerrs "github.com/kitsunium/sdk/internal/kernel/errs"
	svcres "github.com/kitsunium/sdk/internal/service/app/resilience"
)

// nowOnly is a clock that can read the time and cannot wait.
type nowOnly struct{ clock.Clock }

// TestRateLimiterWaitsForATokenDueWithinMaxWait pins the wait: with one token
// a second and a MaxWait of 1.5 s, the second call of an instant waits exactly
// one second for its token — not a nanosecond less — and then runs.
//
// GOROUTINE LIFECYCLE: the waiting call runs on a goroutine the test joins
// through done once the clock has been advanced past its token.
func TestRateLimiterWaitsForATokenDueWithinMaxWait(t *testing.T) {
	t.Parallel()
	manual := clock.NewManualClock(time.Unix(1_700_000_000, 0))
	l := svcres.NewRateLimiter(svcres.RateLimiterConfig{Rate: 1, Burst: 1, Clock: manual, MaxWait: 1500 * time.Millisecond})
	if err := l.Run(t.Context(), func(context.Context) error { return nil }); err != nil {
		t.Fatalf("first call = %v, want the full bucket's token", err)
	}
	var ran atomic.Bool
	done := make(chan error, 1)
	go func() {
		done <- l.Run(t.Context(), func(context.Context) error { ran.Store(true); return nil })
	}()
	manual.BlockUntil(1)
	manual.Advance(time.Second - time.Nanosecond)
	if ran.Load() || manual.Pending() != 1 {
		t.Fatal("the call ran before its token was due")
	}
	manual.Advance(time.Nanosecond)
	if err := <-done; err != nil || !ran.Load() {
		t.Fatalf("waiting call = %v (ran %v), want run once its token was due", err, ran.Load())
	}
}

// TestRateLimiterRejectsATokenDueBeyondMaxWait pins the reservation: once one
// call has reserved the next token, the call after it is due two seconds
// out, past a MaxWait of 1.5 s, and is rejected at once saying so.
func TestRateLimiterRejectsATokenDueBeyondMaxWait(t *testing.T) {
	t.Parallel()
	manual := clock.NewManualClock(time.Unix(1_700_000_000, 0))
	l := svcres.NewRateLimiter(svcres.RateLimiterConfig{Rate: 1, Burst: 1, Clock: manual, MaxWait: 1500 * time.Millisecond})
	_ = l.Run(t.Context(), func(context.Context) error { return nil })
	done := make(chan error, 1)
	go func() { done <- l.Run(t.Context(), func(context.Context) error { return nil }) }()
	manual.BlockUntil(1)
	err := l.Run(t.Context(), func(context.Context) error { return errors.New("ran past MaxWait") })
	if !kerrs.HasCode(err, coreres.CodeRateLimited) {
		t.Fatalf("third call = %v, want RATE_LIMITED", err)
	}
	if wait, ok := svcres.RetryAfter(err); !ok || wait != 2*time.Second {
		t.Fatalf("RetryAfter = %v, %v, want 2s behind the reservation", wait, ok)
	}
	manual.Advance(time.Second)
	if err := <-done; err != nil {
		t.Fatalf("reserved call = %v", err)
	}
}

// TestRateLimiterCancelledWaitHandsTheTokenBack pins the caller who leaves: a
// waiting call whose context ends returns ctx.Err(), not RateLimited, and the
// token it reserved is handed back — the next call waits one second, not two.
func TestRateLimiterCancelledWaitHandsTheTokenBack(t *testing.T) {
	t.Parallel()
	manual := clock.NewManualClock(time.Unix(1_700_000_000, 0))
	l := svcres.NewRateLimiter(svcres.RateLimiterConfig{Rate: 1, Burst: 1, Clock: manual, MaxWait: 1500 * time.Millisecond})
	_ = l.Run(t.Context(), func(context.Context) error { return nil })
	ctx, cancel := context.WithCancel(t.Context())
	left := make(chan error, 1)
	go func() { left <- l.Run(ctx, func(context.Context) error { return errors.New("ran after leaving") }) }()
	manual.BlockUntil(1)
	cancel()
	if err := <-left; !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled call = %v, want context.Canceled", err)
	}
	done := make(chan error, 1)
	go func() { done <- l.Run(t.Context(), func(context.Context) error { return nil }) }()
	manual.BlockUntil(1)
	manual.Advance(time.Second)
	if err := <-done; err != nil {
		t.Fatalf("next call = %v, want its token after one second", err)
	}
}

// TestRateLimiterWithoutMaxWaitStillRejectsAtOnce pins the zero: no MaxWait is
// the rejecting limiter, which arms no wait.
func TestRateLimiterWithoutMaxWaitStillRejectsAtOnce(t *testing.T) {
	t.Parallel()
	manual := clock.NewManualClock(time.Unix(1_700_000_000, 0))
	l := svcres.NewRateLimiter(svcres.RateLimiterConfig{Rate: 1, Burst: 1, Clock: manual})
	_ = l.Run(t.Context(), func(context.Context) error { return nil })
	if err := l.Run(t.Context(), func(context.Context) error { return nil }); !kerrs.HasCode(err, coreres.CodeRateLimited) {
		t.Fatalf("second call = %v, want RATE_LIMITED", err)
	}
	if manual.Pending() != 0 {
		t.Fatal("the rejecting limiter armed a wait")
	}
}

// TestRateLimiterRefusesAWaitOnAClockThatCannotWait pins the refusal: a
// positive MaxWait with a Now-only clock is PolicyMisconfigured naming Clock,
// while the same clock without MaxWait is accepted.
func TestRateLimiterRefusesAWaitOnAClockThatCannotWait(t *testing.T) {
	t.Parallel()
	clk := nowOnly{clock.NewManualClock(time.Unix(1_700_000_000, 0))}
	l := svcres.NewRateLimiter(svcres.RateLimiterConfig{Rate: 1, Clock: clk, MaxWait: time.Second})
	err := l.Run(t.Context(), func(context.Context) error { t.Fatal("ran under a refused policy"); return nil })
	if !kerrs.HasCode(err, coreres.CodePolicyMisconfigured) || knobOf(err) != "Clock" {
		t.Fatalf("Run = %v, want POLICY_MISCONFIGURED naming Clock", err)
	}
	ok := svcres.NewRateLimiter(svcres.RateLimiterConfig{Rate: 1, Clock: clk})
	if err := ok.Run(t.Context(), func(context.Context) error { return nil }); err != nil {
		t.Fatalf("Run without MaxWait = %v, want admitted", err)
	}
}
