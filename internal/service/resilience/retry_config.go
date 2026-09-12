// Package resilience — retry policy configuration.
package resilience

import "time"

// RetryConfig parameterises NewRetry. A non-positive MaxAttempts clamps to 1; a
// Multiplier of 1 or below defaults to 2; a zero MaxDelay means no cap; a nil
// Retryable replays every error; a zero Jitter backs off deterministically.
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
	// Jitter widens each backoff by a random fraction of itself, drawn
	// uniformly from [0, Jitter) and ADDED to the computed delay. 0 — the zero
	// value — is the deterministic backoff this policy has always had, so an
	// existing caller's timing is unchanged. 0.5 is the common choice: it
	// desynchronises callers that failed together without letting the average
	// wait stop growing monotonically. Values outside [0, 1] clamp into it.
	//
	// It exists because deterministic backoff SYNCHRONISES the very callers it
	// is meant to spread. N clients that fail against one dependency at the
	// same instant retry at the same instant, N times over, and every retry
	// round lands as one burst on a dependency that is already struggling.
	// Jitter is what turns those bursts back into a distribution.
	//
	// It applies AFTER the MaxDelay cap, deliberately: the cap bounds the
	// growth, and jitter spreads callers around the bound rather than being
	// squeezed flat against it. The consequence is worth stating — WHEN a cap is
	// configured, the effective ceiling on one wait becomes MaxDelay * (1 +
	// Jitter), not MaxDelay. A zero MaxDelay is no cap at all, so there is no
	// ceiling to raise: the backoff grows geometrically and the jitter widens
	// whatever it reaches, bounded only by what a time.Duration can represent.
	Jitter float64
}
