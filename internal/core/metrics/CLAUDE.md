# internal/core/metrics/

## Purpose

Declares the **observability port** — the natural twin of the logger: instruments
(`Counter`/`Gauge`/`Histogram`), the `Meter` that mints + `Collect`s them, and
the `Exporter` contract + process-wide registry that ships a `SnapshotValue`
out. A core sibling admitted by **ADR 0027** (Phase-B wave). The in-memory meter
+ a stdlib text exporter live in `internal/service/metrics`; exporters
self-register via the registry (writer-registry model, ADR 0012).

Code range: `0.2.9.*` (ADR 0027).

Instruments are keyed by name **and label set**: one name plus one label set is
one **series**, and a `Meter` is required to bound how many series a name may
hold. Labels were the deferred half of ADR 0027 §Deferred and are no longer
deferred; the Prometheus/OTLP exporters still are.

## Contents

| File | Surface |
|---|---|
| `counter.go` / `gauge.go` / `histogram.go` | the three instrument interfaces |
| `label.go` | `LabelValue` + the reserved `OverflowLabelKey`/`OverflowLabelValue` |
| `meter.go` | `Meter` (Counter/Gauge/Histogram, each variadic in `LabelValue`, + `Collect() SnapshotValue`) |
| `exporter.go` | `Exporter` + `ExporterName` + registry (`RegisterExporter`/`LookupExporter`/`AvailableExporters`/`Export`) |
| `histogram_value.go` | `CounterValue` / `GaugeValue` / `HistogramValue` / `SnapshotValue` (exportable value types) |
| `codes.go` / `errors.go` | `0.2.9.*` (UNKNOWN_EXPORTER, EXPORT_FAILED, INSTRUMENT_KIND_CONFLICT, INVALID_LABEL, DUPLICATE_REGISTRATION) |

## The snapshot shape, and why it is this one

```go
type SnapshotValue struct {
    Counters   map[string][]CounterValue    // instrument name -> its series
    Gauges     map[string][]GaugeValue
    Histograms map[string][]HistogramValue
}
```

**Name → its series**, not `series key → value`. Every wire format an exporter
targets groups by name first: Prometheus emits one `# HELP`/`# TYPE` header per
name followed by its series, OTLP nests data points inside one `Metric`. An
exporter walking this map writes its header once per key and its points from the
slice — no regrouping pass, no parsing of a composite key, no second index.

Two properties an exporter may rely on:

- **Each series slice is sorted** by label set (key, then value, shorter set
  first). A snapshot therefore renders byte-identically twice in a row given the
  same values, which is what makes exporter output diffable and its tests
  writable without a sort of their own.
- **Each `Labels` slice is sorted by `Key`**, and `nil` for the dimensionless
  series — so `len(Labels) == 0` is the "no braces" case rather than something
  to special-case per exporter.

`Labels` **aliases the meter's own copy** and must not be mutated. Cloning per
`Collect` would cost one allocation per series per scrape — i.e. it would scale
the cost of scraping with cardinality, the exact axis the bound exists to
contain. The meter never mutates a label set after creating the series, so a
reader is safe; a writer would corrupt every future snapshot.

## Conventions

- **Instruments are multi-method interfaces** (Counter has Add+Inc; Histogram
  Record+RecordDuration) — domain-named, not `Adder`/`Recorder`.
- **`Collect` lives on `Meter`** (not a separate Collector) so every Meter is
  collectable without a second interface.
- **Labels are variadic** on every instrument accessor. That makes the
  dimensionless series the zero-argument case, so every pre-label call site
  still compiles and still means what it meant.
- **A label KEY is structure, a label VALUE is data.** Keys are written at the
  call site and constant for the process; values vary per observation. That
  asymmetry is why an unusable key is a programmer error the implementation
  panics on (`InvalidLabel`), while an unbounded stream of values is a runtime
  condition the implementation must absorb.
- **A label set is a SET**: order is not identity, and a duplicate key is a
  refusal, not a last-one-wins merge.
- **Exporter registry** mirrors the writer registry (`snapshot.Value`, idempotent
  Register, panic on conflict).
- A name reused across instrument kinds is a programmer error (`InstrumentKindConflict`).

## Do NOT

- Put meter/instrument bodies here — they live in `service/metrics`.
- Mutate a `Labels` slice reached through a `SnapshotValue`. It is the meter's.
- Add an "unbounded cardinality" mode to the port. ADR 0031: a bound of zero is
  a clamp, never an inert policy.

## Verification

```
bazel test --config=race //internal/core/metrics:metrics_test
```
