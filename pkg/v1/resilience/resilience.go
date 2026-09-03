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
//
// # Classifying deterministic failures
//
// Retry and the circuit-breaker act on transient failures. A deterministic one —
// a policy refusal, an HTTP 400, invalid input — gains nothing from a replay and
// says nothing about a dependency's health, yet by default both policies treat
// every non-nil error as transient: the retry spends its whole budget on it and
// hides it behind RetryExhausted, and the breaker counts it towards tripping.
// Set the Retryable predicate on either config to tell the two apart. It takes
// the same shape in both, so a nested Retry(Breaker(op)) shares one classifier:
//
//	transient := func(err error) bool { return !errors.Is(err, ErrBadRequest) }
//	r := resilience.NewRetry(resilience.RetryConfig{MaxAttempts: 3, Retryable: transient})
//	b := resilience.NewCircuitBreaker(resilience.BreakerConfig{FailureThreshold: 5, Retryable: transient})
//
// A rejected error comes back verbatim: the retry stops at that attempt without
// backoff and without the RetryExhausted relabel, and the breaker leaves its
// state machine untouched. A nil predicate keeps the historical behaviour.
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
	// PolicyMisconfigured is returned by every call to a policy that was built
	// with a configuration it cannot honour — a non-positive RateLimiterConfig
	// .Rate, a non-positive NewTimeout duration. The operation is not run.
	// Unlike the sentinels above it is permanent, not transient: the fix is at
	// the construction site, never a retry (ADR 0031).
	PolicyMisconfigured = coreres.PolicyMisconfigured
)

// NewRetry returns a retry-with-backoff Runner. An error rejected by
// cfg.Retryable ends the loop at once and is returned verbatim.
func NewRetry(cfg RetryConfig) Runner {
	//: delegate to the service constructor.
	return svcres.NewRetry(cfg)
}

// NewCircuitBreaker returns a circuit-breaker Runner. An error rejected by
// cfg.Retryable is returned verbatim and left out of the state machine.
func NewCircuitBreaker(cfg BreakerConfig) Runner {
	//: delegate to the service constructor.
	return svcres.NewCircuitBreaker(cfg)
}

// NewRateLimiter returns a token-bucket rate-limiter Runner. A non-positive
// cfg.Rate is refused: every call returns PolicyMisconfigured without running
// the operation, because any rate chosen for the caller would be a guess.
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
