// Package resilience — token-bucket rate-limiter configuration.
package resilience

import "github.com/kitsunium/sdk/internal/kernel/clock"

// RateLimiterConfig parameterises NewRateLimiter. Burst is the bucket capacity
// (clamped to >= 1); a nil Clock defaults to System.
type RateLimiterConfig struct {
	// Rate is the sustained admission rate in tokens per second. It has no
	// default: a Rate that is not a finite positive number — zero, negative,
	// NaN or infinite — makes NewRateLimiter return a policy that refuses
	// every call with PolicyMisconfigured. Any rate the SDK picked on
	// the caller's behalf would be a guess at their requirement, and a caller
	// who silently received one would believe they were limited at the rate
	// they intended (ADR 0031). Contrast Burst, which does have an obvious
	// floor — admit at least one call — and is therefore clamped.
	Rate  float64
	Burst int
	Clock clock.Clock
}
