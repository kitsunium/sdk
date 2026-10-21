// Package backoff_test — the three jitter modes (#482): additive, full and
// decorrelated, each held to its band.
package backoff_test

import (
	"math"
	"testing"
	"time"

	"github.com/kitsunium/sdk/internal/kernel/backoff"
)

// TestFullDrawsWithinZeroAndTheDelay pins Full: [0, delay], no wait for a
// non-positive delay, and no overflow at the longest duration.
func TestFullDrawsWithinZeroAndTheDelay(t *testing.T) {
	t.Parallel()
	for _, delay := range []time.Duration{0, -time.Second} {
		if got := backoff.Full(delay); got != 0 {
			t.Fatalf("Full(%v) = %v, want 0", delay, got)
		}
	}
	if got := backoff.Full(time.Nanosecond); got < 0 || got > time.Nanosecond {
		t.Fatalf("Full(1ns) = %v, want 0 or 1ns", got)
	}
	for range jitterDraws {
		if got := backoff.Full(time.Second); got < 0 || got > time.Second {
			t.Fatalf("Full(1s) = %v, want within [0, 1s]", got)
		}
		if got := backoff.Full(math.MaxInt64); got < 0 {
			t.Fatalf("Full(MaxInt64) = %v, want non-negative", got)
		}
	}
}

// TestDecorrelateDrawsFromBaseToThreeTimesThePrevious pins Decorrelate's band
// [base, min(ceiling, 3 × previous)], a previous below base read as base, and
// the ceiling held even when it is below base.
func TestDecorrelateDrawsFromBaseToThreeTimesThePrevious(t *testing.T) {
	t.Parallel()
	type tc struct {
		name                    string
		base, ceiling, previous time.Duration
		low, high               time.Duration
	}
	for _, c := range []tc{
		{"first wait", time.Second, 0, 0, time.Second, 3 * time.Second},
		{"previous below base", time.Second, 0, time.Millisecond, time.Second, 3 * time.Second},
		{"grown", time.Second, 0, 4 * time.Second, time.Second, 12 * time.Second},
		{"capped", time.Second, 5 * time.Second, 4 * time.Second, time.Second, 5 * time.Second},
		{"ceiling below base", time.Second, time.Millisecond, time.Second, time.Millisecond, time.Millisecond},
		{"no ceiling, huge previous", time.Second, 0, math.MaxInt64 / 2, time.Second, math.MaxInt64},
	} {
		for range jitterDraws {
			if got := backoff.Decorrelate(c.base, c.ceiling, c.previous); got < c.low || got > c.high {
				t.Fatalf("%s: Decorrelate = %v, want within [%v, %v]", c.name, got, c.low, c.high)
			}
		}
	}
	for _, base := range []time.Duration{0, -time.Second} {
		if got := backoff.Decorrelate(base, time.Minute, time.Second); got != 0 {
			t.Fatalf("Decorrelate(base %v) = %v, want no wait", base, got)
		}
	}
}

// TestValueDelayUnderEachJitterMode pins Delay's dispatch: additive is the
// curve plus Jitter (unchanged at zero), full lies below the curve, and
// decorrelated lies in [BaseDelay, 3 × the curve's wait for the attempt
// before]; a mode outside the three reads as additive.
func TestValueDelayUnderEachJitterMode(t *testing.T) {
	t.Parallel()
	curve := backoff.Value{BaseDelay: time.Second, MaxDelay: time.Minute}
	for attempt := 1; attempt <= 8; attempt++ {
		grown := curve.Delay(attempt)
		unknown := curve
		unknown.JitterMode = 7
		if got := unknown.Delay(attempt); got != grown {
			t.Fatalf("unknown mode, attempt %d: %v, want the additive %v", attempt, got, grown)
		}
		full := curve
		full.JitterMode, full.Jitter = backoff.JitterFull, 1
		decorrelated := curve
		decorrelated.JitterMode = backoff.JitterDecorrelated
		upper := min(3*curve.Delay(max(attempt-1, 1)), curve.MaxDelay)
		for range jitterDraws {
			if got := full.Delay(attempt); got < 0 || got > grown {
				t.Fatalf("full, attempt %d: %v, want within [0, %v]", attempt, got, grown)
			}
			if got := decorrelated.Delay(attempt); got < curve.BaseDelay || got > upper {
				t.Fatalf("decorrelated, attempt %d: %v, want within [%v, %v]", attempt, got, curve.BaseDelay, upper)
			}
		}
	}
}

// TestJitterModeRendersItsName pins the spellings a configuration dump reads.
func TestJitterModeRendersItsName(t *testing.T) {
	t.Parallel()
	for mode, want := range map[backoff.JitterMode]string{
		backoff.JitterAdditive: "additive", backoff.JitterFull: "full",
		backoff.JitterDecorrelated: "decorrelated", 9: "unknown",
	} {
		if got := mode.String(); got != want {
			t.Fatalf("JitterMode(%d).String() = %q, want %q", mode, got, want)
		}
	}
}
