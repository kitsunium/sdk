//go:generate gomarkdoc --output README.md --repository.url https://github.com/kitsunium/sdk --repository.default-branch main --repository.path /pkg/v1/clock .

// Package clock is the public facade for the SDK's time port: the interfaces a
// caller injects a time source through, the production clock, and the manual
// clock a test drives by hand.
//
// # Why this package exists
//
// These types were already part of the public API before this package existed
// — they just could not be used. Every SDK configuration that takes a time
// source is a type alias onto an internal struct whose Clock field is typed
// [Clock] or [Timed], so the compiler has always named them at a consumer's
// call site:
//
//	cannot use &myClock{} (value of type *myClock) as clock.Timed value in
//	struct literal: *myClock does not implement clock.Timed
//	(wrong type for method NewTicker)
//	        have NewTicker(time.Duration) *myTicker
//	        want NewTicker(time.Duration) clock.Ticker
//
// The NewTimer and NewTicker methods of [Waiter] return [Timer] and
// [Ticker], and Go requires type identity — not an identical method set — in
// a method signature. A locally declared interface with the very same methods
// is a different named type, and the package declaring the real one sits
// behind the internal/ firewall. So [Timed] was a field every caller could
// see, that no caller outside this module could satisfy, and whose only
// reachable value was the wall clock.
//
// This package is aliases and nothing else. It adds no behaviour, changes no
// behaviour, and exists so that naming what the compiler already names is
// possible (ADR 0090).
//
// # Two interfaces, because there are two jobs
//
//   - [Clock] READS time — Now, Since. Code that stamps a record asks for this
//     and nothing more.
//   - [Waiter] WAITS for time — After, NewTimer, NewTicker, Sleep.
//   - [Timed] is the union, and what a scheduler, a timeout, a retry backoff or
//     a lease keepalive asks for.
//
// The split is why a two-method double still works where only timestamps are
// read: [Clock] is frozen at two methods, so a struct with Now and Since
// satisfies every SDK field typed that way, forever.
//
// # Two implementations
//
// [System] is the production value, a [Timed] delegating to package time. It
// is what every SDK configuration falls back to when its Clock field is left
// nil, so you never have to name it to get the normal behaviour.
//
// [ManualClock] is the test double: time moves only when you move it. It is
// how a timeout, a cron cadence or a lease expiry is asserted exactly, with no
// sleeping and no wall-clock tolerance.
//
//	mc := clock.NewManualClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
//
//	sched := scheduler.New(scheduler.Config{Clock: mc})
//	_ = sched.Add(scheduler.Entry{Name: "hourly", Schedule: every, Job: job})
//	go sched.Run(ctx)
//
//	mc.BlockUntil(1)        // the engine has armed its wait — no race, no sleep
//	mc.Advance(time.Hour)   // the hour passes instantly
//	<-fired                 // the job ran
//
// BlockUntil is the half that makes this deterministic rather than merely
// fast: it waits until n waits are armed, closing the race where a test
// advances the clock before the code under test has asked to be woken.
//
// # Zero and negative durations
//
// After, NewTimer and Sleep read a non-positive delay as "already elapsed" and
// deliver at once. That is the stdlib contract and it is total.
//
// NewTicker and Ticker.Reset REFUSE a non-positive period and panic, because
// a ticker has no meaningful zero period and any value the SDK substituted
// would be arbitrary (ADR 0031). [System] and [ManualClock] panic identically,
// with the same message, so the two stay interchangeable.
//
// # The method sets do not grow
//
// [Clock], [Waiter], [Timed], [Timer] and [Ticker] are frozen. Under ADR 0039
// a published interface is extended by a SIBLING interface discovered by type
// assertion, never by widening — Go interfaces are structural, so one added
// method breaks every hand-written double downstream at compile time with no
// deprecation window. Writing a double against these five is safe.
//
// [ManualClock] is a concrete type and carries no exported field, so it can
// only be built through [NewManualClock] and driven through its methods. That
// is deliberate: it means a future field costs no consumer anything, which is
// the expensive half of ADR 0040.
//
// # Relationship to testing/synctest
//
// The stdlib's testing/synctest replaces package time inside a bubble, so
// [System] already observes fake time there. The two mechanisms are
// complementary: synctest fakes time for everything in the bubble, while a
// [ManualClock] fakes it for exactly the component you handed it to, from an
// origin you chose. Reach for synctest when the code under test uses package
// time directly; reach for [ManualClock] when it takes a [Timed].
package clock
