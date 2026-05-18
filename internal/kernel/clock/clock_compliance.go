// Package clock: clock_compliance.go hosts compile-time interface assertions
// per KTN-IFACE-ASSERT-PLACEMENT — keeping them out of the production
// source so the runtime binary carries no diagnostic-only declarations.
package clock

// Compile-time assertion that systemClock satisfies the Clock contract
// (KTN-INTERFACE-COMPILE-CHECK) — fails the build before any test runs if
// the interface and implementation drift apart.
var _ Clock = (*systemClock)(nil)
