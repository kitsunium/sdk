# ADR 0044 — `metrics` adopts the OpenTelemetry DATA MODEL, and none of its code

- **Status**: Accepted
- **Date**: 2026-09-09
- **Deciders**: SDK maintainers
- **Related**: [ADR 0027](0027-sdk-metrics-domain.md) (the metrics domain this reshapes), [ADR 0039](0039-extending-a-published-port-without-breaking-it.md) (sibling interfaces), [ADR 0040](0040-changing-a-published-shape-while-v0.md) (a published shape may change while v0), [ADR 0031](0031-policy-zero-values-are-never-inert.md) (zero values), [ADR 0041](0041-sdk-scheduler-domain.md) (func ports), [ADR 0030](0030-stdout-is-a-protocol-channel.md) (exporter destinations)
- **Amends**: ADR 0027 §Decision 1 (the port's shape) and §Deferred

## Context

`internal/core/metrics` shipped a house model: a label was a
`{Key, Value string}` pair, a counter was monotonic by construction, a snapshot
was three maps of name → series, and nothing anywhere said which time window a
number covered. It worked, and it was ours.

It was also incompatible with the only metrics model the industry has agreed on.
Measured against the OpenTelemetry metrics specification, the domain carried
**none** of its load-bearing concepts:

| OTel concept | Present before |
|---|---|
| Typed attributes (`string` / `bool` / `int64` / `double`) | no — string→string only |
| Aggregation temporality (delta / cumulative) | no — implicitly cumulative, and silent about it |
| `Resource` (who produced this) | no |
| `InstrumentationScope` (what instrumented it) | no |
| Non-monotonic sums (`UpDownCounter`) | no |
| Observable (asynchronous) instruments | no |
| Exemplars | no |
| Exponential histograms | no |

The gap that matters most is **temporality**. `http_requests_total 4200` means
two different things under the two settings — "4200 since the process started"
or "4200 since you last asked" — and nothing in the number distinguishes them.
A consumer who guesses wrong does not get an error; they get a dashboard that is
wrong by a factor of the scrape interval, forever. A model that cannot say which
one it means is not a model.

The second gap is **attributes**. `status="503"` and `status=503` are the same
dimension spelled two ways, and a string-only model forces every producer to
pick one and every consumer to guess. OTLP carries the type; so should the
snapshot an OTLP encoder will read.

## Decision

### 1. Adopt the OTel data model. Import nothing from `go.opentelemetry.io`.

OpenTelemetry is a **published specification**, not only a library. This SDK
already implements the Prometheus text exposition format, RFC 7517 (JWK), the
JWS Compact Serialization and a five-field POSIX cron from their documents, with
the standard library and nothing else. The metrics data model is implemented the
same way, for the same reasons:

- The dependency budget is the product. `go.opentelemetry.io/otel` plus
  `otel/sdk` plus `otel/metric` drags in `x/sys` — banned SDK-wide (ADR 0022 /
  ADR 0034) — and a release cadence this repo does not control, for a model
  whose whole content is a shape.
- The model is small enough to own. Everything below is under 400 lines of
  declarations.
- What OTel actually buys is **interoperability**, and interoperability is a
  property of the wire, not of the import graph. A snapshot that carries
  temporality, resource, scope and typed attributes is OTLP-encodable by
  construction — which is the point.

The SDK therefore speaks the model without depending on the project.

### 2. Typed attributes replace string labels.

`LabelValue{Key, Value string}` becomes `AttrValue`: an exported `Key` plus an
**unexported** value carrying one of four kinds — `String`, `Bool`, `Int64`,
`Float64`. The value is unexported so the only way to build a usable attribute
is one of the four constructors, and the string case stays the shortest thing to
write:

```go
m.Counter("requests",
    metrics.String("http.request.method", "GET"),
    metrics.Int64("http.response.status_code", 503),
    metrics.Bool("cache.hit", false),
).Inc()
```

A struct literal can still set `Key`; the resulting `AttrKindInvalid` is
**refused** rather than read as an empty string (ADR 0031).

**The kind is part of the series identity.** The series key gains a kind tag per
value, so `String("v", "1")` and `Int64("v", 1)` are two series. Without the tag
they would encode identically the moment either was rendered — the same
forge-by-collision hazard the length-prefixed key already existed to prevent,
one level up.

**Homogeneous ARRAY attributes are deferred**, and the reason is measured rather
than aesthetic: a slice-valued attribute needs a boxed field on `AttrValue`,
which grows the struct on the stack scratch every observation sorts through and
costs one heap allocation per array-valued observation, on the path the whole
cardinality design keeps allocation-free. An array is also a poor dimension by
construction — it multiplies cardinality — and neither exporter in this SDK can
spell one. The four scalar kinds are the OTel specification's own "standard
attribute" set; arrays land the day something needs them, as a fifth kind.

### 3. Temporality is explicit, and REAL.

`Temporality` is `Unspecified` / `Delta` / `Cumulative`, carried on
`SumMetricValue` and `HistogramMetricValue` — on the metric, exactly where the
OTel model puts it — and **not** on `GaugeMetricValue`, because a gauge is a
sampled reading that covers no window and OTLP's `Gauge` message has no such
field either.

It is not decoration. A meter built with `TemporalityDelta` makes `Collect` a
**mutation**: every synchronous accumulator is swapped to zero, every histogram
bucket with it, and the snapshot's `StartTime` advances to the previous
collection's `Time`. Under `TemporalityCumulative` the accumulators are read and
the start repeats — which is the specification's own wording, "successive data
points repeat the starting timestamp".

Consequences stated rather than discovered:

- **A delta meter has exactly one reader.** Two concurrent collections would
  each carry away part of the observations. `Collect` is serialised against
  itself by a dedicated mutex so each window is whole, and a test sums eight
  concurrent delta collections back to the number recorded.
- **A series that saw nothing in a delta window reports zero** rather than
  vanishing. Omitting it would make a series flicker in and out of a dashboard,
  and the cardinality bound already keeps the set finite.

Per ADR 0031, the unset value is not inert: `TemporalityUnspecified` **clamps**
to `Cumulative`, and the reason is not convention — an in-memory meter
accumulates into atomics and never resets them unless asked, so "cumulative" is
a *description of what the meter does*, not a value chosen on the caller's
behalf. A `Temporality` that is none of the three constants can only come from a
cast, so it **refuses**, with `InvalidTemporality` (`0.2.9.6`).

### 4. Resource and Scope are carried once per payload.

`ResourceValue` (the producer: `service.name`, host, pid) and `ScopeValue` (the
instrumenting library: name + version) are fields of `SnapshotValue`, not of
every point. That is the whole reason the concepts exist: `service.name` on ten
thousand data points is ten thousand copies of one fact.

An absent `service.name` resolves to `unknown_service` — **the specification's
own mandate for exactly this case**, which is what makes it a clamp rather than
an invention. The spec's optional `unknown_service:<executable>` refinement is
not applied: a binary path is not a service identity and would silently become
one on a dashboard. An empty scope name resolves to this SDK's own import path,
the one thing the meter can state truthfully. An empty scope **version** stays
empty, because the specification makes it optional and inventing one would be a
claim about code the package cannot see.

`SchemaURL` and scope-level attributes are absent on both, deliberately: both
are optional, nothing here produces either, and an always-empty field is a
placeholder (rule 5).

### 5. `UpDownCounter` is a sibling instrument, and one field in the model.

The API gains `UpDownCounter` (`Add`, `Inc`, `Dec`); the data model does **not**
gain a point type. A Counter and an UpDownCounter both produce a
`SumMetricValue`, told apart by `Monotonic` — the OTel model's own economy, and
the reason there are two interfaces and one shape.

`Dec` exists so the two interfaces are structurally distinct: without it a
`Counter` would satisfy `UpDownCounter` and could be passed where a signed total
was required. On a monotonic sum `Dec` (and any non-positive `Add`) is inert,
because a backend reading `Monotonic = true` is entitled to treat a decrease as
a process restart.

### 6. Observable instruments ship.

`ObservableCounter`, `ObservableUpDownCounter` and `ObservableGauge` register a
callback read once per `Collect`. The reporting functions are **func ports**
(`ObserveInt64`, `ObserveFloat64`, `Int64Callback`, `Float64Callback`) rather
than single-method interfaces, applying ADR 0041 structurally: a published func
type cannot grow a method at all.

A callback reports the **absolute** total, which is the OTel contract; under
delta temporality the meter differences successive reports itself. Registrations
**accumulate** rather than replace, as the OTel API specifies. There is no
unregistration: an observable is declared at wiring time and lives as long as
the meter.

Two consequences named rather than left to be found:

- A callback runs inline in the collection, so a slow one is a slow scrape for
  every instrument. It must not call `Collect` on its own meter.
- A folded observable is lossier than a folded counter: past the cardinality
  bound every further attribute set lands in one overflow series, and each
  report **overwrites** the previous one rather than adding to it. An
  observable's attribute set should be small and fixed.

### 7. Extend by siblings, break the shape loudly, and say which is which.

The two halves of the ADR 0039 / ADR 0040 rule are both in force here, and the
change is a worked example of each:

- **`Meter` is FROZEN** and keeps exactly `Counter` / `Gauge` / `Histogram` /
  `Collect`. `UpDownCounter` and the three observables land on the sibling
  interfaces `UpDownMeter` and `AsyncMeter`; `FullMeter` is the union, and it is
  what the constructors return. Widening a returned **value** from `Meter` to
  `FullMeter` is safe for the same reason widening `clock.System` was — every
  existing assignment into a `Meter` still compiles.
- **The published SHAPES change, and v0 is the only reason.** This is ADR 0040
  applied a second time, to the same domain. Named explicitly, because "it
  compiles here" is not a reason:

  | `pkg/v1` alias | Old shape | New shape |
  |---|---|---|
  | `metrics.Snapshot` = `SnapshotValue` | `{Counters, Gauges, Histograms map[string][]…Value}` | `{Resource, Scope, StartTime, Time, Sums map[string]SumMetricValue, Gauges …, Histograms …}` |
  | `metrics.Label` = `LabelValue` | `{Key, Value string}` | **gone** — replaced by `metrics.Attr` = `AttrValue`, whose value is typed and unexported |
  | `metrics.CounterValue` | `{Labels []LabelValue; Value int64}` | **gone** — replaced by `metrics.SumPoint` = `SumValue`, `{Attrs []AttrValue; Value int64}`, under a `SumMetric` envelope |
  | `metrics.GaugeValue` / `HistogramValue` | as above | `metrics.GaugePoint` / `HistogramPoint`, under envelopes; `Buckets` renamed `Bounds` (OTLP calls it `explicit_bounds`) |
  | `metrics.OverflowLabelKey` / `OverflowLabelValue` | `string` pair | `metrics.OverflowAttrKey` alone — the marker is now a **bool** attribute |
  | `metrics.InvalidLabel` | sentinel | `metrics.InvalidAttribute` (same code `0.2.9.4`, reason `INVALID_ATTRIBUTE`) |

  Every one of these reaches consumers through a `pkg/v1/metrics` type alias.
  **The module is `pkg/v0.1.26`**, and Go promises nothing across v0 minors
  (`go.dev/ref/mod#v0-major`). That licence — and nothing else — is what permits
  this. It expires at `pkg/v1.0.0`, after which the same edit would require a
  new named type, a `pkg/v2` path, or not happening.

  There is one interaction ADR 0040 did not have to state and this change does:
  **a shape change propagates into every port that mentions it.** `Meter`'s
  method set is not widened — but `Counter(name string, attrs ...AttrValue)` is
  not the same signature as `Counter(name string, labels ...LabelValue)`, so a
  downstream `Meter` implementer breaks too. ADR 0039 forbids *widening a method
  set*; it cannot forbid a mentioned type from changing without voiding ADR 0040
  for every shape a port passes — which is all of them.

### 8. The Prometheus exporter stays, requalified as a lossy CONNECTOR.

It is not a rendering of this model; the exposition format predates most of it.
It is kept because a Prometheus deployment is a real destination, and its losses
are now enumerated in `internal/service/metrics/CLAUDE.md` §What the Prometheus
connector loses, each with an executable test:

- **Temporality**: the format has none, and a server reads every counter as
  cumulative. A delta snapshot is **REFUSED** (`UNSUPPORTED_TEMPORALITY`,
  `0.3.45.4`), not mis-labelled — `rate()` over values that are already
  differences reports the second derivative, and every decrease reads as a
  counter reset.
- **The attribute's TYPE**: a Prometheus label value *is* a string.
  `Int64("v", 1)` and `String("v", "1")` — two series here — become one there.
  Unlike a mangled name, there is no injective alternative: the target has one
  value type.
- **Resource and Scope**: dropped entirely. `service.name` cannot even be
  spelled — a Prometheus label name is `[a-zA-Z_][a-zA-Z0-9_]*` and the dot is
  outside it. The OTel-to-Prometheus interoperability specification answers this
  by mangling the key into a `target_info` metric; this SDK does not, because
  the mangling is not injective and this exporter already refuses non-injective
  name rewriting for instrument names in the same file. Consistency with our own
  rule beats consistency with theirs, and the loss is named instead.
- **OTel-conventional attribute keys**: the sharpest consequence of adopting the
  model. `http.request.method` is the spelling the semantic conventions
  *specify*, and this connector refuses it by name. That is deliberate and it is
  loud — a scrape fails on the first attempt or never, because a key is a
  literal at the call site.
- **Exemplars**: not produced at all.

One behaviour changes rather than being lost: a **non-monotonic** sum is typed
`gauge`, not `counter`, which is the interoperability specification's own
mapping and what keeps `rate()` from inventing a spike every time the value
falls.

### 9. No OTLP exporter here.

It lands next, on top of this. The snapshot is shaped so that an OTLP/JSON or
OTLP/protobuf encoder walks it **without regrouping or reconstruction**:

```
SnapshotValue                → ResourceMetrics
  .Resource                  →   .resource.attributes
  .Scope                     →   .scopeMetrics[0].scope {name, version}
  .Sums[name]                →   .scopeMetrics[0].metrics[] {name, sum:{
                                    aggregationTemporality, isMonotonic, dataPoints}}
  .Sums[name].Points[i]      →     NumberDataPoint {attributes, asInt, startTimeUnixNano, timeUnixNano}
  .Gauges[name]              →   metrics[] {name, gauge:{dataPoints}}
  .Histograms[name]          →   metrics[] {name, histogram:{aggregationTemporality, dataPoints}}
  .Histograms[…].Points[i]   →     HistogramDataPoint {attributes, count, sum,
                                    bucketCounts, explicitBounds, …}
  .StartTime / .Time         →   copied DOWN onto every data point
```

The two single-element levels of the OTel hierarchy are collapsed because one
Meter has exactly one Resource and one Scope. The timestamps are one pair per
snapshot rather than one per point because every point of one `Collect` covers
the same window; an encoder copies the pair down, which is a field assignment,
not a reconstruction. `Counts` is **per bucket**, not cumulative — which is what
OTLP wants and what Prometheus has to convert.

## Consequences

- `internal/core/metrics` grows the model values (`AttrValue`, `Temporality`,
  `ResourceValue`, `ScopeValue`, the point/metric pairs) and two sibling ports.
  `internal/service/metrics` grows a delta path, an observable path and a
  seven-valued instrument kind. `pkg/v1/metrics` re-exports all of it under the
  short names, and its `README.md` is regenerated from the package comment
  (rule 10).
- **Error codes**: `0.2.9.4` keeps its value and is renamed
  `INVALID_LABEL` → `INVALID_ATTRIBUTE`; `0.2.9.6 INVALID_TEMPORALITY` and
  `0.3.45.4 UNSUPPORTED_TEMPORALITY` are new. Both land in blocks the domain
  already owns — no new `codeRangeOwners` entry (ADR 0035).
- **The instrument kind now opens the series key.** Four instrument kinds share
  one sum store, so without it `Counter("x")` and `UpDownCounter("x")` would
  resolve to the same key, the second call would hit the read lock, and the
  cross-kind conflict would never reach the guard. One byte on a stack buffer
  buys the check back without a second map read per observation.
- **The constructors are thin on purpose.** `NewMeter` / `NewMeterWithConfig`
  are one-line wrappers over an unexported builder so both stay inlinable:
  inlining is what carries the concrete meter type to the call sites below it,
  and that is what lets the compiler prove a variadic attribute slice does not
  escape. Growing either past the inlining budget costs one allocation per
  attributed observation — measured, and pinned by the allocation gate.
- **Performance held.** A resolved attributed lookup is still **0 allocations**
  and within noise of the string-label implementation; `Collect` pays for the
  richer payload. Numbers, before and after, in
  `internal/service/metrics/BENCH.md`.

## Deferred

Named here so a future reader finds the reason rather than the gap:

- **Exemplars.** A data point may carry a sample linking it to a trace. This SDK
  has no tracing domain, so an exemplar would have no span id to hold; the field
  lands with the first trace context, not before.
- **Exponential (base-2) histograms.** A different bucketing scheme with its own
  scale / offset / zero-count fields — a second point type rather than a field
  on the existing one, and the explicit-bounds histogram is what both current
  exporters can carry.
- **Homogeneous array attributes.** §Decision 2, with the allocation reason.
- **`SchemaURL`** on Resource and Scope, and scope-level attributes. §Decision 4.
- **The OTLP exporter itself.** §Decision 9.
- **Views / aggregation configuration** (renaming, filtering or re-aggregating
  an instrument at the SDK edge). It is a whole configuration surface, and
  nothing in this SDK needs it to produce a correct payload.

## Why not

- **Depend on `go.opentelemetry.io/otel`.** Rejected: it introduces `x/sys`,
  which the SDK bans (ADR 0022 / ADR 0034), plus a release cadence this repo
  does not control — in exchange for a data model that is a shape. What
  interoperability needs is the wire, and the wire is reachable from a snapshot
  that carries the model.
- **Keep string labels and add types later.** Rejected: the attribute type is
  part of the SERIES IDENTITY, so retrofitting it re-partitions every existing
  series. Doing it at v0, once, is the cheap moment; doing it at v1 is a `pkg/v2`.
- **Make `Temporality` a field the exporters read and the meter ignores.**
  Rejected as exactly the inert value ADR 0031 bans: a meter that advertises
  `Delta` and reports cumulative numbers is worse than one that never mentions
  temporality, because a consumer would act on the label.
- **Delete the Prometheus exporter now that the model is OTel's.** Rejected: a
  Prometheus deployment is a real destination and the format is a real wire. The
  answer to a lossy connector is to NAME the losses and refuse the cases where
  the loss is silent — which is what §Decision 8 does.
- **Emit `target_info` so the Resource survives into Prometheus.** Rejected:
  `service.name` is not spellable as a Prometheus label name, so it requires the
  non-injective `_` mangling this exporter refuses everywhere else in the same
  file. Naming the loss is honest; mangling it is a merge nobody can detect.
- **Add `UpDownCounter` to `Meter`.** Rejected by ADR 0039: `Meter` is published
  through a `pkg/v1` alias and Go interfaces are structural, so widening it
  breaks every downstream implementer at compile time with no deprecation
  window. A sibling costs one name.
- **Defer observable instruments.** Considered, and rejected once the shape was
  clear: a callback read at collection reuses the ordinary fetch path
  wholesale — it is the same series identity, the same cardinality bound, one
  extra flag on the sum — and leaving it out would have made the `AsyncMeter`
  sibling a placeholder rather than a port.

## References

- OpenTelemetry metrics **data model** — <https://opentelemetry.io/docs/specs/otel/metrics/data-model/>
- OpenTelemetry metrics **API** (the instrument set) — <https://opentelemetry.io/docs/specs/otel/metrics/api/>
- OpenTelemetry **common** (the attribute definition) — <https://opentelemetry.io/docs/specs/otel/common/>
- Impl: `internal/core/metrics/`, `internal/service/metrics/`, `pkg/v1/metrics/`
- Rationale in place: `internal/core/metrics/CLAUDE.md`,
  `internal/service/metrics/CLAUDE.md` §What the Prometheus connector loses,
  `internal/service/metrics/BENCH.md`
- ADR 0027 (the domain), ADR 0039 (siblings), ADR 0040 (the v0 licence),
  ADR 0031 (zero values), ADR 0041 (func ports), ADR 0035 (code ranges)
- [Go modules reference — v0 major version](https://go.dev/ref/mod#v0-major)
