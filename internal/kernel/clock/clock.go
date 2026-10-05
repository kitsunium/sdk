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
