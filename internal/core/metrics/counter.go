// Package metrics declares the observability port of the SDK, shaped on the
// OpenTelemetry metrics DATA MODEL — the specification, never the library.
// Instruments (Counter, UpDownCounter, Gauge, Histogram and their observable
// counterparts), the Meter that mints + collects them, typed Attrs, aggregation
// Temporality, the producing Resource and the InstrumentationScope, plus the
// Exporter contract + registry that ships a SnapshotValue out. A core sibling
// admitted by ADR 0027 (Phase-B wave) and re-shaped by ADR 0044; the in-memory
// meter and the stdlib exporters live in internal/service/metrics, and
// exporters self-register via the registry (writer-registry model, ADR 0012).
//
// A series is one instrument name plus one attribute set, and a Meter bounds
// how many series a name may hold.
package metrics

// Counter is a MONOTONIC sum: it only ever increases. In the OTel data model
// its points land in a Sum carrying Monotonic = true, which is what tells a
// backend that a decrease is a restart rather than a measurement.
type Counter interface {
	// Add increments the counter by a non-negative delta. A non-positive
	// delta is ignored — a counter that could go down is an UpDownCounter,
	// and a backend reading this one is entitled to assume it cannot.
	Add(delta int64)
	// Inc increments the counter by one (Add(1) shorthand).
	Inc()
}
