package resilience_test

import (
	"context"
	"errors"
	"testing"
	"time"

	coreres "github.com/kitsunium/sdk/internal/core/resilience"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	res "github.com/kitsunium/sdk/internal/service/resilience"
)

// fakeClock is a settable clock for deterministic breaker/limiter tests.
type fakeClock struct{ now time.Time }

func (f *fakeClock) Now() time.Time                  { return f.now }
func (f *fakeClock) Since(t time.Time) time.Duration { return f.now.Sub(t) }
func (f *fakeClock) advance(d time.Duration)         { f.now = f.now.Add(d) }

var errBoom = errors.New("boom")

// TestRetrySucceedsThenExhausts covers the two retry outcomes.
func TestRetrySucceedsThenExhausts(t *testing.T) {
	t.Parallel()
	r := res.NewRetry(res.RetryConfig{MaxAttempts: 3})
	//: an op that succeeds on the 3rd attempt returns nil.
	calls := 0
	err := r.Run(context.Background(), func(context.Context) error {
		calls++
		if calls < 3 {
			return errBoom
		}
		return nil
	})
	if err != nil || calls != 3 {
		t.Fatalf("retry-success: err=%v calls=%d, want nil/3", err, calls)
	}
	//: an always-failing op exhausts and wraps as RetryExhausted.
	err = r.Run(context.Background(), func(context.Context) error { return errBoom })
	if !errs.HasCode(err, coreres.CodeRetryExhausted) {
		t.Errorf("retry-exhaust: err=%v, want RETRY_EXHAUSTED", err)
	}
}

// TestTimeout covers the deadline path + the fast path.
func TestTimeout(t *testing.T) {
	t.Parallel()
	to := res.NewTimeout(10 * time.Millisecond)
	//: an op that respects ctx and overruns surfaces TIMEOUT_EXCEEDED.
	err := to.Run(context.Background(), func(ctx context.Context) error {
		<-ctx.Done()
		return ctx.Err()
	})
	if !errs.HasCode(err, coreres.CodeTimeoutExceeded) {
		t.Errorf("timeout: err=%v, want TIMEOUT_EXCEEDED", err)
	}
	//: a fast op passes through.
	if err := to.Run(context.Background(), func(context.Context) error { return nil }); err != nil {
		t.Errorf("timeout fast-path: err=%v, want nil", err)
	}
}

// TestBreakerTripsAndRecovers covers Closed→Open→HalfOpen→Closed.
func TestBreakerTripsAndRecovers(t *testing.T) {
	t.Parallel()
	clk := &fakeClock{now: time.Unix(0, 0)}
	b := res.NewCircuitBreaker(res.BreakerConfig{FailureThreshold: 2, OpenDuration: time.Minute, Clock: clk})
	fail := func(context.Context) error { return errBoom }
	//: two failures trip the breaker Open.
	b.Run(context.Background(), fail)
	b.Run(context.Background(), fail)
	//: a call while Open is rejected fast with CIRCUIT_OPEN.
	if err := b.Run(context.Background(), fail); !errs.HasCode(err, coreres.CodeCircuitOpen) {
		t.Fatalf("breaker open: err=%v, want CIRCUIT_OPEN", err)
	}
	//: after the cooldown, a successful trial closes the breaker.
	clk.advance(2 * time.Minute)
	if err := b.Run(context.Background(), func(context.Context) error { return nil }); err != nil {
		t.Fatalf("breaker half-open trial: err=%v, want nil", err)
	}
	//: the breaker is Closed again and admits calls.
	if err := b.Run(context.Background(), func(context.Context) error { return nil }); err != nil {
		t.Errorf("breaker closed: err=%v, want nil", err)
	}
}

// TestRateLimiterBurst covers burst admission then rejection.
func TestRateLimiterBurst(t *testing.T) {
	t.Parallel()
	clk := &fakeClock{now: time.Unix(0, 0)}
	rl := res.NewRateLimiter(res.RateLimiterConfig{Rate: 1, Burst: 2, Clock: clk})
	noop := func(context.Context) error { return nil }
	//: the burst of 2 is admitted.
	if err := rl.Run(context.Background(), noop); err != nil {
		t.Fatalf("token 1: %v", err)
	}
	if err := rl.Run(context.Background(), noop); err != nil {
		t.Fatalf("token 2: %v", err)
	}
	//: the 3rd immediate call is rate-limited.
	if err := rl.Run(context.Background(), noop); !errs.HasCode(err, coreres.CodeRateLimited) {
		t.Fatalf("token 3: err=%v, want RATE_LIMITED", err)
	}
	//: after 1s a refill (rate=1/s) admits one more.
	clk.advance(time.Second)
	if err := rl.Run(context.Background(), noop); err != nil {
		t.Errorf("after refill: err=%v, want nil", err)
	}
}

// TestBulkheadRejectsWhenFull uses a re-entrant Run (no goroutine): the outer
// op holds the only slot, so a nested Run sees the bulkhead full.
func TestBulkheadRejectsWhenFull(t *testing.T) {
	t.Parallel()
	bh := res.NewBulkhead(1)
	var nestedErr error
	//: the outer Run occupies the single slot for the duration of its op.
	outerErr := bh.Run(context.Background(), func(ctx context.Context) error {
		//: a nested Run, while the slot is held, must be rejected.
		nestedErr = bh.Run(ctx, func(context.Context) error { return nil })
		//: the outer op itself succeeds.
		return nil
	})
	//: the outer op completed cleanly.
	if outerErr != nil {
		t.Fatalf("outer Run: %v", outerErr)
	}
	//: the nested call hit the full bulkhead.
	if !errs.HasCode(nestedErr, coreres.CodeBulkheadFull) {
		t.Errorf("nested Run: err=%v, want BULKHEAD_FULL", nestedErr)
	}
}
