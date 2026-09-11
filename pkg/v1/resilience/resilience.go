//go:generate gomarkdoc --output README.md --repository.url https://github.com/kitsunium/sdk --repository.default-branch main --repository.path /pkg/v1/resilience .

// Package resilience is the public facade for the SDK's reliability policies:
// retry, circuit-breaker, rate-limit, bulkhead, timeout, fallback, and hedging.
// Each constructor returns a [Runner] that guards an [Operation] (a ctx-aware
// func); policies compose by nesting.
//
//	r := resilience.NewRetry(resilience.RetryConfig{MaxAttempts: 3})
//	b := resilience.NewCircuitBreaker(resilience.BreakerConfig{FailureThreshold: 5, OpenDuration: time.Second})
//	err := r.Run(ctx, func(ctx context.Context) error {
//	    return b.Run(ctx, callDownstream) // Retry(Breaker(op))
//	})
//
// Rejections surface typed sentinels (RetryExhausted / CircuitOpen / RateLimited
// / BulkheadFull / TimeoutExceeded / FallbackFailed) matchable via
// errs.HasReason / errs.HasCode.
//
// # Classifying deterministic failures
//
// Retry and the circuit-breaker act on transient failures. A deterministic one —
// a policy refusal, an HTTP 400, invalid input — gains nothing from a replay and
// says nothing about a dependency's health, yet by default both policies treat
// every non-nil error as transient: the retry spends its whole budget on it and
// hides it behind RetryExhausted, and the breaker counts it towards tripping.
// Set the Retryable predicate on either config to tell the two apart. It takes
// the same shape in both, so a nested Retry(Breaker(op)) shares one classifier.
// The sentinel is the caller's own — this package exports none to classify
// against, because only the caller knows which of their failures are
// deterministic:
//
//	var ErrBadRequest = errors.New("bad request") // declared by the caller
//
//	transient := func(err error) bool { return !errors.Is(err, ErrBadRequest) }
//	r := resilience.NewRetry(resilience.RetryConfig{MaxAttempts: 3, Retryable: transient})
//	b := resilience.NewCircuitBreaker(resilience.BreakerConfig{FailureThreshold: 5, Retryable: transient})
//
// A rejected error comes back verbatim: the retry stops at that attempt without
// backoff and without the RetryExhausted relabel, and the breaker leaves its
// state machine untouched. A nil predicate keeps the historical behaviour.
// [FallbackConfig] takes the same predicate, where it decides whether a primary
// failure is worth serving a substitute answer for.
//
// # Falling back
//
// [NewFallback] runs a second Operation when the first one fails, and reports
// success when it works — that masking is the policy. The port carries no
// result value, so a "fallback VALUE" is a closure assigning the caller's own
// default:
//
//	var page []byte
//	f := resilience.NewFallback(resilience.FallbackConfig{
//	    Fallback: func(context.Context) error { page = cachedPage; return nil },
//	})
//	err := f.Run(ctx, func(ctx context.Context) error { return fetchLive(ctx, &page) })
//
// When BOTH halves fail, the result is FallbackFailed carrying both messages as
// fields — never one of the two errors alone. Reporting only the fallback's
// failure would never say what it was covering for, and reporting only the
// primary's would never say that plan B was tried and also broke; either way
// the outcome stops being diagnosable. A nil Fallback is refused (ADR 0031).
//
// A cancelled context is the one exception, and it is reported as itself: when
// it is already dead after the primary the fallback does not run at all, and
// when it dies while the fallback runs, a failed fallback returns ctx.Err()
// rather than FallbackFailed — neither dependency was at fault. A fallback that
// succeeds despite a late cancellation still returns nil: the work was done.
//
// # Hedging — for IDEMPOTENT operations only
//
// [NewHedge] guards tail latency by duplicating a slow call: when an attempt
// has been outstanding for Delay, a second copy starts, and the first to
// succeed wins.
//
//	h := resilience.NewHedge(resilience.HedgeConfig{
//	    Idempotent:  true,               // asserted, not assumed — see below
//	    Delay:       80 * time.Millisecond,
//	    MaxHedges:   1,
//	    MaxInFlight: 32,                 // duplicates in flight, all calls
//	})
//
// This is the only policy here that runs an Operation CONCURRENTLY with itself,
// which makes it the only one that can corrupt rather than merely delay: a
// non-idempotent Operation hedged is a double charge, a double insert, a
// duplicate outbound message — and the policy reports one clean success. The
// SDK cannot detect idempotence, so [HedgeConfig].Idempotent makes the claim
// mandatory and in code: its zero value refuses the policy, so hedging is never
// reached by copying a config and dropping a field, and `grep -r 'Idempotent:'`
// enumerates every hedged call path in a codebase.
//
// The second hazard is load. A dependency that has gone slow makes every
// in-flight call want a duplicate at the same instant, so hedging can deepen
// the outage it was meant to hide. Delay and MaxInFlight are both refused when
// unset for that reason — a zero Delay duplicates every call, and an unbounded
// MaxInFlight lets the duplicates scale with the outage. Reaching MaxInFlight
// degrades the policy to no-hedging (the call proceeds on its first attempt);
// it never turns into a rejection. Hedging also fires on latency ONLY: an
// attempt that fails before Delay elapses ends the call with that error, since
// replaying a failure is [NewRetry]'s job, and composing NewRetry(NewHedge(op))
// is how you get both.
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

// FallbackConfig is the public alias for the fallback policy configuration.
type FallbackConfig = svcres.FallbackConfig

// HedgeConfig is the public alias for the hedging policy configuration.
type HedgeConfig = svcres.HedgeConfig

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
	// FallbackFailed is returned when the primary operation AND its fallback
	// both failed. Both messages travel as the "primary" and "fallback"
	// fields, so neither half of a double failure is lost and neither can
	// hijack the policy's own code.
	FallbackFailed = coreres.FallbackFailed
	// PolicyMisconfigured is returned by every call to a policy that was built
	// with a configuration it cannot honour — a non-positive RateLimiterConfig
	// .Rate, a non-positive NewTimeout duration, a nil FallbackConfig.Fallback,
	// an unasserted HedgeConfig.Idempotent, a non-positive HedgeConfig.Delay or
	// MaxInFlight. The operation is not run. Unlike the sentinels above it is
	// permanent, not transient: the fix is at the construction site, never a
	// retry (ADR 0031).
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

// NewTimeout returns a deadline-enforcing Runner. A non-positive d is refused:
// every call returns PolicyMisconfigured without running the operation, since
// any deadline chosen for the caller would be a guess (ADR 0031).
func NewTimeout(d time.Duration) Runner {
	//: delegate to the service constructor.
	return svcres.NewTimeout(d)
}

// NewFallback returns a Runner that runs cfg.Fallback when the guarded
// Operation fails, reporting success when it works. When both fail the result
// is FallbackFailed, carrying both messages — unless the context was cancelled,
// which is reported as ctx.Err(). A nil cfg.Fallback is refused.
func NewFallback(cfg FallbackConfig) Runner {
	//: delegate to the service constructor.
	return svcres.NewFallback(cfg)
}

// NewHedge returns a tail-latency Runner that DUPLICATES an Operation still
// outstanding after cfg.Delay and takes the first success. Correct only on an
// idempotent Operation, which cfg.Idempotent makes the caller assert: that
// field, cfg.Delay and cfg.MaxInFlight are all refused when unset (ADR 0031).
//
// Each copy runs on a goroutine of its own, so a panic in one is recovered
// there and re-raised by Run on the caller's goroutine with the ORIGINAL value
// — a recover comparing against http.ErrAbortHandler still matches — but with
// the stack of the re-raise site; the copy's own stack is the price of not
// ending the process. A losing copy's late panic is recovered and dropped.
func NewHedge(cfg HedgeConfig) Runner {
	//: delegate to the service constructor.
	return svcres.NewHedge(cfg)
}
