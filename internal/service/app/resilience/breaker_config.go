package resilience

import (
	"time"

	coreres "github.com/kitsunium/sdk/internal/core/app/resilience"
	"github.com/kitsunium/sdk/internal/kernel/clock"
)

// BreakerConfig parameterises NewCircuitBreaker.
type BreakerConfig struct {
	FailureThreshold int
	// OpenDuration is how long an Open breaker rejects calls before admitting
	// one HalfOpen trial.
	OpenDuration time.Duration
	Clock        clock.Clock
	// Retryable classifies a non-nil operation error as a dependency failure.
	Retryable func(error) bool
	// Window, when set, opens the breaker on a rate rather than a streak.
	Window *BreakerWindowValue
	// OnStateChange, when set, is called on every change of phase.
	OnStateChange func(from coreres.BreakerPhase, to coreres.BreakerPhase)
	// OpenBackoff, when set, lengthens each opening while trials keep failing.
	OpenBackoff *BackoffValue
}

// BreakerWindowValue is a circuit breaker's sliding window.
type BreakerWindowValue struct {
	Size         time.Duration
	Buckets      int
	MinCalls     int
	FailureRatio float64
	SlowAfter    time.Duration
	SlowRatio    float64
}
