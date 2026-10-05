// Package scheduler is the public facade for the SDK's time-driven execution
// domain: a five-field POSIX cron parser, a fixed-interval schedule, and an
// engine that fires jobs on either.
//
//	sched := scheduler.New(scheduler.Config{
//	    OnResult: func(r scheduler.Result) { log.Info("job", "name", r.Name, "err", r.Err) },
//	})
//
//	nightly, err := scheduler.Parse("0 2 * * *") // 02:00 UTC, every day
//	if err != nil {
//	    return err // an unusable expression is refused HERE, not at 02:00
//	}
//	if err := sched.Add(scheduler.Entry{Name: "rollup", Schedule: nightly, Job: rollup}); err != nil {
//	    return err
//	}
//	err = sched.Run(ctx) // blocks until ctx is cancelled, then drains
//
// # What is refused, and when
//
// A [Scheduler] with no entries is legitimate — it runs empty and waits for
// its context, which is what a service with every job behind a feature flag
// looks like. An unusable ENTRY is not: an empty name, a nil Job or a nil
// Schedule is refused by [Scheduler].Add, and an unusable cron expression is
// refused by [Parse]. The two are different questions and the SDK answers them
// differently on purpose (ADR 0031).
//
// [Parse] refuses by name rather than by guessing. Six- and seven-field
// expressions (the seconds and year dialects), @reboot, @every, the Quartz
// operators L, W, # and ?, a step written over a single value, an inverted
// range, 7 for Sunday, and any expression that matches no date on the calendar
// — "0 0 30 2 *" — all come back as a typed error at construction. Fixed
// intervals are [Every], which is a different idea from a calendar expression
// and has a different name for that reason.
//
// # Time zones
//
// [Parse] evaluates in UTC. [ParseInLocation] takes any *time.Location, and
// refuses a nil one — nil is what an unchecked time.LoadLocation leaves you
// with, and reading it as UTC would silently run the job on a different clock.
// A named location needs the host's tz database or a blank import of
// time/tzdata in your binary; the SDK does not import it for you.
//
// Around a DST transition the rules are fixed and documented rather than
// emergent:
//
//   - Spring forward — a wall-clock time that does not exist that day does not
//     fire. "0 2 * * *" in a zone that skips 02:00-03:00 skips that day. It is
//     not shifted to 01:00 or 03:00: those are instants the expression does
//     not name.
//   - Fall back — a wall-clock time that happens twice fires once, at the
//     first occurrence.
//
// # Missed deadlines, and overlap
//
// The scheduler SKIPS and COUNTS. When the machine sleeps, or a run holds its
// slot past the next deadline, the job runs once for the most recent due
// instant and [Result].Missed reports how many older ones were dropped. It
// never replays them: a process that was down for a day would come back to a
// burst of stale work at the worst possible moment. Fires missed while the
// process was not running are invisible — this scheduler keeps no state across
// restarts.
//
// When a run has not returned by the next deadline, the default is to SKIP
// that fire and report it as [Result].Skipped. Queueing grows without bound
// behind a persistently slow job; running copies in parallel duplicates a side
// effect the SDK cannot know is safe. A caller who knows concurrent copies are
// safe says so with [Entry].AllowOverlap — an in-code assertion, like
// resilience.HedgeConfig.Idempotent, whose zero value is the safe answer.
//
// # What is guaranteed
//
// A job is never fired EARLY. Lateness has no upper bound: it is whatever the
// platform's timer resolution, the machine's load and the OS's scheduling
// impose, and on a machine that suspends it is unbounded. This is why cron's
// finest field here is the minute and why a seconds field is refused — a
// six-field expression would promise a resolution no GOOS in the SDK's support
// matrix delivers uniformly (ADR 0018).
//
// A failing job never stops the scheduler and never affects another entry. A
// PANICKING job is recovered, reported as [JobPanicked] in [Result].Err, and
// the scheduler keeps running — one job's bug must not take the process down.
// The [Config].OnResult hook is the caller's own code and is deliberately NOT
// recovered.
package scheduler
