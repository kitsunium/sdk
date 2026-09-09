// Package clock abstracts the passage of time so higher layers can inject a
// deterministic time source in tests instead of coupling to the wall clock.
//
// The package carries two independent capabilities behind two independent
// interfaces, and that separation is the load-bearing design decision:
//
//   - [Clock] READS time (Now, Since). It is frozen at two methods on purpose.
//     It is reachable from the published pkg module — pkg/v1/cache.Config is a
//     type alias whose Clock field has this type — so every method added to it
//     would break every hand-written double downstream, at compile time, with
//     no deprecation window.
//   - [Waiter] WAITS for time (After, NewTimer, NewTicker, Sleep). Code that
//     only stamps records keeps asking for a Clock; code that schedules,
//     retries, or times out asks for a [Timed], the union of the two.
//
// [System] is the production value: a Timed delegating to package time.
// [ManualClock] is the test double: a Timed whose time moves only when the
// test moves it, so a timeout, a retry backoff, or a ticker cadence can be
// asserted exactly, without sleeping and without a wall-clock tolerance.
//
// # Non-positive durations
//
// After, NewTimer and Sleep accept a non-positive delay and read it as
// "already elapsed": the value is delivered at once, Sleep returns at once.
// That is the stdlib contract, it is total, and it is the only sensible
// reading of a delay that has no future — a clamp needing no explanation
// (ADR 0031).
//
// NewTicker and Ticker.Reset REFUSE a non-positive period and panic. A ticker
// has no meaningful zero period, and any positive value this package invented
// instead would be arbitrary — the other half of ADR 0031. The panic is raised
// HERE, by requirePositivePeriod, with a message naming this package and the
// offending duration, and it is raised identically by [System] and by
// [ManualClock]: time.NewTicker's own panic never reaches a caller, so the two
// implementations stay interchangeable at the boundary that matters most.
//
// # Relationship to testing/synctest
//
// The stdlib's testing/synctest replaces package time inside a bubble, so
// [System] already observes fake time there — the two mechanisms are
// complementary, not redundant. Which one to reach for is documented in
// CLAUDE.md §"clock vs testing/synctest", and both halves of that comparison
// are pinned by synctest_external_test.go rather than asserted in prose.
package clock

import "time"

// Clock produces timestamps and durations.
// Implementations must be safe for concurrent use by multiple goroutines.
//
// This interface is DELIBERATELY not extended: see the package comment. New
// time capabilities land on [Waiter] and are consumed through [Timed].
type Clock interface {
	// Now returns the current instant.
	Now() time.Time
	// Since returns the elapsed duration between the given instant and Now.
	Since(t time.Time) time.Duration
}

// Timed is a [Clock] that also drives waiting. It is the interface production
// code asks for when it needs both halves — a scheduler, a timeout, a retry
// backoff — and the one both [System] and [ManualClock] satisfy.
type Timed interface {
	Clock
	Waiter
}
