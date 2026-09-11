// Package clock — hosts compile-time interface assertions
// — keeping them out of the production
// source so the runtime binary carries no diagnostic-only declarations.
package clock

// Compile-time assertions that every implementation satisfies the contract it
// claims — the build fails before any test runs if an interface and one of its
// implementations drift apart. Each clock is asserted against all three of
// Clock, Waiter and Timed on purpose: Clock is the frozen, downstream-visible
// half of the port, and losing it silently would be the one break this package
// is shaped to avoid.
var (
	_ Clock  = (*systemClock)(nil)
	_ Waiter = (*systemClock)(nil)
	_ Timed  = (*systemClock)(nil)
	_ Clock  = (*ManualClock)(nil)
	_ Waiter = (*ManualClock)(nil)
	_ Timed  = (*ManualClock)(nil)
	_ Timer  = (*systemTimer)(nil)
	_ Ticker = (*systemTicker)(nil)
	_ Timer  = (*manualTimer)(nil)
	_ Ticker = (*manualTicker)(nil)
)
