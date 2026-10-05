package backoff

import (
	"math"
	"math/rand/v2"
	"time"
)

const (
	// maxDuration is the longest wait a time.Duration can represent. A backoff
	// that grows past it is held there rather than converted: Go leaves the
	// conversion of an out-of-range float64 to an integer
	// implementation-defined, and on amd64 it yields math.MinInt64 — a
	// NEGATIVE wait, which a timer fires at once, abolishing the backoff at
	// exactly the attempt where it is longest.
	maxDuration time.Duration = math.MaxInt64
	// defaultMultiplier is the growth factor a multiplier that would not grow
	// resolves to: standard exponential doubling.
	defaultMultiplier float64 = 2
	// noJitter is the deterministic backoff, and the zero value.
	noJitter float64 = 0
	// maxJitter is the ceiling on a jitter fraction. Above 1 the random
	// component would exceed the delay it widens, which is a different policy
	// (a randomised wait) wearing this one's name.
	maxJitter float64 = 1
)

// Delay returns the wait after the attempt-th consecutive failure: [Grow] for
// the curve, then [Widen] for the jitter. attempt counts from 1; a smaller
// value is read as 1, so a loop that has not failed yet and asks anyway waits
// BaseDelay rather than nothing.
//
// It never returns a negative duration, and it costs at most as many
// multiplications as it takes the delay to reach its ceiling, however large
// attempt is.
func (b Value) Delay(attempt int) time.Duration {
	//: the growth first, as a pure function of the attempt; the randomisation
	//: second, so the curve can be asserted without it.
	return Widen(Grow(b.BaseDelay, b.MaxDelay, b.Multiplier, attempt), b.Jitter)
}

// Grow returns the deterministic half of the curve: base × multiplier^(attempt−1),
// held at ceiling when one is set (a zero ceiling is none) and at the longest
// time.Duration otherwise. It is a PURE function of its arguments.
//
// multiplier is normalised as [NormalMultiplier] does, and attempt is read as
// at least 1, so no input makes it return a negative duration or loop past
// the few dozen multiplications that reach the bound.
func Grow(base, ceiling time.Duration, multiplier float64, attempt int) time.Duration {
	//: normalised here so the exported half is total; grow trusts its caller.
	return grow(base, ceiling, NormalMultiplier(multiplier), attempt)
}

// Widen returns delay widened by a uniform random fraction of itself, drawn
// from [0, jitter) and ADDED, so the average wait keeps growing with the
// attempt rather than being pulled back toward zero.
//
// jitter is normalised as [NormalJitter] does; zero hands delay back
// unchanged, and so does a delay that is zero, negative or too small to
// split. The widened wait stays representable: it is spread across what is
// left below the longest time.Duration rather than past it.
func Widen(delay time.Duration, jitter float64) time.Duration {
	//: normalised here so the exported half is total; widen trusts its caller.
	return widen(delay, NormalJitter(jitter))
}

// NormalMultiplier resolves a growth factor that would not grow — 1 or below,
// or NaN — to the doubling default, and returns any other factor unchanged,
// +Inf included: [Grow] holds an infinite growth at the ceiling.
func NormalMultiplier(multiplier float64) float64 {
	//: NaN first: it compares false against every bound, so `<= 1` alone would
	//: let it through and poison every product it touches.
	if math.IsNaN(multiplier) || multiplier <= 1 {
		//: standard exponential doubling.
		return defaultMultiplier
	}
	//: the caller's own factor, +Inf included — grow holds it at the ceiling.
	return multiplier
}

// NormalJitter clamps a jitter fraction into [0, 1], reading NaN as none.
//
// NaN is resolved FIRST because Go's min and max propagate it: min(max(NaN,
// 0), 1) is NaN, and NaN <= 0 is false, so it would sail past every later
// guard into a float-to-integer conversion Go leaves implementation-defined.
func NormalJitter(jitter float64) float64 {
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
	//: the zero value is the deterministic backoff.
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
