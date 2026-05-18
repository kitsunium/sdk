// Package clock — hosts compile-time interface assertions
// — keeping them out of the production
// source so the runtime binary carries no diagnostic-only declarations.
package clock

// Compile-time assertion that systemClock satisfies the Clock contract
// — fails the build before any test runs if
// the interface and implementation drift apart.
var _ Clock = (*systemClock)(nil)
