package resilience_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/kitsunium/sdk/pkg/v1/resilience"
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
