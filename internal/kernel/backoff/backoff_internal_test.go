// Package backoff — the curve's internals.
package backoff

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

// Test_widen pins that the private half trusts its caller's clamp and still
// never hands back a value past the end of the type.
func Test_widen(t *testing.T) {
	t.Parallel()
	type tc struct {
		name   string
		delay  time.Duration
		jitter float64
		lo, hi time.Duration
	}
	tests := []tc{
		{"no jitter is the delay", time.Second, noJitter, time.Second, time.Second},
		{"the widest jitter at most doubles", time.Second, maxJitter, time.Second, 2 * time.Second},
		{"the top of the type has no headroom", maxDuration, maxJitter, maxDuration, maxDuration},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		//: many draws, because the randomness is what could escape.
		for range 200 {
			if got := widen(c.delay, c.jitter); got < c.lo || got > c.hi {
				t.Fatalf("widen(%v, %v) = %v, want within [%v, %v]", c.delay, c.jitter, got, c.lo, c.hi)
			}
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}
