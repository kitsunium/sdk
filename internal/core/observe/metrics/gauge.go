// Package metrics — the Gauge instrument.
package metrics

// Gauge is an instantaneous value that can go up or down.
type Gauge interface {
	// Set replaces the gauge's current value.
	Set(value float64)
	// Add adjusts the gauge by delta (may be negative).
	Add(delta float64)
}
