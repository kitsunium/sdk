package resilience_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kitsunium/sdk/pkg/v1/app/resilience"
)

// errSink defeats dead-code elimination on every Run below.
var errSink error

// errBenchFailure is the error the failing operations return. It is a package
// value so constructing it never lands inside a timed loop.
var errBenchFailure = errors.New("bench: operation failed")

// okOp is the cheapest possible protected call: it does nothing and succeeds.
// Every number in this file is therefore the POLICY's own cost, with the work
// it guards held at zero — which is what a caller needs in order to decide
// whether wrapping a 20 ns call is sensible or whether wrapping a 20 ms one is
// free.
func okOp(context.Context) error { return nil }

// failOp always fails, which is what drives the retry, breaker and fallback
// paths into the branches that actually cost something.
func failOp(context.Context) error { return errBenchFailure }

// BenchmarkBaseline_NoPolicy is the floor: calling the operation directly,
// through the same function-value indirection a Runner would. Every policy
// below should be read as a delta against this.
func BenchmarkBaseline_NoPolicy(b *testing.B) {
	ctx := b.Context()
	op := okOp
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		errSink = op(ctx)
	}
}

// BenchmarkRetry_FirstAttemptSucceeds is the case that matters most: a retry
// policy spends almost all of its life NOT retrying, so its cost on the happy
// path is what every guarded call pays.
func BenchmarkRetry_FirstAttemptSucceeds(b *testing.B) {
	runner := resilience.NewRetry(resilience.RetryConfig{
		MaxAttempts: 3, BaseDelay: time.Millisecond, MaxDelay: time.Second, Multiplier: 2,
	})
	ctx := b.Context()
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		errSink = runner.Run(ctx, okOp)
	}
}

// BenchmarkRetry_WithBudget_FirstAttemptSucceeds is the happy path with a
// shared retry budget: one more interface call and one compare-and-swap on the
// way out. It must stay allocation-free — the budget sits on every guarded
// call of a dependency.
func BenchmarkRetry_WithBudget_FirstAttemptSucceeds(b *testing.B) {
	runner := resilience.NewRetry(resilience.RetryConfig{
		MaxAttempts: 3, BaseDelay: time.Millisecond, MaxDelay: time.Second, Multiplier: 2,
		Budget: resilience.NewRetryBudget(resilience.RetryBudgetConfig{MaxTokens: 10, TokenRatio: 0.1}),
	})
	ctx := b.Context()
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		errSink = runner.Run(ctx, okOp)
	}
}

// boolSink defeats dead-code elimination on the budget's answers; atomic,
// since every parallel worker stores into it.
var boolSink atomic.Bool

// BenchmarkRetryBudget_Contended is the budget alone under every core at
// once: a failure, a question and a success per iteration, each a lock-free
// compare-and-swap or load on one shared counter. Zero allocations.
func BenchmarkRetryBudget_Contended(b *testing.B) {
	budget := resilience.NewRetryBudget(resilience.RetryBudgetConfig{MaxTokens: 100, TokenRatio: 1})
	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		allowed := false
		for pb.Next() {
			budget.RecordFailure()
			allowed = budget.AllowRetry()
			budget.RecordSuccess()
		}
		boolSink.Store(allowed)
	})
}

// BenchmarkCircuitBreaker_Closed is the same question for a breaker: closed is
// the state it is in whenever the dependency is healthy.
func BenchmarkCircuitBreaker_Closed(b *testing.B) {
	runner := resilience.NewCircuitBreaker(resilience.BreakerConfig{
		FailureThreshold: 5, OpenDuration: time.Second,
	})
	ctx := b.Context()
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		errSink = runner.Run(ctx, okOp)
	}
}

// BenchmarkCircuitBreaker_Window_Closed is the closed path of a breaker that
// opens on a rate: the same lock, plus two reads of the clock to time the
// call and one slot of the window filled. It must stay allocation-free — a
// window is filled on every guarded call of a dependency.
func BenchmarkCircuitBreaker_Window_Closed(b *testing.B) {
	runner := resilience.NewCircuitBreaker(resilience.BreakerConfig{
		OpenDuration: time.Second,
		Window:       &resilience.BreakerWindowValue{Size: 30 * time.Second, Buckets: 10, MinCalls: 100, FailureRatio: 0.5},
	})
	ctx := b.Context()
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		errSink = runner.Run(ctx, okOp)
	}
}

// BenchmarkCircuitBreaker_Window_Closed_Listened is the same closed path with
// an OnStateChange set: a listener costs nothing while the phase holds.
func BenchmarkCircuitBreaker_Window_Closed_Listened(b *testing.B) {
	changes := 0
	runner := resilience.NewCircuitBreaker(resilience.BreakerConfig{
		OpenDuration:  time.Second,
		Window:        &resilience.BreakerWindowValue{},
		OnStateChange: func(resilience.BreakerPhase, resilience.BreakerPhase) { changes++ },
	})
	ctx := b.Context()
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		errSink = runner.Run(ctx, okOp)
	}
	if changes != 0 {
		b.Fatalf("a healthy breaker changed phase %d times", changes)
	}
}

// BenchmarkCircuitBreaker_State is what a readiness probe or a metrics scrape
// pays to read a breaker: one lock and a copy.
func BenchmarkCircuitBreaker_State(b *testing.B) {
	state, ok := resilience.NewCircuitBreaker(resilience.BreakerConfig{
		Window: &resilience.BreakerWindowValue{},
	}).(resilience.BreakerState)
	if !ok {
		b.Fatal("the breaker is not a BreakerState")
	}
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		phaseSink = state.State().Phase
	}
}

// phaseSink defeats dead-code elimination on State's answers.
var phaseSink resilience.BreakerPhase

// BenchmarkCircuitBreaker_Open is the point of a breaker: once it is open it
// must refuse without touching the dependency, and refusing must be far cheaper
// than the call it is standing in for. If this were not much cheaper than
// Closed, the breaker would be protecting nothing.
func BenchmarkCircuitBreaker_Open(b *testing.B) {
	runner := resilience.NewCircuitBreaker(resilience.BreakerConfig{
		FailureThreshold: 1, OpenDuration: time.Hour,
	})
	ctx := b.Context()
	//: trip it once, outside the timed loop.
	if err := runner.Run(ctx, failOp); err == nil {
		b.Fatal("the tripping call succeeded")
	}
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		errSink = runner.Run(ctx, okOp)
	}
	if errSink == nil {
		b.Fatal("an open breaker admitted a call")
	}
}

// BenchmarkRateLimiter_Admitted measures the token-bucket check on the path
// where the call is allowed through — the only path a healthy service takes.
func BenchmarkRateLimiter_Admitted(b *testing.B) {
	runner := resilience.NewRateLimiter(resilience.RateLimiterConfig{
		Rate: 1e9, Burst: 1_000_000,
	})
	ctx := b.Context()
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		errSink = runner.Run(ctx, okOp)
	}
}

// BenchmarkRateLimiter_Admitted_WithMaxWait is the same admitted call on a
// limiter that may wait (ADR 0184): a token is available, so no wait is armed,
// and the cost must stay the rejecting limiter's.
func BenchmarkRateLimiter_Admitted_WithMaxWait(b *testing.B) {
	runner := resilience.NewRateLimiter(resilience.RateLimiterConfig{
		Rate: 1e9, Burst: 1_000_000, MaxWait: time.Millisecond,
	})
	ctx := b.Context()
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		errSink = runner.Run(ctx, okOp)
	}
}

// BenchmarkBulkheadWait_Uncontended is the bulkhead that queues (ADR 0184) on
// the path a healthy service takes: a slot is free, so the queue and the
// clock are never touched.
func BenchmarkBulkheadWait_Uncontended(b *testing.B) {
	runner := resilience.NewBulkheadWait(resilience.BulkheadWaitConfig{Max: 16, Queue: 16, MaxWait: time.Millisecond})
	ctx := b.Context()
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		errSink = runner.Run(ctx, okOp)
	}
}

// BenchmarkAdaptiveThrottle_Healthy is the adaptive throttle (ADR 0184) in
// front of a dependency accepting every call: the refusal probability is zero,
// so no draw is made — one lock and a slot advance per call.
func BenchmarkAdaptiveThrottle_Healthy(b *testing.B) {
	runner := resilience.NewAdaptiveThrottle(resilience.AdaptiveThrottleConfig{})
	ctx := b.Context()
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		errSink = runner.Run(ctx, okOp)
	}
}

// BenchmarkBulkhead_Uncontended and _Contended bracket the concurrency limiter:
// the first is what one goroutine pays for the guarantee, the second is what
// eight pay for it, and the ratio is the number that decides whether a bulkhead
// belongs on a hot path.
func BenchmarkBulkhead_Uncontended(b *testing.B) {
	runner := resilience.NewBulkhead(16)
	ctx := b.Context()
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		errSink = runner.Run(ctx, okOp)
	}
}

func BenchmarkBulkhead_Contended(b *testing.B) {
	runner := resilience.NewBulkhead(16)
	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		ctx := context.Background()
		for pb.Next() {
			if err := runner.Run(ctx, okOp); err != nil {
				b.Errorf("Run: %v", err)
				return
			}
		}
	})
}

// BenchmarkTimeout is a deadline that never fires, which is the normal case:
// the cost is one derived context per call, and a derived context allocates.
func BenchmarkTimeout(b *testing.B) {
	runner := resilience.NewTimeout(time.Hour)
	ctx := b.Context()
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		errSink = runner.Run(ctx, okOp)
	}
}

// BenchmarkFallback_PrimarySucceeds is the happy path — plan B is never run —
// so this is what having a fallback wired up costs when it is not needed.
func BenchmarkFallback_PrimarySucceeds(b *testing.B) {
	runner := resilience.NewFallback(resilience.FallbackConfig{Fallback: okOp})
	ctx := b.Context()
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		errSink = runner.Run(ctx, okOp)
	}
}

// BenchmarkFallback_PrimaryFails runs both operations, so the delta against the
// happy path is the cost of the switch itself rather than of plan B.
func BenchmarkFallback_PrimaryFails(b *testing.B) {
	runner := resilience.NewFallback(resilience.FallbackConfig{Fallback: okOp})
	ctx := b.Context()
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		errSink = runner.Run(ctx, failOp)
	}
}

// BenchmarkComposed_RetryBreakerTimeout is the stack a real service wires, and
// the number a caller actually budgets. ADR 0026 says policies compose by
// wrapping; this is what the wrapping costs.
func BenchmarkComposed_RetryBreakerTimeout(b *testing.B) {
	timeout := resilience.NewTimeout(time.Hour)
	breaker := resilience.NewCircuitBreaker(resilience.BreakerConfig{
		FailureThreshold: 5, OpenDuration: time.Second,
	})
	retry := resilience.NewRetry(resilience.RetryConfig{
		MaxAttempts: 3, BaseDelay: time.Millisecond, MaxDelay: time.Second, Multiplier: 2,
	})
	ctx := b.Context()
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		errSink = retry.Run(ctx, func(inner context.Context) error {
			return breaker.Run(inner, func(innermost context.Context) error {
				return timeout.Run(innermost, okOp)
			})
		})
	}
}
