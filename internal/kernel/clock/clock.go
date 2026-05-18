// Package clock abstracts time.Now for testability so that higher layers can
// inject fake clocks in unit tests without coupling to the wall clock.
package clock

import "time"

// Clock produces timestamps and durations.
// Implementations must be safe for concurrent use by multiple goroutines.
type Clock interface {
	// Now returns the current instant.
	Now() time.Time
	// Since returns the elapsed duration between the given instant and Now.
	Since(t time.Time) time.Duration
}

// systemClock is the default Clock backed by the package time wall clock.
type systemClock struct{}

// Now returns the current wall-clock instant from package time.
func (systemClock) Now() time.Time {
	//: delegate to the standard library so the OS provides the timestamp.
	return time.Now()
}

// Since returns the elapsed duration between t and the current wall-clock time.
func (systemClock) Since(t time.Time) time.Duration {
	//: delegate to the standard library for monotonic-aware subtraction.
	return time.Since(t)
}

var (
	// System is the default Clock using the package time wall clock.
	System Clock = systemClock{}
)
