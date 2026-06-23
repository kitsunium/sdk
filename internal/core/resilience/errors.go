// Package resilience — declares the sentinel *errs.Error policy outcomes. Each
// var's name equals its errs.Define Reason in SCREAMING_SNAKE form.
package resilience

import "github.com/kitsunium/sdk/internal/kernel/errs"

// exitUnavailable matches sysexits EX_TEMPFAIL (75) — a resilience rejection is a
// transient unavailability, not a generic internal fault.
const exitUnavailable int = 75

var (
	// RetryExhausted wraps the final attempt's error after the retry budget ran out.
	RetryExhausted = errs.Define(CodeRetryExhausted, "RETRY_EXHAUSTED",
		"The operation failed after exhausting all retries",
		"service/resilience: retry budget exhausted; the wrap cause is the last attempt's error",
		errs.WithExitCode(exitUnavailable))

	// CircuitOpen is returned when the breaker is Open and the call is rejected fast.
	CircuitOpen = errs.Define(CodeCircuitOpen, "CIRCUIT_OPEN",
		"The circuit breaker is open; the call was rejected",
		"service/resilience: breaker in Open state — downstream presumed unhealthy",
		errs.WithExitCode(exitUnavailable))

	// RateLimited is returned when no token was available (or the wait was cancelled).
	RateLimited = errs.Define(CodeRateLimited, "RATE_LIMITED",
		"The call was rejected by the rate limiter",
		"service/resilience: no rate-limit token available",
		errs.WithExitCode(exitUnavailable))

	// BulkheadFull is returned when every concurrency slot is occupied.
	BulkheadFull = errs.Define(CodeBulkheadFull, "BULKHEAD_FULL",
		"The call was rejected because the bulkhead is full",
		"service/resilience: all bulkhead concurrency slots occupied",
		errs.WithExitCode(exitUnavailable))

	// TimeoutExceeded is returned when the operation outran the timeout deadline.
	TimeoutExceeded = errs.Define(CodeTimeoutExceeded, "TIMEOUT_EXCEEDED",
		"The operation exceeded its timeout",
		"service/resilience: operation did not complete before the deadline",
		errs.WithExitCode(exitUnavailable))
)
