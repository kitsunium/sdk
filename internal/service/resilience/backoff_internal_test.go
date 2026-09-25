// Package resilience — the backoff curve's internals.
package resilience

import (
	"testing"
	"time"
)

// Test_grow pins that the growth stops multiplying once it reaches its bound.
//
// A supervised loop that has been failing for a week counts its failures in
// the hundreds of thousands; a growth that multiplied once per failure would
// pay for every one of them on every wait. The bound is reached after a few
// dozen doublings, and this asserts the loop leaves as soon as it is.
func Test_grow(t *testing.T) {
	t.Parallel()
	type tc struct {
		name       string
		base       time.Duration
		ceiling    time.Duration
		multiplier float64
		attempt    int
		want       time.Duration
	}
	tests := []tc{
		{"the first attempt is the base", time.Second, 0, 2, 1, time.Second},
		{"growth below the ceiling", time.Second, time.Minute, 2, 4, 8 * time.Second},
		{"the ceiling holds", time.Second, time.Minute, 2, 10, time.Minute},
		{"no ceiling is the longest duration", time.Second, 0, 2, 200, maxDuration},
		{"a zero base is no wait", 0, time.Minute, 2, 10, 0},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		if got := grow(c.base, c.ceiling, c.multiplier, c.attempt); got != c.want {
			t.Errorf("grow(%v, %v, %v, %d) = %v, want %v", c.base, c.ceiling, c.multiplier, c.attempt, got, c.want)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_retryRunner_backoffOverflow pins that the retry policy inherits the
// overflow fix through the shared curve rather than keeping its own copy: a
// runner built WITHOUT NewRetry, with a NaN multiplier NewRetry would have
// normalised, still gets a finite, non-negative wait.
func Test_retryRunner_backoffOverflow(t *testing.T) {
	t.Parallel()
	r := &retryRunner{cfg: RetryConfig{BaseDelay: time.Second, MaxDelay: time.Hour}}
	//: attempt 80 is past 2^63 nanoseconds at ×2 from one second.
	if got := r.backoff(80); got != time.Hour {
		t.Errorf("backoff(80) = %v, want the one-hour ceiling", got)
	}
	unbounded := &retryRunner{cfg: RetryConfig{BaseDelay: time.Second}}
	if got := unbounded.backoff(80); got != maxDuration {
		t.Errorf("backoff(80) without a ceiling = %v, want %v", got, maxDuration)
	}
}
