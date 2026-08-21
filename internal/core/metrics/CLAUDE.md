# internal/core/metrics/

## Purpose

Declares the **observability port** — the natural twin of the logger: instruments
(`Counter`/`Gauge`/`Histogram`), the `Meter` that mints + `Collect`s them, and
the `Exporter` contract + process-wide registry that ships a `SnapshotValue`
out. A core sibling admitted by **ADR 0027** (Phase-B wave). The in-memory meter
+ a stdlib text exporter live in `internal/service/metrics`; exporters
self-register via the registry (writer-registry model, ADR 0012).

Code range: `0.2.9.*` (ADR 0027). **v1 is label-free** (instruments keyed by name).

## Contents

| File | Surface |
|---|---|
| `counter.go` / `gauge.go` / `histogram.go` | the three instrument interfaces |
| `meter.go` | `Meter` (Counter/Gauge/Histogram + `Collect() SnapshotValue`) |
| `exporter.go` | `Exporter` + `ExporterName` + registry (`RegisterExporter`/`LookupExporter`/`AvailableExporters`/`Export`) |
| `histogram_value.go` | `HistogramValue` + `SnapshotValue` (exportable value types) |
| `codes.go` / `errors.go` | `0.2.9.*` (UNKNOWN_EXPORTER, EXPORT_FAILED, INSTRUMENT_KIND_CONFLICT, DUPLICATE_REGISTRATION) |

## Conventions

- **Instruments are multi-method interfaces** (Counter has Add+Inc; Histogram
  Record+RecordDuration) — domain-named, not `Adder`/`Recorder`.
- **`Collect` lives on `Meter`** (not a separate Collector) so every Meter is
  collectable without a second interface.
- **Exporter registry** mirrors the writer registry (`snapshot.Value`, idempotent
  Register, panic on conflict).
- A name reused across instrument kinds is a programmer error (`InstrumentKindConflict`).

## Do NOT

- Put meter/instrument bodies here — they live in `service/metrics`.
- Add labels without an ADR note (deferred).

## Verification

```
bazel test --config=race //internal/core/metrics:metrics_test
```
