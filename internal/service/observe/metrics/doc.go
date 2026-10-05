// Package metrics — one instrument name's slot in a collection arena.
//
// Package metrics — the Meter's configuration: cardinality bound, aggregation
// temporality, producing Resource, instrumentation Scope and clock.
//
// Package metrics — OTLP/HTTP emitter: the exporter that POSTs an encoded
// snapshot to a collector. The transport itself — endpoint refusal, the default
// client and its own pool, the bounded reads, the classification — is the one
// both signals share (internal/service/observe/internal/otlp); what is this signal's is
// the path, the vocabulary every verdict is named in, and the encoder.
//
// Package metrics — OTLP/JSON encoder: SnapshotValue to the bytes an OTLP
// receiver accepts, implemented from the specification with encoding/json.
//
// Package metrics — Prometheus text exposition connector (format 0.0.4).
//
// Package metrics — stdlib text Exporter (one series per line, under a header
// per instrument name).
//
// Package metrics — atomic float64 gauge.
//
// Package metrics — atomic bucketed histogram.
//
// Package metrics — the in-memory Meter (a core/observe/metrics.FullMeter).
//
// Package metrics — the description a Meter attaches to an instrument NAME.
//
// Package metrics — the asynchronous instruments: registration, and the read
// that happens once per Collect.
//
// Package metrics — the qualifier set of one text-exporter "# metric" line.
//
// Package metrics — per-instrument-name bookkeeping: kind binding and the
// cardinality tally.
//
// Package metrics — the one member of a collector's answer that is this
// signal's own: the count an ExportMetricsServiceResponse reports rejected.
//
// Package metrics — the OTLP payload tree: a Go mirror of
// opentelemetry/proto/{collector/metrics,metrics}/v1, restricted to the fields
// this SDK produces. The common/v1 and resource/v1 messages it embeds —
// KeyValue, AnyValue, Resource, InstrumentationScope — and the proto3-JSON
// scalars are the ones both signals share, in internal/service/observe/internal/otlp.
//
// Field ORDER inside each struct is the schema's FIELD-NUMBER order, not a
// reading order: encoding/json emits struct fields as declared, and deriving
// the order from the document is what makes the expected bytes in the tests
// checkable against the .proto field by field. The visible evidence that the
// order came from the schema rather than from taste is otlpNumberDataPoint,
// which puts attributes AFTER the value because it is field 7 — it replaced a
// long-removed labels field at 1.
//
// Every field this SDK does not produce is ABSENT rather than always-empty
// (rule 5): schemaUrl, unit, flags, exemplars, droppedAttributesCount, and a
// histogram point's min/max. `description` joined the produced set in ADR 0067
// and is omitted only when the metric carries none — see otlpMetric.
//
// Package metrics — the OTLP/HTTP exporter's configuration.
//
// Package metrics — series identity: the canonical key binding an instrument
// name and an attribute set to exactly one series.
//
// Package metrics — one live series.
//
// Package metrics — one snapshot group's series map.
//
// Package metrics provides the in-memory Meter + lock-free instruments
// (Counter, UpDownCounter, Gauge, Histogram and their observable counterparts)
// implementing core/observe/metrics, plus two stdlib Exporters. Instruments are atomic
// and lock-free on the hot path; the Meter takes only a read lock to resolve an
// existing series and serialises creation alone. A series is one instrument
// name plus one TYPED attribute set, with a per-name cardinality bound that
// folds the excess into one aggregated overflow series. ADR 0027 / ADR 0044.
// Cross-OS: 100% portable.
package metrics
