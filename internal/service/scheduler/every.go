// Package scheduler — hosts Every, the fixed-interval Schedule.
package scheduler

import (
	"time"

	coresched "github.com/kitsunium/sdk/internal/core/scheduler"
)

// Every returns a [coresched.Schedule] due one period after each previous due
// instant. It exists because @every is refused by the cron parser: an interval
// is not a calendar expression, and pretending it is confuses two different
// ideas — "every 30 seconds" has no month, no weekday and no DST question,
// while "0 2 * * *" has all three.
//
// The period is measured from the previous DUE instant, not from the previous
// completion, so a slow job does not shift the cadence. When a run overruns
// its period the next fire is already late; the scheduler then applies the
// domain's missed-deadline rule (skip and count) rather than queueing.
//
// A non-positive period is refused ([InvalidInterval]) rather than clamped: a
// schedule due infinitely often is not a slow schedule, it is a busy loop, and
// no substitute value would be anything but a guess at the caller's intent
// (ADR 0031). Sub-minute periods are accepted here — unlike cron, which stops
// at the minute — but the timing guarantee is the domain's: not early, and
// late by however much the platform's timers and the machine's load impose.
func Every(period time.Duration) (schedule coresched.Schedule, err error) {
	//: a zero or negative period has no cadence to run at.
	if period <= 0 {
		//: refuse at construction; the caller's own duration is echoed.
		return nil, rejectInterval(period)
	}
	//: the closure holds only the period, so the Schedule stays pure and is
	//: safe to call from the scheduler's loop without further guarding.
	return func(after time.Time) (time.Time, bool) {
		//: strictly after `after`, because period > 0 — the contract the
		//: engine's missed-deadline loop relies on to terminate.
		return after.Add(period), true
	}, nil
}
