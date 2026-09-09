# internal/core/metrics/

## Purpose

Declares the **observability port** — the natural twin of the logger — shaped on
the **OpenTelemetry metrics DATA MODEL**. Instruments
(`Counter`/`UpDownCounter`/`Gauge`/`Histogram` and their observable
counterparts), the `Meter` that mints + `Collect`s them, typed `AttrValue`s,
aggregation `Temporality`, the producing `ResourceValue` and the
`ScopeValue` that instrumented it, plus the `Exporter` contract + process-wide
registry that ships a `SnapshotValue` out. A core sibling admitted by
**ADR 0027** and re-shaped by **ADR 0044**. The in-memory meter and the stdlib
`text` / `prometheus` / `otlpjson` exporters live in
`internal/service/metrics`; exporters self-register via the registry
(writer-registry model, ADR 0012).

Code range: `0.2.9.*` (ADR 0027, extended by ADR 0044).

**OTel is a specification here, not a dependency.** Nothing in this package
imports `go.opentelemetry.io/*`, and nothing ever should: the model is a shape,
the OTel Go SDK would drag in the SDK-wide-banned `x/sys`, and what
interoperability actually needs is a payload an OTLP encoder can walk — which is
exactly what `SnapshotValue` is. That encoder now exists and imports nothing
either (`internal/service/metrics`, ADR 0048): the OTLP/JSON wire is
`encoding/json` over these values, which is the claim this posture was making. Same posture as the Prometheus exposition
format, RFC 7517 and the JWS Compact Serialization: implemented from the
document, with the standard library.

Instruments are keyed by name **and attribute set**: one name plus one attribute
set is one **series**, and a `Meter` is required to bound how many series a name
may hold.

## Contents

| File | Surface |
|---|---|
| `attr_value.go` | `AttrValue` + `AttrKind` + the four constructors (`String`/`Bool`/`Int64`/`Float64`) + accessors + `AppendIdentity`/`AppendText` + `CompareAttrKey`/`CompareAttrValue`/`ValidateAttrs`/`SortAttrs` + `OverflowAttrKey` |
| `temporality.go` | `Temporality` (Unspecified/Delta/Cumulative) + `String` + `Resolved` |
| `resource_value.go` | `ResourceValue` + `Normalized` + `ServiceNameKey`/`UnknownService` |
| `scope_value.go` | `ScopeValue` + `Normalized` + `DefaultScopeName` |
| `counter.go` / `updowncounter.go` / `gauge.go` / `histogram.go` | the four synchronous instrument interfaces (+ the package doc) |
| `observable.go` | `ObserveInt64`/`ObserveFloat64` + `Int64Callback`/`Float64Callback` — FUNC ports |
| `meter.go` | `Meter` — **frozen**: Counter/Gauge/Histogram + `Collect() SnapshotValue` |
| `updown_meter.go` / `async_meter.go` / `full_meter.go` | the two sibling ports and their union |
| `sum_value.go` / `gauge_value.go` / `histogram_value.go` | the three per-series point types |
| `snapshot_value.go` | `SumMetricValue`/`GaugeMetricValue`/`HistogramMetricValue` + `SnapshotValue` |
| `exporter.go` | `Exporter` + `ExporterName` + registry (`RegisterExporter`/`LookupExporter`/`AvailableExporters`/`Export`) |
| `codes.go` / `errors.go` | `0.2.9.*` (UNKNOWN_EXPORTER, EXPORT_FAILED, INSTRUMENT_KIND_CONFLICT, INVALID_ATTRIBUTE, DUPLICATE_REGISTRATION, INVALID_TEMPORALITY) |

`pkg/v1/metrics` publishes these under shorter names — `Attr`, `Resource`,
`Scope`, `Snapshot`, `SumPoint`/`SumMetric`, … — the same way `Snapshot` has
always aliased `SnapshotValue`. The `Value` suffix is this layer's role-suffix
convention, not part of the public vocabulary.

## The OTel model, and which parts are here

| Concept | Here | Where |
|---|---|---|
| Typed attributes (`string`/`bool`/`int64`/`double`) | yes | `AttrValue` + four constructors |
| Homogeneous ARRAY attributes | **deferred** | ADR 0044 §Decision 2 — a boxed field on the hot path's stack scratch |
| Aggregation temporality | yes | `Temporality`, on `SumMetricValue` / `HistogramMetricValue` |
| Resource | yes | `ResourceValue`, once per `SnapshotValue` |
| InstrumentationScope | yes (name + version) | `ScopeValue`; `SchemaURL` + scope attributes deferred |
| Sum, with `is_monotonic` | yes | `SumMetricValue.Monotonic` — one point shape, two instruments |
| Gauge (last value) | yes | `GaugeMetricValue` — and NO temporality, deliberately |
| Explicit-bucket Histogram | yes | `HistogramMetricValue` |
| Observable (asynchronous) instruments | yes | `AsyncMeter` + the two func callbacks |
| Exemplars | **deferred** | no tracing domain, so no span id to carry |
| Exponential histograms | **deferred** | a second point type, not a field |
| Summary (legacy) | **never** | the OTel spec itself says "not recommended for new applications" |

## The snapshot shape, and why it is this one

```go
type SnapshotValue struct {
    Resource   ResourceValue
    Scope      ScopeValue
    StartTime  time.Time
    Time       time.Time
    Sums       map[string]SumMetricValue        // name -> {Temporality, Monotonic, Points}
    Gauges     map[string]GaugeMetricValue      // name -> {Points}
    Histograms map[string]HistogramMetricValue  // name -> {Temporality, Points}
}
```

It is the OTel payload hierarchy flattened into one Go value:
`ResourceMetrics → ScopeMetrics → Metric → data points`, with the two
single-element levels collapsed because one `Meter` has exactly one Resource and
one Scope.

**Name → its metric → its series**, not `series key → value`. Every wire format
an exporter targets groups by name first: Prometheus emits one `# TYPE` header
per name followed by its series, OTLP nests data points inside one `Metric`. An
exporter walking these maps writes its header once per key and its points from
the slice — no regrouping pass, no composite key to parse, no second index.

The per-name **envelope** is what the earlier `map[name][]seriesValue` shape
could not carry: temporality and monotonicity belong to the METRIC in the OTel
model, not to a point, and they are the same for every series under one name.

Properties an exporter may rely on:

- **Each `Points` slice is sorted** by attribute set (key, then the value's
  total order, shorter set first). A snapshot therefore renders byte-identically
  twice in a row given the same values, which is what makes exporter output
  diffable and its tests writable without a sort of their own.
- **Each `Attrs` slice is sorted by `Key`**, and `nil` for the dimensionless
  series — so `len(Attrs) == 0` is the "no braces" case rather than something to
  special-case per exporter.
- **`Counts` is PER BUCKET**, not cumulative. OTLP wants exactly that; a format
  that wants a cumulative ladder builds it as it walks.
- **One `StartTime`/`Time` pair per snapshot.** OTLP puts them on every point;
  an encoder copies this pair down, because every point of one `Collect` covers
  the same window. That is a field assignment, not a reconstruction. It is also
  the one place a hand-built snapshot bites: `time.Time.UnixNano` is documented
  as undefined for the zero `Time`, so an encoder must map an unset instant onto
  the schema's own 0 rather than the year-2339 value the cast produces — see
  `internal/service/metrics/CLAUDE.md` §The OTLP/JSON encoder.

`Attrs` **aliases the meter's own copy** and must not be mutated. Cloning per
`Collect` would cost one allocation per series per scrape — i.e. it would scale
the cost of scraping with cardinality, the exact axis the bound exists to
contain. The meter never mutates an attribute set after creating the series, so
a reader is safe; a writer would corrupt every future snapshot.

## Conventions

- **A published port is extended by a SIBLING** (ADR 0039). `Meter` is frozen at
  Counter/Gauge/Histogram/Collect; `UpDownMeter` and `AsyncMeter` carry what it
  lacks, and `FullMeter` is the union every constructor returns. Widening a
  returned *value* is safe; widening the interface is not.
- **Instruments are multi-method interfaces** — domain-named, not
  `Adder`/`Recorder`. `UpDownCounter` carries `Dec` so it is structurally
  distinct from `Counter`: a Counter cannot be passed where a signed total is
  required.
- **The callbacks are FUNC types**, not single-method interfaces (ADR 0041): a
  published func type cannot grow a method at all.
- **`Collect` lives on `Meter`** (not a separate Collector) so every Meter is
  collectable without a second interface.
- **Attributes are variadic** on every instrument accessor. That makes the
  dimensionless series the zero-argument case.
- **An attribute KEY and an attribute KIND are structure; a VALUE is data.** Key
  and kind are written at the call site and constant for the process; values
  vary per observation. That asymmetry is why an unusable key or an unset value
  is a programmer error the implementation panics on (`InvalidAttribute`), while
  an unbounded stream of values is a runtime condition it must absorb.
- **An attribute set is a SET**: order is not identity, and a duplicate key is a
  refusal, not a last-one-wins merge — including when only the KIND differs.
- **The kind tag is part of the identity.** `AppendIdentity` writes it before the
  value, so `String("v", "1")` and `Int64("v", 1)` never encode alike. A double
  is keyed on its IEEE-754 BIT PATTERN, so ±0 and two NaN payloads are distinct
  series; `CompareAttrValue` breaks the same ties, or a sort would call two
  distinct series equal and the snapshot would stop being deterministic.
- **`Temporality` is on the metric, never on the point** — the OTel split — and
  a gauge has none at all.
- **Exporter registry** mirrors the writer registry (`snapshot.Value`, idempotent
  Register, panic on conflict).
- A name reused across instrument kinds is a programmer error
  (`InstrumentKindConflict`) — including Counter versus UpDownCounter, which
  would flip `Monotonic` on a metric a backend already trusts.

## Do NOT

- **Import `go.opentelemetry.io/*`.** The model is a specification; the code is
  ours. See §Purpose and ADR 0044 §Decision 1.
- Put meter/instrument bodies here — they live in `service/metrics`.
- Mutate an `Attrs` slice reached through a `SnapshotValue`. It is the meter's.
- Add a method to `Meter`. Add a sibling interface (ADR 0039).
- Give `GaugeMetricValue` a `Temporality`. A sampled reading covers no window,
  and OTLP's `Gauge` message has no such field.
- Add an "unbounded cardinality" mode to the port. ADR 0031: a bound of zero is
  a clamp, never an inert policy.
- Let `TemporalityUnspecified` reach a `SnapshotValue`. A Meter resolves it at
  construction, or refuses.

## Verification

```
bazel test --config=race //internal/core/metrics:metrics_test
```
