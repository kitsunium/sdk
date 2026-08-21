//go:generate gomarkdoc --output README.md --repository.url https://github.com/kitsunium/sdk --repository.default-branch main --repository.path /pkg/v1/resilience .

// Package resilience is the public facade for the SDK's reliability policies:
// retry, circuit-breaker, rate-limit, bulkhead, and timeout. Each constructor
// returns a [Runner] that guards an [Operation] (a ctx-aware func); policies
// compose by nesting.
//
//	r := resilience.NewRetry(resilience.RetryConfig{MaxAttempts: 3})
//	b := resilience.NewCircuitBreaker(resilience.BreakerConfig{FailureThreshold: 5, OpenDuration: time.Second})
//	err := r.Run(ctx, func(ctx context.Context) error {
//	    return b.Run(ctx, callDownstream) // Retry(Breaker(op))
//	})
//
// Rejections surface typed sentinels (RetryExhausted / CircuitOpen / RateLimited
// / BulkheadFull / TimeoutExceeded) matchable via errs.HasReason / errs.HasCode.
package resilience

import (
	"time"

	coreres "github.com/kitsunium/sdk/internal/core/resilience"
	svcres "github.com/kitsunium/sdk/internal/service/resilience"
)

// Operation is the public alias for the ctx-aware unit of guarded work.
type Operation = coreres.Operation

// Runner is the public alias for the composable policy-executor contract.
type Runner = coreres.Runner

// RetryConfig is the public alias for the retry policy configuration.
type RetryConfig = svcres.RetryConfig

// BreakerConfig is the public alias for the circuit-breaker configuration.
type BreakerConfig = svcres.BreakerConfig

// RateLimiterConfig is the public alias for the rate-limiter configuration.
type RateLimiterConfig = svcres.RateLimiterConfig

var (
	// RetryExhausted is returned when the retry budget is spent.
	RetryExhausted = coreres.RetryExhausted
	// CircuitOpen is returned when the breaker rejects a call fast.
	CircuitOpen = coreres.CircuitOpen
	// RateLimited is returned when no rate-limit token is available.
	RateLimited = coreres.RateLimited
	// BulkheadFull is returned when every concurrency slot is occupied.
	BulkheadFull = coreres.BulkheadFull
	// TimeoutExceeded is returned when an operation outruns its deadline.
	TimeoutExceeded = coreres.TimeoutExceeded
)

// NewRetry returns a retry-with-backoff Runner.
func NewRetry(cfg RetryConfig) Runner {
	//: delegate to the service constructor.
	return svcres.NewRetry(cfg)
}

// NewCircuitBreaker returns a circuit-breaker Runner.
func NewCircuitBreaker(cfg BreakerConfig) Runner {
	//: delegate to the service constructor.
	return svcres.NewCircuitBreaker(cfg)
}

// NewRateLimiter returns a token-bucket rate-limiter Runner.
func NewRateLimiter(cfg RateLimiterConfig) Runner {
	//: delegate to the service constructor.
	return svcres.NewRateLimiter(cfg)
}

// NewBulkhead returns a bounded-concurrency Runner (reject mode).
func NewBulkhead(maxConcurrent int) Runner {
	//: delegate to the service constructor.
	return svcres.NewBulkhead(maxConcurrent)
}

// NewTimeout returns a deadline-enforcing Runner.
func NewTimeout(d time.Duration) Runner {
	//: delegate to the service constructor.
	return svcres.NewTimeout(d)
}
