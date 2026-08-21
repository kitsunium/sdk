// Package resilience — circuit-breaker configuration.
package resilience

import (
	"time"

	"github.com/kitsunium/sdk/internal/kernel/clock"
)

// BreakerConfig parameterises NewCircuitBreaker. A non-positive FailureThreshold
// defaults to 5; a nil Clock defaults to clock.System.
type BreakerConfig struct {
	FailureThreshold int
	OpenDuration     time.Duration
	Clock            clock.Clock
}
