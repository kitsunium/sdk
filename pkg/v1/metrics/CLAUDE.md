# pkg/v1/metrics/

## Purpose

Public facade for the SDK observability domain (ADR 0027, re-shaped on the
OpenTelemetry data model by ADR 0044), the logger's twin. Aliases the
`Meter`/instrument/`Exporter` types, the typed `Attr`, `Temporality`,
`Resource`, `Scope`, the point/metric snapshot values and `MeterConfig`; exposes
`String`/`Bool`/`Int64`/`Float64`, `NewMeter`, `NewMeterWithConfig`, `Export`,
`RegisterExporter`, `NewTextExporter`, `NewPrometheusExporter`,
`AvailableExporters`, the overflow key and the sentinels. Both default exporters
register on import and write to **stderr** (ADR 0030) — importing a package must
not arm a writer on stdout, which the process may be using as a protocol
channel. Stdlib-only → dep-light; cross-OS.

**Zero OTel imports.** The model is a published specification; the code is this
SDK's. See `internal/core/metrics/CLAUDE.md` §Purpose and ADR 0044 §Decision 1.

Instruments are identified by name **and typed attribute set** (one pair = one
series), with a per-instrument cardinality bound whose excess folds into a
visible aggregated overflow series.

## Surface

| Symbol | Notes |
|---|---|
| `Meter` | the FROZEN port: Counter/Gauge/Histogram + Collect (ADR 0039) |
| `UpDownMeter` / `AsyncMeter` / `FullMeter` | the two sibling ports and their union — what `NewMeter` returns |
| `Counter` / `UpDownCounter` / `Gauge` / `Histogram` | instrument aliases; every accessor is variadic in `Attr` |
| `Attr` (= `AttrValue`) / `AttrKind` + `AttrKind*` | one TYPED dimension of a series |
| `String` / `Bool` / `Int64` / `Float64` | the only ways to build a usable `Attr` |
| `Temporality` + `Temporality*` | delta or cumulative, carried by the metric |
| `Resource` / `Scope` + `ServiceNameKey` / `UnknownService` / `DefaultScopeName` | who produced the payload, and what instrumented it |
| `Snapshot` (= `SnapshotValue`) | `{Resource, Scope, StartTime, Time, Sums/Gauges/Histograms map[name]…Metric}` — see below |
| `SumMetric` / `GaugeMetric` / `HistogramMetric` | the per-name envelopes |
| `SumPoint` / `GaugePoint` / `HistogramPoint` | the per-series points |
| `ObserveInt64` / `ObserveFloat64` / `Int64Callback` / `Float64Callback` | the observable func ports |
| `MeterConfig` / `DefaultMaxSeriesPerInstrument` | bound, temporality, resource, scope, clock |
| `OverflowAttrKey` | how a consumer RECOGNISES the folded series (its value is the bool `true`) |
| `Exporter` / `ExporterName` | exporter aliases |
| `NewMeter()` / `NewMeterWithConfig(cfg)` | in-memory `FullMeter` |
| `Export` / `RegisterExporter` / `NewTextExporter` / `NewPrometheusExporter` / `AvailableExporters` | registry verbs |
| `UnknownExporter` / `ExportFailed` / `InstrumentKindConflict` / `InvalidAttribute` / `InvalidTemporality` | sentinels |
| `InvalidMetricName` / `InvalidLabelName` / `ReservedLabelName` / `UnsupportedTemporality` | how a consumer RECOGNISES what the Prometheus connector cannot carry |

## Conventions

- **Type aliases, not new types**; constructors are thin delegations. The core
  layer spells the shapes `…Value` (its role-suffix convention); this package
  publishes the short names, exactly as `Snapshot` has always aliased
  `SnapshotValue`.
- **`NewMeter` returns `FullMeter`, and a `Meter` parameter still accepts it.**
  Widening a returned VALUE is safe; widening the interface is what ADR 0039
  forbids. `UpDownCounter` and the observables live on siblings for that reason.
- **An attribute's KIND is part of the series identity.** `String("v", "1")` and
  `Int64("v", 1)` are two series. The four constructors are the only way to
  build one; a struct literal leaves `AttrKindInvalid`, which is refused.
- **stdout is opt-in**: `NewTextExporter(name, os.Stdout)` binds it explicitly;
  neither registered exporter ever does (ADR 0030).
- **`Snapshot` groups by instrument name**, each name holding one metric
  envelope whose `Points` are sorted by attribute set — the shape a per-series
  exporter consumes without regrouping, and the shape an OTLP encoder walks
  without reconstruction. The `Attrs` slices alias the meter's own and must not
  be mutated.
- **Temporality is on the metric, and a gauge has none.** Unset resolves to
  cumulative; a cast value refuses (ADR 0031). Under `TemporalityDelta`,
  `Collect` CONSUMES the window it reports, so a delta meter has one reader.
- **A cardinality bound of zero clamps to the default** and never means
  unbounded (ADR 0031); there is no unbounded setting at all.
- **The overflow key is re-exported on purpose.** Detecting the fold is the
  whole reason it is visible rather than silent, so a consumer must be able to
  test for it without importing anything internal.
- **The Prometheus exporter is a deliberately LOSSY connector**, and the
  sentinels that say so are re-exported for the same reason the overflow key is.
  It refuses a delta snapshot (`UnsupportedTemporality`) and a name the format
  cannot spell (`InvalidMetricName`/`InvalidLabelName`/`ReservedLabelName`) —
  including an OTel-conventional DOTTED attribute key — rather than
  transliterating, which would merge distinct instruments silently. Full list of
  losses: `internal/service/metrics/CLAUDE.md` §What the Prometheus connector
  loses.
- **`NewPrometheusExporter` is the scrape path**; the registered `prometheus`
  name is a stderr diagnostic, like `text` (ADR 0030).
- The OTLP exporter is not here yet (ADR 0044 §Deferred); the snapshot shape
  exists so it can be written without reconstructing anything. The Prometheus
  **protobuf** format remains deferred to `third-party/` (ADR 0027).
- README generated by gomarkdoc; edit the `metrics.go` doc comment and
  regenerate with `cd pkg/v1 && GOWORK=off go generate ./metrics/...`.

## Do NOT

- Reimplement instruments here — the facade is aliases + wrappers.
- Import `go.opentelemetry.io/*`. Anywhere.
- Add a method to the `Meter` alias's underlying interface. Add a sibling.

## Verification

```
bazel test --config=race //pkg/v1/metrics:metrics_test
```
