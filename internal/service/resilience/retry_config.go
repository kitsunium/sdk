// Package resilience — retry policy configuration.
package resilience

import "time"

// RetryConfig parameterises NewRetry. A non-positive MaxAttempts clamps to 1; a
// Multiplier of 1 or below defaults to 2; a zero MaxDelay means no cap; a nil
// Retryable replays every error.
type RetryConfig struct {
	MaxAttempts int
	BaseDelay   time.Duration
	MaxDelay    time.Duration
	Multiplier  float64
	// Retryable classifies a non-nil operation error as replayable. It is
	// consulted once per failed attempt: false returns that error verbatim —
	// no backoff, no further attempt, no RetryExhausted relabel — so a
	// deterministic failure (policy refusal, HTTP 400, invalid input) surfaces
	// as itself at the first attempt instead of costing the whole budget. A
	// nil predicate replays every error, which is the pre-classifier
	// behaviour. BreakerConfig accepts the same shape, so one classifier
	// serves a nested Retry(Breaker(op)) stack.
	Retryable func(error) bool
}
