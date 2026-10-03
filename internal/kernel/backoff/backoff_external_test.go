// Package backoff_test — the curve as a caller computes it.
package backoff_test

import (
	"math"
	"testing"
	"time"

	"github.com/kitsunium/sdk/internal/kernel/backoff"
)

// jitterDraws is how many samples each bounds assertion takes: enough that a
// uniform draw escaping its range is found, cheap enough for a unit test.
const jitterDraws int = 500

// TestValueDelay pins the curve a supervised loop, an outbox and a workflow
// each used to compute by hand: one second, doubling, held at the ceiling.
//
// Those three copies agreed on "the n-th consecutive failure waits base ×
// 2^(n−1)" and on reading a count below one as the first failure, and this is
// the same curve the retry policy waits between attempts.
func TestValueDelay(t *testing.T) {
	t.Parallel()
	type tc struct {
		name    string
		curve   backoff.Value
		attempt int
		want    time.Duration
	}
	loop := backoff.Value{BaseDelay: time.Second, MaxDelay: time.Minute}
	tests := []tc{
		{"the first failure waits the base", loop, 1, time.Second},
		{"the second doubles it", loop, 2, 2 * time.Second},
		{"the sixth is thirty-two times the base", loop, 6, 32 * time.Second},
		{"the seventh is held at the ceiling", loop, 7, time.Minute},
		{"far past the ceiling it stays there", loop, 1000, time.Minute},
		//: a loop that asks before it has failed is not told to spin.
		{"a zero attempt is the first", loop, 0, time.Second},
		{"a negative attempt is the first", loop, -5, time.Second},
		{"the zero value waits nothing", backoff.Value{}, 3, 0},
		{"a negative base waits nothing", backoff.Value{BaseDelay: -time.Second}, 3, 0},
		{
			//: a flat multiplier would hammer at a fixed interval.
			name:    "a multiplier of one doubles",
			curve:   backoff.Value{BaseDelay: time.Second, Multiplier: 1},
			attempt: 3, want: 4 * time.Second,
		},
		{
			name:    "a NaN multiplier doubles",
			curve:   backoff.Value{BaseDelay: time.Second, Multiplier: math.NaN()},
			attempt: 3, want: 4 * time.Second,
		},
		{
			name:    "a custom multiplier is honoured",
			curve:   backoff.Value{BaseDelay: time.Second, Multiplier: 3},
			attempt: 3, want: 9 * time.Second,
		},
		{
			name:    "a base above the ceiling is held at it",
			curve:   backoff.Value{BaseDelay: time.Hour, MaxDelay: time.Minute},
			attempt: 1, want: time.Minute,
		},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		if got := c.curve.Delay(c.attempt); got != c.want {
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

// TestValueDelayNeverWrapsNegative pins the defect the shared curve fixed on
// the way in. The retry policy grew its delay as a float64 and converted it to
// a time.Duration unchecked; past 2^63 nanoseconds Go leaves that conversion
// implementation-defined, and on amd64 it yields math.MinInt64. A negative
// wait fires at once, so a budget long enough to overflow stopped backing off
// at exactly the attempt where the wait was meant to be longest — and the
// ceiling did not save it, because a negative number is below every ceiling.
func TestValueDelayNeverWrapsNegative(t *testing.T) {
	t.Parallel()
	type tc struct {
		name    string
		curve   backoff.Value
		attempt int
		want    time.Duration
	}
	unbounded := backoff.Value{BaseDelay: time.Second}
	capped := backoff.Value{BaseDelay: time.Second, MaxDelay: 5 * time.Minute}
	tests := []tc{
		{"the attempt that crosses 2^63 without a ceiling", unbounded, 64, math.MaxInt64},
		{"far past it without a ceiling", unbounded, 1 << 20, math.MaxInt64},
		{"the largest attempt without a ceiling", unbounded, math.MaxInt, math.MaxInt64},
		{"the attempt that crosses 2^63 under a ceiling", capped, 64, 5 * time.Minute},
		{"the largest attempt under a ceiling", capped, math.MaxInt, 5 * time.Minute},
		{
			name:    "an infinite multiplier under a ceiling",
			curve:   backoff.Value{BaseDelay: time.Second, MaxDelay: time.Minute, Multiplier: math.Inf(1)},
			attempt: 2, want: time.Minute,
		},
		{
			name:    "an infinite multiplier without one",
			curve:   backoff.Value{BaseDelay: time.Second, Multiplier: math.Inf(1)},
			attempt: 2, want: math.MaxInt64,
		},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		if got := c.curve.Delay(c.attempt); got != c.want {
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

// TestValueDelayJitterStaysInItsBand pins that Jitter only ever WIDENS a wait,
// within [delay, delay × (1 + Jitter)), and that an out-of-range value is
// clamped rather than trusted.
func TestValueDelayJitterStaysInItsBand(t *testing.T) {
	t.Parallel()
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
		b := backoff.Value{BaseDelay: time.Second, Jitter: c.jitter}
		//: many draws, because one sample from a uniform proves nothing about
		//: its bounds.
		for range jitterDraws {
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

// TestGrow pins the deterministic half on its own, with inputs Value.Delay
// never hands it: an un-normalised multiplier must still double, because Grow
// is exported and a caller may pass anything.
func TestGrow(t *testing.T) {
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
		{"no ceiling is the longest duration", time.Second, 0, 2, 200, math.MaxInt64},
		{"a zero base is no wait", 0, time.Minute, 2, 10, 0},
		{"a shrinking multiplier doubles", time.Second, 0, 0.5, 3, 4 * time.Second},
		{"a NaN multiplier doubles", time.Second, 0, math.NaN(), 3, 4 * time.Second},
		{"a zero attempt is the first", time.Second, 0, 2, 0, time.Second},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		if got := backoff.Grow(c.base, c.ceiling, c.multiplier, c.attempt); got != c.want {
			t.Errorf("Grow(%v, %v, %v, %d) = %v, want %v", c.base, c.ceiling, c.multiplier, c.attempt, got, c.want)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// TestWiden pins the random half: it only ever widens, within [delay, delay ×
// (1 + jitter)), and the inputs that would reach an operation with no defined
// answer — a NaN width, a delay at the top of the type, a delay too small to
// split — come back representable and non-negative.
func TestWiden(t *testing.T) {
	t.Parallel()
	type tc struct {
		name   string
		delay  time.Duration
		jitter float64
		lo, hi time.Duration
	}
	tests := []tc{
		{"zero jitter is the deterministic wait", time.Second, 0, time.Second, time.Second},
		{"half the delay", time.Second, 0.5, time.Second, 1500 * time.Millisecond},
		{"above one clamps to one", time.Second, 99, time.Second, 2 * time.Second},
		{"negative clamps to none", time.Second, -1, time.Second, time.Second},
		//: Go's min/max PROPAGATE NaN: it must be normalised, not clamped.
		{"NaN is none", time.Second, math.NaN(), time.Second, time.Second},
		//: rand.Int64N panics on a non-positive bound, which a one-nanosecond
		//: delay reaches by rounding the width to zero.
		{"a delay too small to split", 1, 0.5, 1, 1},
		{"a zero delay", 0, 0.5, 0, 0},
		{"a negative delay is handed back untouched", -time.Second, 0.5, -time.Second, -time.Second},
		//: delay+width wraps negative past MaxInt64.
		{"a delay at the top of the type stays representable", math.MaxInt64, 0.5, math.MaxInt64, math.MaxInt64},
		{"a delay just under the top spreads across what is left", math.MaxInt64 - 1000, 0.5, math.MaxInt64 - 1000, math.MaxInt64},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		//: many draws, because the randomness is what could escape.
		for range jitterDraws {
			got := backoff.Widen(c.delay, c.jitter)
			if got < c.lo || got > c.hi {
				t.Fatalf("Widen(%v, %v) = %v, want within [%v, %v]", c.delay, c.jitter, got, c.lo, c.hi)
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

// TestNormalMultiplier pins the resolution of a factor that would not grow.
func TestNormalMultiplier(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		in   float64
		want float64
	}
	tests := []tc{
		{"one doubles", 1, 2},
		{"below one doubles", 0.5, 2},
		{"zero doubles", 0, 2},
		{"negative doubles", -3, 2},
		{"a growing factor is kept", 3, 3},
		{"infinity is kept, Grow holds it at the ceiling", math.Inf(1), math.Inf(1)},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		if got := backoff.NormalMultiplier(c.in); got != c.want {
			t.Errorf("NormalMultiplier(%v) = %v, want %v", c.in, got, c.want)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
	//: NaN compares unequal to itself, so it gets its own assertion.
	if got := backoff.NormalMultiplier(math.NaN()); got != 2 {
		t.Errorf("NormalMultiplier(NaN) = %v, want 2", got)
	}
}

// TestNormalJitter pins the clamp, and that NaN — which min and max propagate —
// becomes no jitter rather than surviving the clamp.
func TestNormalJitter(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		in   float64
		want float64
	}
	tests := []tc{
		{"zero is none", 0, 0},
		{"a fraction is kept", 0.25, 0.25},
		{"one is kept", 1, 1},
		{"above one clamps to one", 7, 1},
		{"negative clamps to none", -1, 0},
		{"NaN is none", math.NaN(), 0},
		{"infinity clamps to one", math.Inf(1), 1},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		got := backoff.NormalJitter(c.in)
		if math.IsNaN(got) || got != c.want {
			t.Errorf("NormalJitter(%v) = %v, want %v", c.in, got, c.want)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}
