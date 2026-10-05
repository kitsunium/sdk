// Package metrics — the sibling port that registers asynchronous instruments.
//
// Package metrics — this signal's half of the shared attribute model
// (internal/core/observe/otel): the refusal an unusable set earns HERE, the Resource a
// Meter publishes, and the one attribute key only a meter ever writes.
//
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
//
// Package metrics — declares the sentinel *errs.Error values. Each var's name
// equals its errs.Define Reason in SCREAMING_SNAKE form.
//
// Package metrics — the Exporter contract + process-wide exporter registry.
//
// Package metrics — the Gauge instrument.
//
// Package metrics — the exportable gauge point: a sampled reading, which covers
// no window and therefore carries no temporality.
//
// Package metrics — the Histogram instrument.
//
// Package metrics — the exportable histogram point: an explicit-bucket
// distribution.
//
// Package metrics — the Meter: the frozen instrument factory + collector.
//
// Package metrics — the asynchronous (observable) instruments: a value read at
// collection time rather than written at observation time.
//
// Package metrics — the instrumentation scope this signal publishes.
//
// Package metrics — the exportable snapshot: the OTel payload hierarchy in Go.
//
// Package metrics — the exportable sum point: the shape a Counter and an
// UpDownCounter share. The metric envelope that tells them apart lives beside
// SnapshotValue, whose field it is.
//
// Package metrics — aggregation temporality, the OTel concept that says which
// window a reported number covers.
package metrics
