// Package resilience — circuit-breaker configuration.
package resilience

import (
	"time"

	"github.com/kitsunium/sdk/internal/kernel/clock"
)

// BreakerConfig parameterises NewCircuitBreaker. A non-positive FailureThreshold
// defaults to 5; a non-positive OpenDuration defaults to 30s; a nil Clock
// defaults to clock.System; a nil Retryable counts every error as a failure.
type BreakerConfig struct {
	FailureThreshold int
	// OpenDuration is how long an Open breaker rejects calls before admitting
	// one HalfOpen trial. A non-positive value clamps to 30s: a zero cooldown
	// would let the very next call through, which is a breaker that never
	// rejects anything and so protects nothing while the caller believes it
	// does (ADR 0031).
	OpenDuration time.Duration
	Clock        clock.Clock
	// Retryable classifies a non-nil operation error as a dependency failure
	// worth counting against the breaker's health. false means the error is
	// deterministic — the caller's own fault, not the dependency's — so it is
	// returned verbatim and folded into neither the failure count nor the
	// success path: a stream of HTTP 400s can no longer trip the breaker, and
	// one of them cannot close a HalfOpen breaker either. A nil predicate
	// counts every error as a failure, which is the pre-classifier behaviour.
	// Shares its shape with RetryConfig.Retryable so one classifier serves a
	// nested Retry(Breaker(op)) stack.
	Retryable func(error) bool
}
