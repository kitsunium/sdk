<!-- updated: 2026-10-05T12:00:00Z -->
# pkg/v1/observe/metrics/

## Purpose

Public facade for the SDK observability domain (ADR 0027, re-shaped on the
OpenTelemetry data model by ADR 0044, given a `Describer` by ADR 0067), the
logger's twin. Aliases the `Meter`/instrument/`Describer`/`Exporter` types, the
typed `Attr`, `Temporality`, `Resource`, `Scope`, the point/metric snapshot
values and `MeterConfig`; exposes
`String`/`Bool`/`Int64`/`Float64`, `NewMeter`, `NewMeterWithConfig`, `Export`,
`RegisterExporter`, `NewTextExporter`, `NewPrometheusExporter`,
`EncodeOTLPJSON`, `NewOTLPJSONExporter`, `NewOTLPHTTPExporter`, `OTLPRetryable`,
`AvailableExporters`, the overflow key and the sentinels. All three default
exporters register on import and write to **stderr** (ADR 0030) — importing a
package must not arm a writer on stdout, which the process may be using as a
protocol channel — and the OTLP/HTTP emitter is not registered at all, because
an import must not arm a network client either (ADR 0048). Stdlib-only →
dep-light; cross-OS.

**Zero OTel imports.** The model is a published specification; the code is this
SDK's. See `internal/core/observe/metrics/CLAUDE.md` §Purpose and ADR 0044 §Decision 1.

Every sentinel aliases `internal/core/observe/metrics`, the engine's wire
refusals (`0.3.45.*`) included: they are declared there since ADR 0160 §2, and an
alias points at the layer that owns its symbol (ADR 0074). The constructors and
exporters still delegate to `internal/service/observe/metrics`.

Instruments are identified by name **and typed attribute set** (one pair = one
series), with a per-instrument cardinality bound whose excess folds into a
visible aggregated overflow series.

## Surface

| Symbol | Notes |
|---|---|
| `Meter` | the FROZEN port: Counter/Gauge/Histogram + Collect (ADR 0039) |
| `UpDownMeter` / `AsyncMeter` / `FullMeter` | the two INSTRUMENT sibling ports and their union — what `NewMeter` returns |
| `Describer` | the third sibling: `Describe(name, description string)`. Deliberately NOT in `FullMeter` — reach it by type assertion, and read the false case as "this meter records no description" |
| `Counter` / `UpDownCounter` / `Gauge` / `Histogram` | instrument aliases; every accessor is variadic in `Attr` |
| `Attr` (= `internal/core/observe/otel.AttrValue`) / `AttrKind` + `AttrKind*` | one TYPED dimension of a series — the same type `pkg/v1/observe/trace.Attr` is |
| `String` / `Bool` / `Int64` / `Float64` | the only ways to build a usable `Attr` |
| `Temporality` + `Temporality*` | delta or cumulative, carried by the metric |
| `Resource` / `Scope` + `ServiceNameKey` / `UnknownService` / `DefaultScopeName` | who produced the payload, and what instrumented it. The two types alias `internal/core/observe/otel`'s, shared with `pkg/v1/observe/trace`; neither carries a `Normalized()` method any more (a v0 shape change, ADR 0040) — the meter normalises them with this signal's code and default |
| `Snapshot` (= `SnapshotValue`) | `{Resource, Scope, StartTime, Time, Sums/Gauges/Histograms map[name]…Metric}` — see below |
| `SumMetric` / `GaugeMetric` / `HistogramMetric` | the per-name envelopes — each carries `Description`, `""` when nobody wrote one |
| `SumPoint` / `GaugePoint` / `HistogramPoint` | the per-series points |
| `ObserveInt64` / `ObserveFloat64` / `Int64Callback` / `Float64Callback` | the observable func ports |
| `MeterConfig` / `DefaultMaxSeriesPerInstrument` | bound, temporality, resource, scope, clock |
| `OverflowAttrKey` | how a consumer RECOGNISES the folded series (its value is the bool `true`) |
| `Exporter` / `ExporterName` | exporter aliases |
| `NewMeter()` / `NewMeterWithConfig(cfg)` | in-memory `FullMeter` |
| `Export` / `RegisterExporter` / `NewTextExporter` / `NewPrometheusExporter` / `NewOTLPJSONExporter` / `AvailableExporters` | registry verbs |
| `EncodeOTLPJSON` | the OTLP/JSON ENCODER — snapshot to the exact bytes of one `ExportMetricsServiceRequest`, no I/O (ADR 0048) |
| `NewOTLPHTTPExporter` / `OTLPHTTPConfig` / `OTLPMetricsPath` / `DefaultOTLPTimeout` / `DefaultOTLPMaxResponseBytes` | the EMITTER and its knobs; never registered, and the endpoint is a full URL used as-is |
| `OTLPRetryable` | classifies an export failure; its signature IS `resilience.RetryConfig.Retryable`'s |
| `UnknownExporter` / `ExportFailed` / `InstrumentKindConflict` / `InvalidAttribute` / `InvalidTemporality` | sentinels |
| `InvalidDescription` / `DescriptionConflict` | the two `Describe` refusals — an empty description, and a second differing one for one name |
| `InvalidMetricName` / `InvalidLabelName` / `ReservedLabelName` / `UnsupportedTemporality` | how a consumer RECOGNISES what the Prometheus connector cannot carry |
| `OTLPUnresolvedTemporality` / `OTLPInvalidBucketLayout` | what the OTLP encoder refuses to spell — both structural, so they fail on the first export or never |
| `OTLPEndpointInvalid` / `OTLPExportRejected` / `OTLPExportUnavailable` / `OTLPPartialSuccess` | the OTLP/HTTP verdicts; only `OTLPExportUnavailable` is retryable |

## Conventions

- **Type aliases, not new types**; constructors are thin delegations. The core
  layer spells the shapes `…Value` (its role-suffix convention); this package
  publishes the short names, exactly as `Snapshot` has always aliased
  `SnapshotValue`.
- **`NewMeter` returns `FullMeter`, and a `Meter` parameter still accepts it.**
  Widening a returned VALUE is safe; widening the interface is what ADR 0039
  forbids. `UpDownCounter` and the observables live on siblings for that reason.
- **A description is NOT part of the series identity** — the OTel data model
  calls it non-identifying — so it lives on the metric envelope and `Describe`
  takes a NAME. `Describer` is a fourth sibling and stays OUT of `FullMeter`,
  because a union is still an interface and widening one breaks a downstream
  double at any version. Describing is a wiring-time call and costs the
  observation path nothing (`internal/service/observe/metrics/BENCH.md` §ADR 0067). Both
  refusals panic: an empty description would document nothing, and two differing
  descriptions for one name mean one wiring site is wrong. Identical text is
  idempotent.
- **An attribute's KIND is part of the series identity.** `String("v", "1")` and
  `Int64("v", 1)` are two series. The four constructors are the only way to
  build one; a struct literal leaves `AttrKindInvalid`, which is refused.
- **stdout is opt-in**: `NewTextExporter(name, os.Stdout)` binds it explicitly;
  none of the three registered exporters ever does (ADR 0030).
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
  It refuses any snapshot that is not cumulative (`UnsupportedTemporality`: a
  delta one, or a hand-built one whose temporality was left unresolved) and a
  name the format cannot spell (`InvalidMetricName`/`InvalidLabelName`/`ReservedLabelName`) —
  including an OTel-conventional DOTTED attribute key — rather than
  transliterating, which would merge distinct instruments silently. Full list of
  losses: `internal/service/observe/metrics/CLAUDE.md` §What the Prometheus connector
  loses.
- **`NewPrometheusExporter` is the scrape path**; the registered `prometheus`
  name is a stderr diagnostic, like `text` (ADR 0030).
- **OTLP/JSON is the native wire and loses nothing** (ADR 0048) — temporality
  travels, an attribute keeps its type, Resource and Scope reach the payload.
  It ships as TWO surfaces on purpose: `EncodeOTLPJSON` is a pure
  snapshot-to-bytes function that touches no socket, and `NewOTLPHTTPExporter`
  adds only the POST — so an encoding defect and a network defect are never the
  same investigation. `NewOTLPJSONExporter(name, w)` sits between them and
  writes NDJSON.
- **The OTLP/HTTP emitter does not retry, and that is the point.** It classifies
  (`OTLPRetryable`, exactly `resilience.RetryConfig.Retryable`'s shape) so the
  caller who owns the scrape loop owns the backoff, visibly, with a policy they
  can tune and cancel. A hidden loop inside `Export` could be neither.
- The OTLP **protobuf** encoding and the Prometheus **protobuf** format remain
  deferred (ADR 0048 §Deferred, ADR 0027); the JSON encoding is a first-class
  OTLP encoding, so binary would buy throughput rather than reach.
- README generated by gomarkdoc; edit the `metrics.go` doc comment and
  regenerate with `cd pkg/v1 && GOWORK=off go generate ./observe/metrics/...`.

## Do NOT

- Reimplement instruments here — the facade is aliases + wrappers.
- Import `go.opentelemetry.io/*`. Anywhere.
- Add a method to the `Meter` alias's underlying interface — or to `FullMeter`'s.
  Add a sibling.
- Fold `Describer` into `FullMeter` for convenience. The type assertion is the
  API: its false branch is how a caller learns a foreign `Meter` will drop their
  documentation.
- Register the OTLP/HTTP emitter, or hand it a default endpoint. An import
  must not arm a network client (ADR 0048).
- Wrap `NewOTLPHTTPExporter` in a private retry loop. `resilience` owns
  backoff; `OTLPRetryable` is the seam.

## Generated

`facade_gen.go` is kit's (ADR 0165): every alias, re-exported constant and variable, and forwarder of this package is declared in the `facade:` of `design/observe/metrics.yaml`, doc comments included, and kit writes it. Where this file names another file as holding one of them, read `facade_gen.go`: the hand-written files keep the package comment and the declarations of their own. Change a re-export, or its doc comment, in the design and run `kit gen` (then `make api` and `make docs-readme`); `make api-check` fails on a `facade_gen.go` edited by hand.

## Verification

```
bazel test --config=race //pkg/v1/observe/metrics:metrics_test
```
