// Package resilience — the exponential backoff every waiting policy computes.
package resilience

import (
	"math"
	"math/rand/v2"
	"time"
)

// maxDuration is the longest wait a time.Duration can represent. A backoff
// that grows past it is held there rather than converted: Go leaves the
// conversion of an out-of-range float64 to an integer implementation-defined,
// and on amd64 it yields math.MinInt64 — a NEGATIVE wait, which a timer fires
// at once, abolishing the backoff at exactly the attempt where it is longest.
const maxDuration time.Duration = math.MaxInt64

// BackoffValue is an exponential backoff: the wait after the n-th consecutive
// failure is BaseDelay × Multiplier^(n−1), held at MaxDelay when one is set and
// widened by Jitter when one is asked for.
//
// It is the policy NewRetry has always applied between attempts, published so
// a loop that retries on its own terms — a supervised goroutine, an outbox, a
// state machine's failed transition — computes the same curve instead of
// writing a fourth copy of it. RetryConfig keeps its four fields of the same
// names; NewRetry builds one of these from them.
//
// The zero value is usable and waits nothing: a zero BaseDelay retries at
// once, which is what RetryConfig has always meant by it. Every other field is
// normalised on each call, so no value can make Delay misbehave:
//
//   - a Multiplier of 1 or below, or NaN, grows by 2 — a flat or shrinking
//     "exponential" backoff is a fixed-interval hammer on a dependency that is
//     already struggling;
//   - a zero MaxDelay is no ceiling, and the growth then stops at the longest
//     time.Duration rather than wrapping negative;
//   - a Jitter outside [0, 1], or NaN, is clamped into it.
type BackoffValue struct {
	// BaseDelay is the wait after the first failure. Zero or negative waits
	// nothing.
	BaseDelay time.Duration
	// MaxDelay is the ceiling on the grown delay. Zero means no ceiling.
	MaxDelay time.Duration
	// Multiplier is the growth factor between consecutive waits. 1 or below,
	// and NaN, mean 2.
	Multiplier float64
	// Jitter widens each wait by a uniform random fraction of itself, drawn
	// from [0, Jitter) and ADDED, after the MaxDelay ceiling — so when a
	// ceiling is set a wait may reach MaxDelay × (1 + Jitter). Zero is the
	// deterministic backoff. See RetryConfig.Jitter for why it exists.
	Jitter float64
}

// Delay returns the wait after the attempt-th consecutive failure. attempt
// counts from 1; a smaller value is read as 1, so a loop that has not failed
// yet and asks anyway waits BaseDelay rather than nothing.
//
// It never returns a negative duration, and it costs at most as many
// multiplications as it takes the delay to reach its ceiling, however large
// attempt is.
func (b BackoffValue) Delay(attempt int) time.Duration {
	//: the growth first, as a pure function of the attempt; the randomisation
	//: second, so the curve can be asserted without it.
	return widen(grow(b.BaseDelay, b.MaxDelay, normalMultiplier(b.Multiplier), attempt), normalJitter(b.Jitter))
}

// normalMultiplier resolves a growth factor that would not grow to the
// doubling default.
func normalMultiplier(multiplier float64) float64 {
	//: NaN first: it compares false against every bound, so `<= 1` alone would
	//: let it through and poison every product it touches.
	if math.IsNaN(multiplier) || multiplier <= 1 {
		//: standard exponential doubling.
		return defaultMultiplier
	}
	//: the caller's own factor, +Inf included — grow holds it at the ceiling.
	return multiplier
}

// normalJitter clamps a jitter fraction into [0, 1], reading NaN as none.
func normalJitter(jitter float64) float64 {
	//: min/max propagate NaN, so it is resolved before them.
	if math.IsNaN(jitter) {
		//: an unspecifiable width is no width.
		return noJitter
	}
	//: wider than the delay is a randomised wait, not a jittered backoff.
	return min(max(jitter, noJitter), maxJitter)
}

// grow computes base × multiplier^(attempt−1), held at ceiling when one is set
// and at maxDuration otherwise. multiplier is already normalised (> 1, or +Inf).
func grow(base, ceiling time.Duration, multiplier float64, attempt int) time.Duration {
	//: nothing to grow from: a zero base is an immediate retry.
	if base <= 0 {
		//: no wait.
		return 0
	}
	//: the bound the growth is compared against, as a float, so the comparison
	//: happens BEFORE any out-of-range conversion could.
	limit := float64(maxDuration)
	//: a configured ceiling is the tighter bound.
	if ceiling > 0 {
		limit = float64(ceiling)
	}
	delay := float64(base)
	//: one multiplication per prior failure, stopping as soon as the bound is
	//: reached, so a loop that has failed a million times costs as little as
	//: one that has failed forty.
	for range attempt - 1 {
		//: +Inf is caught by the same comparison.
		if delay >= limit {
			break
		}
		delay *= multiplier
	}
	//: at or past the bound, including +Inf.
	if delay >= limit {
		//: the configured ceiling when there is one.
		if ceiling > 0 {
			//: held at the ceiling.
			return ceiling
		}
		//: otherwise the longest representable wait, never a wrapped one.
		return maxDuration
	}
	//: strictly below 2^63, so the conversion is exact enough and in range.
	return time.Duration(delay)
}

// widen adds a uniform random fraction of delay, drawn from [0, jitter), to
// delay. jitter is already clamped into [0, 1].
//
// It is separate from grow so the growth stays a PURE function of the
// attempt: the deterministic curve and the randomisation are two claims, and a
// suite that cannot assert the first without the second can assert neither.
func widen(delay time.Duration, jitter float64) time.Duration {
	//: the zero value is the deterministic backoff this package always had.
	if jitter <= noJitter || delay <= 0 {
		//: unchanged.
		return delay
	}
	//: jitter is at most 1, so the product never exceeds delay and the
	//: conversion is always in range.
	width := int64(float64(delay) * jitter)
	//: A widened wait must stay REPRESENTABLE. delay+width wraps negative past
	//: MaxInt64, and a timer fires a negative duration immediately — so the
	//: overflow would abolish the backoff at exactly the attempt where it is
	//: longest, which is the worst possible moment to stop waiting.
	if headroom := int64(maxDuration - delay); width > headroom {
		//: spread across what is left rather than past the end of the type.
		width = headroom
	}
	//: rand.Int64N panics on a non-positive bound, which a sub-nanosecond
	//: delay, a rounded-to-zero width or an exhausted headroom reaches.
	if width <= 0 {
		//: unchanged.
		return delay
	}
	//: uniform in [delay, delay+width) — additive, so the average wait keeps
	//: growing with the attempt rather than being pulled back toward zero.
	return delay + time.Duration(rand.Int64N(width))
}
