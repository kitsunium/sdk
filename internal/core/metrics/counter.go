// Package metrics declares the observability port of the SDK: instruments
// (Counter, Gauge, Histogram), the Meter that mints + collects them, and the
// Exporter contract + registry that ships their values out. A core sibling
// admitted by ADR 0027 (Phase-B wave), the natural twin of the logger. The
// in-memory meter + a stdlib text exporter live in internal/service/metrics;
// exporters self-register via the registry (writer-registry model, ADR 0012).
// Instruments are keyed by name AND label set: one name plus one label set is
// one series, and a Meter bounds how many series a name may hold.
package metrics

// Counter is a monotonically-increasing cumulative instrument.
type Counter interface {
	// Add increments the counter by a non-negative delta.
	Add(delta int64)
	// Inc increments the counter by one (Add(1) shorthand).
	Inc()
}
