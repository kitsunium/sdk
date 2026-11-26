<!-- updated: 2026-10-05T00:00:00Z -->
# internal/core/observe/metrics/

## Purpose

Declares the **observability port** — the natural twin of the logger — shaped on
the **OpenTelemetry metrics DATA MODEL**. Instruments
(`Counter`/`UpDownCounter`/`Gauge`/`Histogram` and their observable
counterparts), the `Meter` that mints + `Collect`s them, aggregation
`Temporality`, the `Describer` sibling that documents an instrument name, plus
the `Exporter` contract + process-wide registry that ships a `SnapshotValue` out.

The typed attribute, the producing Resource and the Scope that instrumented it
are **not declared here**: they are OTel's shared `common.proto` /
`resource.proto` and live in `internal/core/observe/otel`, under this package AND
`internal/core/observe/trace` (ADR 0051 §Decision 2 named that extraction). What stays
here is this signal's half of them: the refusal an unusable attribute set earns
(`InvalidAttribute`, `0.2.9.4` — unchanged by the move), the `DefaultScopeName`
only this signal may claim, and `OverflowAttrKey`, which only a meter writes. A core sibling admitted by **ADR 0027**, re-shaped by
**ADR 0044** and given a description by **ADR 0067**. The in-memory meter and the stdlib
`text` / `prometheus` / `otlpjson` exporters live in
`internal/service/observe/metrics`; exporters self-register via the registry
(writer-registry model, ADR 0012).

Code ranges: `0.2.9.*` (ADR 0027, extended by ADR 0044 and ADR 0067), and
`0.3.45.*` — the range ADR 0005 allocated to the engine,
`internal/service/observe/metrics`, for what a wire format refuses, declared here
since ADR 0160 §2 with its value unchanged. The engine returns those sentinels
and declares none; `codeRangeOwners` maps both keys to this directory.

**OTel is a specification here, not a dependency.** Nothing in this package
imports `go.opentelemetry.io/*`, and nothing ever should: the model is a shape,
the OTel Go SDK would drag in the SDK-wide-banned `x/sys`, and what
interoperability actually needs is a payload an OTLP encoder can walk — which is
exactly what `SnapshotValue` is. That encoder now exists and imports nothing
either (`internal/service/observe/metrics`, ADR 0048): the OTLP/JSON wire is
`encoding/json` over these values, which is the claim this posture was making. Same posture as the Prometheus exposition
format, RFC 7517 and the JWS Compact Serialization: implemented from the
document, with the standard library.

Instruments are keyed by name **and attribute set**: one name plus one attribute
set is one **series**, and a `Meter` is required to bound how many series a name
may hold.

**The ports are generated from the design** (ADR 0163): `Counter`,
`UpDownCounter`, `Gauge`, `Histogram`, `Meter`, `FullMeter`, `UpDownMeter`,
`AsyncMeter`, `Describer`, `Exporter` and its siblings `ContextExporter` and `DocumentExporter` are declared, with their doc comments,
under `ports:` in `design/observe/metrics.yaml`, and `kit gen` writes them into
`gen_ports.go`. A port or its doc comment changes in the design, then `kit
gen`, then `make api` — never in `gen_ports.go`, whose header digests `make
api-check` verifies. They moved there from `counter.go`, `down.go`, `gauge.go`,
`histogram.go`, `meter.go`, `async_meter.go`, `describer.go` and `exporter.go`,
content moved and never deleted. The seven files the move left with nothing but
their package comment, `counter.go` included, are gone (rule 5);
the package comment is `doc.go`'s alone, written by kit from the design (ADR 0167).

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
- **The attribute rules are `internal/core/observe/otel`'s; the refusal is this
  package's.** `ValidateAttrs` and `NormalizeResource` are one-line wrappers
  handing `InvalidAttribute` to the shared model, so the meter's panics carry
  `0.2.9.4` exactly as they did before the model moved. The four bullets below
  are the model's own conventions, restated because the meter is where they bite.
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
  a gauge has none at all. **So is `Description`**, for the same reason: the OTel
  data model puts it on the Metric, and the specification calls it explicitly
  NON-IDENTIFYING, so it never joins a series key.
- **A unit belongs to the NAME too (ADR 0208).** `Unit(name, unit)` binds a
  UCUM unit: printable ASCII `0x21`–`0x7E`, 1 to 63 bytes, else `InvalidUnit`
  (`0.2.9.9`); a second differing unit `UnitConflict` (`0.2.9.10`); identical is
  idempotent.
- **A histogram's layout is checked at creation (#1135).** An empty one takes
  `DefaultBuckets()` (a fresh copy of the OpenTelemetry SDK's 0…10000 ladder);
  NaN, ±Inf or a repeated bound panics `InvalidBuckets` (`0.2.9.11`) before the
  series is admitted — the `Histogram` port doc states both rules.
- **A description belongs to the NAME, not to a call site.** `Describe(name, …)`
  rather than a parameter on `Counter`, which also keeps the docstring off the
  observation path entirely. An empty description and a second, differing one
  are both programmer errors the implementation panics on
  (`InvalidDescription` / `DescriptionConflict`); identical text is idempotent.
- **Exporter registry** mirrors the writer registry (`kernel/plugin.Registry`
  over a `snapshot.Value`, idempotent Register, panic on conflict under this
  package's `DUPLICATE_REGISTRATION`).
- A name reused across instrument kinds is a programmer error
  (`InstrumentKindConflict`) — including Counter versus UpDownCounter, which
  would flip `Monotonic` on a metric a backend already trusts.

## Do NOT

- **Import `go.opentelemetry.io/*`.** The model is a specification; the code is
  ours. See §Purpose and ADR 0044 §Decision 1.
- **Redeclare the shared model here.** `AttrValue`, `ResourceValue` and
  `ScopeValue` are `internal/core/observe/otel`'s; a twin would encode identically,
  pass review, and split `metrics.Attr` from `trace.Attr` (ADR 0051 §Decision 2).
- Put meter/instrument bodies here — they live in `service/observe/metrics`.
- Mutate an `Attrs` slice reached through a `SnapshotValue`. It is the meter's.
- Add a method to `Meter` — **or to `FullMeter`**. Add a sibling interface
  (ADR 0039). A union is still an interface: folding `Describer` into `FullMeter`
  would break every downstream double, which is what
  `TestPreDescriberDoubleStillSatisfiesFullMeter` fails on.
- Refuse a description for the BYTES it carries. A name and an attribute key are
  structure and are validated; a description is prose, and escaping it is the
  exporter's job.
- Import `core/observe/trace` for an exemplar. `ExemplarValue` holds the raw
  arrays; the engine reads the span off the context (ADR 0051 §Decision 2).
- Give `GaugeMetricValue` a `Temporality`. A sampled reading covers no window,
  and OTLP's `Gauge` message has no such field.
- Add an "unbounded cardinality" mode to the port. ADR 0031: a bound of zero is
  a clamp, never an inert policy.
- Let `TemporalityUnspecified` reach a `SnapshotValue`. A Meter resolves it at
  construction, or refuses.

## Verification

```
bazel test --config=race //internal/core/observe/metrics:metrics_test
```

## Reference in NOTES.md

Moved unchanged to `internal/core/observe/metrics/NOTES.md`, under the same titles, and read only by a change that needs its mechanics: `Contents`; `The OTel model, and which parts are here`; `The snapshot shape, and why it is this one`.
