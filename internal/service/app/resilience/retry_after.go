package resilience

import (
	"math"
	"strconv"
	"time"

	kerrs "github.com/kitsunium/sdk/internal/kernel/errs"
)

// retryAfterField is the errs field a failure carries to say how many seconds
// to wait before the next attempt. The OTLP sender and the guarded HTTP client
// write the same key, each spelled in its own package; their suites pin that
// RetryAfter reads what they write.
const retryAfterField string = "retry_after_seconds"

// floatBits is the width strconv parses a field's rendering at.
const floatBits int = 64

// RetryAfter reports the delay err says to wait before the next attempt,
// and whether it says one. It reads the retry_after_seconds field of the
// outermost *errs.Error in err's chain — a number of seconds, whole or
// fractional — and reports false when there is none, or when it is not a
// finite positive number.
//
// Three producers in the SDK set it: the OTLP/HTTP exporters on a
// retryable answer whose Retry-After header holds delta-seconds, the
// guarded HTTP client (net/client) on a 429 or a 503 whose Retry-After
// does the same, and RateLimited from NewRateLimiter and
// NewKeyedRateLimiter, with the time the bucket needs to earn its next
// token. An HTTP-date Retry-After is not read: converting it compares the
// peer's clock to ours. Any other error sets the same field with
// errs.Int or errs.Float to take part.
//
// Its shape is RetryConfig.Delay's, so honouring the delay is one line:
//
//	Delay: func(_ int, err error) (time.Duration, bool) { return RetryAfter(err) }
//
// A delay too long for a time.Duration reports the longest one.
func RetryAfter(err error) (time.Duration, bool) {
	fields := kerrs.FieldsOf(err)
	//: inner first, outer last: the newest word on the delay is the last one.
	for i := len(fields) - 1; i >= 0; i-- {
		//: only the one key means a delay.
		if fields[i].Key() != retryAfterField {
			//: another field.
			continue
		}
		//: the first one found from the outside decides, usable or not.
		return secondsToDuration(fields[i].StringValue())
	}
	//: the error names no delay.
	return 0, false
}

// secondsToDuration parses a field's rendering as a number of seconds and
// converts it to a Duration, reporting false for anything that is not a
// finite positive number.
func secondsToDuration(rendered string) (time.Duration, bool) {
	seconds, err := strconv.ParseFloat(rendered, floatBits)
	//: not a number, zero — the OTLP sender's "no hint" —, negative, NaN or ±Inf.
	if err != nil || math.IsNaN(seconds) || math.IsInf(seconds, 0) || seconds <= 0 {
		//: no usable delay.
		return 0, false
	}
	nanos := seconds * float64(time.Second)
	//: compared in the float domain BEFORE converting: past MaxInt64 the
	//: conversion is implementation-defined (ADR 0103).
	if nanos >= math.MaxInt64 {
		//: the longest wait a Duration holds.
		return time.Duration(math.MaxInt64), true
	}
	//: rounded to the nearest nanosecond, so 0.15 s rendered from a float
	//: product is 150 ms and not one nanosecond more; a positive delay below
	//: half a nanosecond is the shortest one, never zero.
	return time.Duration(max(math.Round(nanos), 1)), true
}
