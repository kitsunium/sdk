// Package resilience — token-bucket rate-limiter configuration.
package resilience

import "github.com/kitsunium/sdk/internal/kernel/clock"

// RateLimiterConfig parameterises NewRateLimiter. Rate is tokens-per-second;
// Burst is the bucket capacity (clamped to >= 1); a nil Clock defaults to System.
type RateLimiterConfig struct {
	Rate  float64
	Burst int
	Clock clock.Clock
}
