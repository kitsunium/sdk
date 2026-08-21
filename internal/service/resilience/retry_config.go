// Package resilience — retry policy configuration.
package resilience

import "time"

// RetryConfig parameterises NewRetry. A non-positive MaxAttempts clamps to 1; a
// Multiplier of 1 or below defaults to 2; a zero MaxDelay means no cap.
type RetryConfig struct {
	MaxAttempts int
	BaseDelay   time.Duration
	MaxDelay    time.Duration
	Multiplier  float64
}
