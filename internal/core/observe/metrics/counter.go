// Package metrics — range 0.2.9.* (ADR 0027 core/observe/metrics block), and
// range 0.3.45.*, the metrics engine's, declared here since ADR 0160.
//
// Package metrics declares the observability port of the SDK, shaped on the
// OpenTelemetry metrics DATA MODEL — the specification, never the library.
// Instruments (Counter, UpDownCounter, Gauge, Histogram and their observable
// counterparts), the Meter that mints + collects them, typed Attrs, aggregation
// Temporality, the producing Resource and the InstrumentationScope, plus the
// Exporter contract + registry that ships a SnapshotValue out. A core sibling
// admitted by ADR 0027 (Phase-B wave) and re-shaped by ADR 0044; the in-memory
// meter and the stdlib exporters live in internal/service/observe/metrics, and
// exporters self-register via the registry (writer-registry model, ADR 0012).
//
// A series is one instrument name plus one attribute set, and a Meter bounds
// how many series a name may hold.
//
// Package metrics — the sibling port that documents an instrument NAME.
//
// Package metrics — the sibling port that mints the non-monotonic sum.
package metrics
