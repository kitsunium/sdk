// Package resilience_test — the public backoff as a caller computes it.
package resilience_test

import (
	"math"
	"testing"
	"time"

	svcres "github.com/kitsunium/sdk/internal/service/resilience"
)

// TestBackoffDelay pins the curve a supervised loop, an outbox and a workflow
// each used to compute by hand: one second, doubling, held at the ceiling.
//
// Those three copies agreed on "the n-th consecutive failure waits base ×
// 2^(n−1)" and on reading a count below one as the first failure, and this is
// the same curve NewRetry applies between attempts.
func TestBackoffDelay(t *testing.T) {
	t.Parallel()
	type tc struct {
		name    string
		backoff svcres.BackoffValue
		attempt int
		want    time.Duration
	}
	loop := svcres.BackoffValue{BaseDelay: time.Second, MaxDelay: time.Minute}
	tests := []tc{
		{"the first failure waits the base", loop, 1, time.Second},
		{"the second doubles it", loop, 2, 2 * time.Second},
		{"the sixth is thirty-two times the base", loop, 6, 32 * time.Second},
		{"the seventh is held at the ceiling", loop, 7, time.Minute},
		{"far past the ceiling it stays there", loop, 1000, time.Minute},
		//: a loop that asks before it has failed is not told to spin.
		{"a zero attempt is the first", loop, 0, time.Second},
		{"a negative attempt is the first", loop, -5, time.Second},
		{"the zero value waits nothing", svcres.BackoffValue{}, 3, 0},
		{"a negative base waits nothing", svcres.BackoffValue{BaseDelay: -time.Second}, 3, 0},
		{
			//: a flat multiplier would hammer at a fixed interval.
			name:    "a multiplier of one doubles",
			backoff: svcres.BackoffValue{BaseDelay: time.Second, Multiplier: 1},
			attempt: 3, want: 4 * time.Second,
		},
		{
			name:    "a NaN multiplier doubles",
			backoff: svcres.BackoffValue{BaseDelay: time.Second, Multiplier: math.NaN()},
			attempt: 3, want: 4 * time.Second,
		},
		{
			name:    "a custom multiplier is honoured",
			backoff: svcres.BackoffValue{BaseDelay: time.Second, Multiplier: 3},
			attempt: 3, want: 9 * time.Second,
		},
		{
			name:    "a base above the ceiling is held at it",
			backoff: svcres.BackoffValue{BaseDelay: time.Hour, MaxDelay: time.Minute},
			attempt: 1, want: time.Minute,
		},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		if got := c.backoff.Delay(c.attempt); got != c.want {
			t.Errorf("Delay(%d) = %v, want %v", c.attempt, got, c.want)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// TestBackoffNeverWrapsNegative pins the defect the shared curve fixed on the
// way in. The retry policy grew its delay as a float64 and converted it to a
// time.Duration unchecked; past 2^63 nanoseconds Go leaves that conversion
// implementation-defined, and on amd64 it yields math.MinInt64. A negative
// wait fires at once, so a budget long enough to overflow stopped backing off
// at exactly the attempt where the wait was meant to be longest — and the
// ceiling did not save it, because a negative number is below every ceiling.
func TestBackoffNeverWrapsNegative(t *testing.T) {
	t.Parallel()
	type tc struct {
		name    string
		backoff svcres.BackoffValue
		attempt int
		want    time.Duration
	}
	unbounded := svcres.BackoffValue{BaseDelay: time.Second}
	capped := svcres.BackoffValue{BaseDelay: time.Second, MaxDelay: 5 * time.Minute}
	tests := []tc{
		{"the attempt that crosses 2^63 without a ceiling", unbounded, 64, math.MaxInt64},
		{"far past it without a ceiling", unbounded, 1 << 20, math.MaxInt64},
		{"the largest attempt without a ceiling", unbounded, math.MaxInt, math.MaxInt64},
		{"the attempt that crosses 2^63 under a ceiling", capped, 64, 5 * time.Minute},
		{"the largest attempt under a ceiling", capped, math.MaxInt, 5 * time.Minute},
		{
			name:    "an infinite multiplier under a ceiling",
			backoff: svcres.BackoffValue{BaseDelay: time.Second, MaxDelay: time.Minute, Multiplier: math.Inf(1)},
			attempt: 2, want: time.Minute,
		},
		{
			name:    "an infinite multiplier without one",
			backoff: svcres.BackoffValue{BaseDelay: time.Second, Multiplier: math.Inf(1)},
			attempt: 2, want: math.MaxInt64,
		},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		if got := c.backoff.Delay(c.attempt); got != c.want {
			t.Errorf("Delay(%d) = %v, want %v", c.attempt, got, c.want)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// TestBackoffJitterStaysInItsBand pins that Jitter only ever WIDENS a wait,
// within [delay, delay × (1 + Jitter)), and that an out-of-range value is
// clamped rather than trusted.
func TestBackoffJitterStaysInItsBand(t *testing.T) {
	t.Parallel()
	const draws int = 500
	type tc struct {
		name   string
		jitter float64
		lo, hi time.Duration
	}
	tests := []tc{
		{"half the delay", 0.5, time.Second, 1500 * time.Millisecond},
		{"above one clamps to one", 7, time.Second, 2 * time.Second},
		{"negative clamps to none", -1, time.Second, time.Second},
		{"NaN is none", math.NaN(), time.Second, time.Second},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		b := svcres.BackoffValue{BaseDelay: time.Second, Jitter: c.jitter}
		//: many draws, because one sample from a uniform proves nothing about
		//: its bounds.
		for range draws {
			got := b.Delay(1)
			if got < c.lo || got > c.hi {
				t.Fatalf("Delay(1) with Jitter=%v = %v, want within [%v, %v]", c.jitter, got, c.lo, c.hi)
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
