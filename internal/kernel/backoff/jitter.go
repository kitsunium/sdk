package backoff

import (
	"math/rand/v2"
	"time"
)

// decorrelatedFactor is how far above the previous wait a decorrelated draw
// may reach: the Builders' Library's `random_between(base, sleep * 3)`.
const decorrelatedFactor float64 = 3

// JitterMode is how a [Value] randomises its waits: [JitterAdditive] (the
// zero), [JitterFull] or [JitterDecorrelated].
type JitterMode uint8

// The three ways a [Value] lays its randomisation on the curve. The zero is
// the additive jitter the curve has always had, so a Value written before
// the mode existed waits exactly as it did (ADR 0031).
const (
	// JitterAdditive widens the grown wait by a random fraction of itself,
	// drawn from [0, Jitter) and ADDED: the average wait keeps growing with
	// the attempt. A zero Jitter is the deterministic curve. It is the zero
	// JitterMode.
	JitterAdditive JitterMode = iota
	// JitterFull draws the wait uniformly from [0, grown], the grown wait being
	// the curve held at MaxDelay: the "Full Jitter" of the Builders' Library,
	// which spreads callers that failed together the most widely of the three
	// and keeps the average at half the curve. Jitter is not read.
	JitterFull
	// JitterDecorrelated draws each wait from the previous one, as [Decorrelate]
	// does: uniformly from [BaseDelay, 3 × previous], held at MaxDelay — the
	// "Decorrelated Jitter" of the Builders' Library. A loop that keeps the
	// previous wait calls Decorrelate; [Value.Delay], which has no memory,
	// reads the curve's own previous step as the previous wait. Jitter is not
	// read.
	JitterDecorrelated
)

// String renders the mode for a log line or a configuration dump:
// "additive", "full", "decorrelated"; a value this package does not
// define renders "unknown".
func (m JitterMode) String() string {
	//: a closed set of three, plus the honest answer for anything else.
	switch m {
	//: the zero.
	case JitterAdditive:
		//: additive.
		return "additive"
	//: the whole wait drawn.
	case JitterFull:
		//: full.
		return "full"
	//: the wait drawn from the previous one.
	case JitterDecorrelated:
		//: decorrelated.
		return "decorrelated"
	//: a value this package never mints.
	default:
		//: name the absence rather than print a number.
		return "unknown"
	}
}

// Full returns a wait drawn uniformly from [0, delay]: the "Full Jitter" of
// the AWS Builders' Library, laid on a delay the caller already grew and
// held at its ceiling. A zero or negative delay is no wait.
func Full(delay time.Duration) time.Duration {
	//: nothing to spread.
	if delay <= 0 {
		//: no wait.
		return 0
	}
	//: rand.Int64N(n) draws from [0, n); n = delay+1 reaches delay itself,
	//: except at the longest duration, where it would overflow.
	if delay == maxDuration {
		//: [0, delay) — one nanosecond short of the bound, never past it.
		return time.Duration(rand.Int64N(int64(delay)))
	}
	//: uniform in [0, delay].
	return time.Duration(rand.Int64N(int64(delay) + 1))
}

// Decorrelate returns the next wait of a "Decorrelated Jitter" backoff (AWS
// Builders' Library): drawn uniformly from [base, 3 × previous], held at
// ceiling when one is set (a zero ceiling is none) and at the longest
// time.Duration otherwise. A previous wait below base is read as base, so
// the first wait of a loop is drawn from [base, 3 × base]; a non-positive
// base is no wait.
//
// It is the one variant whose wait depends on the previous wait rather
// than on the attempt, so the loop keeps that wait and hands it back: the
// bound grows with what was actually waited, and two callers that failed
// together drift apart instead of following one curve.
func Decorrelate(base, ceiling, previous time.Duration) time.Duration {
	//: nothing to start from: a zero base is an immediate retry.
	if base <= 0 {
		//: no wait.
		return 0
	}
	limit := maxDuration
	//: a configured ceiling is the tighter bound.
	if ceiling > 0 {
		limit = ceiling
	}
	//: a ceiling at or below the base holds every wait at the ceiling, as
	//: min(cap, random_between(base, …)) does.
	if base >= limit {
		//: held.
		return limit
	}
	//: a loop that has not waited yet starts from the base.
	previous = max(previous, base)
	//: the upper bound, compared in the float domain BEFORE any conversion,
	//: for the reason grow gives.
	upper := limit
	//: below the bound, the product converts exactly enough and in range.
	if bound := float64(previous) * decorrelatedFactor; bound < float64(limit) {
		upper = time.Duration(bound)
	}
	//: base < upper <= limit <= maxDuration and base > 0, so span+1 cannot
	//: overflow.
	span := int64(upper - base)
	//: uniform in [base, upper].
	return base + time.Duration(rand.Int64N(span+1))
}
