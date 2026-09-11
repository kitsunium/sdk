# ADR 0067 — an instrument gets a description, and it belongs to the NAME

- **Status**: Accepted
- **Date**: 2026-09-10
- **Deciders**: SDK maintainers
- **Related**: [ADR 0044](0044-metrics-adopts-the-otel-data-model.md) (the data model that has this field), [ADR 0039](0039-extending-a-published-port-without-breaking-it.md) (a published interface is extended by a sibling), [ADR 0040](0040-changing-a-published-shape-while-v0.md) (**the v0 licence this ADR invokes**), [ADR 0048](0048-sdk-metrics-otlp-json.md) (the OTLP/JSON wire, and its three fields emitted at their zero), [ADR 0031](0031-policy-zero-values-are-never-inert.md) (clamp vs refuse), [ADR 0027](0027-sdk-metrics-domain.md) (the domain)

## Context

`internal/service/metrics/exporter_prometheus.go` carried a promissory note, on
`appendTypeLine`:

> No `# HELP` line accompanies it. HELP is optional in the exposition format and
> carries a DOCSTRING; the SDK's Meter records no description for an instrument,
> so the only HELP this exporter could emit is the metric name repeated back or
> a fixed sentence restating the TYPE line. Both are placeholders, and a
> placeholder on every scrape teaches an operator nothing. **The day Meter grows
> a description, HELP lands on the line above this one** and nothing else about
> the document changes.

That day is here, and it was ADR 0044 that brought it. Adopting the
OpenTelemetry metrics **data model** means the SDK claims to implement a
document, and that document's `Metric` message has three fields the SDK did not
produce: `name`, `description` and `unit`. Two of the three absences were fine —
`unit` is a second decision with its own consequences (on the Prometheus wire a
unit becomes a name **suffix**, so it is not a field, it is a renaming) — but
`description` is different: it is the one the exposition format has had a slot
for since before OTel existed, and the one every wire the SDK speaks can carry.

So the gap is not "a nice-to-have is missing". It is: **the SDK says it
implements a model and omits a field of that model**, while the connector on the
other side has an empty line waiting for it.

Three facts from the specifications shaped everything below.

1. **The description is on the `Metric`, not on the data point.** OTLP's own
   data-model page says the description is **explicitly non-identifying**: two
   streams that differ only by their description are one stream. It can
   therefore never join a series key.
2. **The OTel→Prometheus interoperability specification already names the
   mapping**: "OTLP metric point descriptions become HELP metadata". Nothing had
   to be invented here.
3. **`description` is `string description = 2;`** — a plain proto3 string with
   no `optional`, so it has **no explicit presence**. `""` and absent are the
   same value to a receiver.

## Decision

### 1. The sibling is `Describer`, with `Describe(name, description string)`

`Meter` is frozen (ADR 0039, restated by ADR 0044: "Meter is frozen; new
instruments live on sibling interfaces"), so a description cannot be a parameter
on `Counter`. Two sibling shapes were possible, and they are not equivalent.

**Rejected: `CounterWithDescription(name, description string, attrs ...Attr)`**
and its six siblings. Three reasons, in increasing order of weight:

- It multiplies. Four synchronous instruments plus three observable ones is
  **seven** new methods for one string.
- It attaches the docstring to a **call site**, and a metric has many. Every
  site fetching `http_requests_total` with its own attribute set would repeat
  the description, and two of them could disagree while both looked correct.
  That is a defect the shape invites rather than one a caller can avoid.
- It puts a documentation string **on the observation path**. `Counter(name,
  attrs...)` runs once per observation and is the one call in this package whose
  variadic slice the compiler must prove non-escaping (ADR 0044's measured
  finding: `NewMeter` slipping past the inlining budget cost 48 bytes per
  attributed observation). Adding a second string argument to that call is
  spending the SDK's tightest budget on prose.

**Accepted: `Describer.Describe(name, description string)`**, discovered by type
assertion. It matches where the model puts the field, it is called once at
wiring time, and it touches nothing the observation path reads. The naming
follows the OTel model's own vocabulary rather than the wire's: it is
`Describe`, not `Help`, because `# HELP` is one format's spelling of it and
OTLP's `description` is another.

### 2. `Describer` is NOT folded into `FullMeter`

This was the closest call in the ADR and it is worth stating why the convenient
option lost.

`FullMeter` is `Meter + UpDownMeter + AsyncMeter`, it is what `NewMeter`
returns, and it is published by a `pkg/v1/metrics` alias. Adding `Describer` to
it would make `meter.Describe(…)` work with no assertion. It is refused because
**a union is still an interface**: Go satisfies interfaces structurally, so a
downstream test double implementing the seven methods and assigned to a
`FullMeter` variable breaks at COMPILE time, with no deprecation window. ADR 0039
has no "but this one is only ever returned" clause, and inventing one here would
retire the rule by exception. `TestPreDescriberDoubleStillSatisfiesFullMeter` is
that guard, written with the seven methods spelled out by hand so that embedding
cannot hide a later widening.

Widening `NewMeter`'s **return type** to a new fifth union was also considered —
it is legal, and it is exactly the move ADR 0039 §Decision 1 blessed when
`clock.System` widened from `Clock` to `Timed`. It is refused for a different
reason: it starts a naming treadmill (the next sibling needs a sixth name), and
it would make the type assertion look like an accident rather than the API. The
assertion is the API:

```go
if d, ok := meter.(metrics.Describer); ok {
	d.Describe("http_server_requests", "Requests served, by route and status")
}
```

The false branch carries information. A `Meter` that records no description — a
downstream double, a no-op meter, an implementation written before this ADR —
legitimately does not implement `Describer`, and `ok == false` is how a caller
learns their documentation will not reach the wire. That is `cache.Tagger`
(ADR 0049) and `lock.Deadliner` (ADR 0052) again: **the absence of the sibling
is the answer.**

### 3. Two refusals, both panics; identical text is idempotent

`Describe` returns nothing, exactly as `Counter(name)` returns a `Counter`, so
there is nowhere to put an error — and both mistakes are programming ones, fixed
at a wiring site, constant for the process. They fail on the first boot or
never. That is the argument `bindName` already makes for
`InstrumentKindConflict`, and this is the same class of failure one notch less
severe.

| Call | Outcome | Why |
|---|---|---|
| `Describe(name, "")` | panic `INVALID_DESCRIPTION` (`0.2.9.7`) | a call that documents nothing is the **inert** outcome ADR 0031 bans. The caller meant to write something |
| `Describe(name, x)` then `Describe(name, y)`, `x != y` | panic `DESCRIPTION_CONFLICT` (`0.2.9.8`) | a description belongs to the NAME, so two of them means one of the two wiring sites is wrong — and whichever one lost would be invisible on every wire |
| `Describe(name, x)` twice with the same `x` | idempotent no-op | two packages documenting one metric identically have not disagreed. Refusing this would make a shared instrument impossible to document from more than one place |
| `Describe` on a name that never becomes an instrument | accepted, never emitted | a description with no metric has nowhere to be wrong, and requiring the instrument first would be an ordering rule with no reason behind it |

**The OpenTelemetry SDK specification resolves the conflict case differently** —
it says to keep the first description and log a warning. This SDK refuses
instead, and states the two reasons rather than inheriting the behaviour:
`core/metrics` has no logger to warn through (acquiring one would invert the
layer order), and "first wins, quietly" is precisely the outcome where the
losing wiring site stays wrong forever while its author reads a dashboard that
looks fine.

**A description is DATA, not structure.** Unlike an instrument name or an
attribute key — both of which the Prometheus connector refuses when the wire
cannot spell them — a description is prose, and no byte in it is refused. The
format's job is to escape it. That is the same asymmetry `appendPromLabels`
already draws one line apart: the key is validated, the value is escaped.

### 4. `Description` on the three metric envelopes — the ADR 0040 v0 licence, invoked out loud

`SumMetricValue`, `GaugeMetricValue` and `HistogramMetricValue` each gain
`Description string`. All three are **published shapes**:
`pkg/v1/metrics.SumMetric`, `GaugeMetric` and `HistogramMetric` are type
aliases, so the change reaches every consumer directly.

**This is a breaking change to a published concrete shape, and it is permitted
only because the module is v0.** ADR 0040 §Decision 1 requires that to be said
explicitly — naming the aliases, the change and v0 as the reason — rather than
left silent, and this paragraph is that statement. `Release-bump: minor` is the
correct trailer (ADR 0040 §Decision 2): v0 minors carry no compatibility
promise. Past `pkg/v1.0.0` this same edit would require a new named type, a
`pkg/v2` path, or not happening. The three types are in the "expensive" group B
of `docs/pre-v1-published-shape-audit.md`, where an ADDED field
breaks a caller who wrote an unkeyed composite literal.

**What actually breaks in this repository: nothing** — and that was checked
rather than assumed. Every construction site of the three types, production and
test, is written with FIELD NAMES (`meter.go` ×3, and the `promCounter` /
`promUpDown` / `promGauge` / `promHistogram` helpers plus the literals in the
exporter tests). An unkeyed literal anywhere would have stopped compiling the
moment the field landed. `TestMetricEnvelopesCarryADescription` documents the
property by writing all three keyed.

**A fourth `Descriptions map[string]string` on `SnapshotValue` was rejected**,
even though it touches one published shape instead of three. It would let a
hand-built snapshot describe a metric that does not exist, it would make every
exporter perform a second lookup keyed on a name it is already holding, and it
would put the description somewhere the OTel model does not — losing the
property ADR 0044 §Decision 9 shaped the snapshot for, that the mapping to a
wire is a **walk** and not a reconstruction.

### 5. What each exporter does — present, absent, and adversarial

| | present | absent | escaping |
|---|---|---|---|
| **prometheus** | `# HELP <name> <text>` **above** `# TYPE`, inside the same header-per-name step | **no line at all** | `\` → `\\`, LF → `\n`, and the double quote deliberately **not** escaped |
| **otlpjson** | `Metric.description`, field 2, between `name` and the data oneof | **field omitted** | `encoding/json` owns it |
| **text** | ` description="<text>"` last on the `# metric` header | **no key at all** | `\`, `"` and LF, via the exporter's own `appendEscapedValue` |

**Absent means omitted, everywhere.** `# HELP name ` with nothing after it is
the placeholder the old comment refused to invent, and it would still be one;
the field-absent OTLP payload is byte-identical to the pre-ADR one; the text
header keeps the shape every existing grep depends on. The undescribed document
of all three exporters is pinned as unchanged.

**The Prometheus docstring gets its own escape function**, and the difference
from a label value is the whole reason: a label value is a **quoted token**, so
a `"` inside it would close the value early and must be escaped; a HELP
docstring is the **unquoted remainder of the line**, the format names only "the
backslash and the line feed", and escaping the quote anyway would put a literal
backslash into the help text an operator reads. Corrupting prose to defend
against a delimiter that is not there is not a safe default, it is a bug with
good intentions. The line feed is the one that matters: unescaped, it ends the
comment and the rest of the description parses as a **sample line** — a forged
series, the same hazard the adversarial label-value test already pins.

**Exactly one `# HELP` and one `# TYPE` per name** is the format's own rule, and
it holds structurally: both lines are written in the header-per-name step that
the name-keyed snapshot shape made free, nowhere near the per-series loop. A
64-series family is the regression test.

### 6. OTLP: `description` is omitted when empty, and that is the opposite call from ADR 0048's three

ADR 0048 emits three fields at their zero **because presence IS the meaning**:
`asInt`/`asDouble` are oneof members (an omitted oneof means "no case
selected", not "the default"), a histogram point's `sum` is `optional double`
(explicit presence), and `isMonotonic` would otherwise vanish exactly when it
carries the surprising answer.

`description` has neither property. It is a plain proto3 string with **no**
presence, so `""` and absent are literally the same value to a receiver, and an
empty description has no surprising answer — it means nobody wrote one. Omitting
it is also what the proto3-JSON default mapping does with a default-valued
field. So it is `omitempty`, and the reason is in the schema rather than in
taste.

### 7. The observation path pays nothing, and it is measured

The claim ADR 0044 defends — zero allocations per observation — is the reason
for §Decision 1's shape, so it is gated rather than asserted:

- The `descriptions map[string]string` lives on the meter, guarded by the
  existing `mu`, and is **nil until the first `Describe`**. A nil map reads as
  the empty one in Go, so an undescribed meter allocates nothing for the feature
  and `Collect` still finds `""` for every name without a branch.
- Nothing on the fetch path reads it. `BenchmarkCounterLookup_3Attrs_Described`
  (191.4 ns, 0 allocs) against `BenchmarkCounterLookup_3Attrs` (188.9 ns,
  0 allocs) is the falsifiable version of that sentence, and
  `TestDescribedMeterLookupIsAllocationFree` is the gate.
- The description is read **once per instrument NAME per collection** — 1000
  described names cost 275 µs against 249 µs undescribed, about 25 ns per name
  per scrape, one map lookup, charged to a scraper that runs every 10–60 s.
- The snapshot grew one `string` header per metric NAME (16 bytes), not per
  series and not per observation. **Allocation counts did not move** anywhere.

It also stays out of `nameState`, which carries an instrument KIND: a
description implies no kind, and storing it there would have forced a caller to
mint the instrument before documenting it.

## Consequences

- The SDK stops claiming a model it does not implement. `Metric.description` is
  the last of `Metric`'s three fields the domain was missing that any of its
  wires can carry; `unit` is deferred by name below.
- The Prometheus connector gains the **only** thing on its losses table that
  moves in the other direction: everything else there is something the format
  cannot carry, and this is something it has always been able to.
- `pkg/v1/metrics` grows one alias (`Describer`) and two sentinels. `Meter`,
  `FullMeter`, `NewMeter`'s signature and every instrument accessor are
  unchanged.
- Three published shapes changed under the v0 licence, on the record, with the
  audit's group-B cost acknowledged.
- The new code range is `0.2.9.7`–`0.2.9.8`, inside the `0.2.9.*` block
  `internal/core/metrics` already owns, so the ADR 0035 ownership table needs no
  entry — only `docs/error-codes.yaml` gains two rows.

## Deferred

- **`Metric.unit`.** It is a second decision, not a second field: on the
  Prometheus wire a unit becomes a **name suffix** (`_seconds`, `_bytes`) under
  the interoperability specification's own translation rules, which means
  adopting it changes metric NAMES — and this connector refuses non-injective
  name rewriting everywhere else. It needs its own ADR.
- **`Metric.metadata`** (field 12, `repeated KeyValue`). Nothing in the SDK
  produces one, and an always-empty repeated field is a placeholder (rule 5).
- **A length bound on a description.** None of the three wires imposes one, and
  a very long docstring is a payload-size question rather than a correctness
  one. Said here so its absence reads as a decision.

## Why not

- **Add `description` as a parameter to `Counter`/`Gauge`/`Histogram`.**
  Rejected: `Meter` is frozen, and the seven-method sibling that would replace
  it attaches a name's documentation to a call site and spends the observation
  path's budget on prose. §Decision 1.
- **Fold `Describer` into `FullMeter`.** Rejected: a union is still an
  interface. §Decision 2.
- **Make `Describe` return an `error`.** Rejected: it would be an error nobody
  can act on at runtime, on a call that runs at start-up, for a mistake that is
  a literal at a wiring site. The package already panics on the same class of
  defect (`InstrumentKindConflict`, `InvalidAttribute`), and a returned error
  here would be the one every caller writes `_ =` in front of.
- **Keep the first description and log a warning, as the OTel SDK spec says.**
  Rejected: no logger at this layer, and quiet first-wins is the failure mode
  the refusal exists to prevent. §Decision 3.
- **Emit `# HELP <name> <name>` when there is no description.** Rejected — this
  is the placeholder the original comment already refused, and the format makes
  HELP optional precisely so that nobody has to.
- **Emit `"description": ""` in OTLP for symmetry with `isMonotonic`.**
  Rejected: the two fields differ in the schema, not in style. §Decision 6.
- **Reuse `appendEscapedValue` for the HELP docstring.** Rejected: it escapes a
  third character the format does not, which would corrupt the prose.
  §Decision 5.
- **Add a `metrics.Describe(m, name, text)` package helper so callers need no
  assertion.** Rejected: a helper that swallows the `ok` hides the one fact the
  assertion exists to reveal, and a helper that returns it is the assertion with
  extra steps. This is not `events.On[E]`, which exists because Go methods
  cannot take type parameters — there is no mechanical obstacle here.
- **Put the descriptions in a fourth map on `SnapshotValue`.** Rejected:
  §Decision 4.

## References

- `internal/core/metrics/describer.go` — the port
- `internal/core/metrics/snapshot_value.go` — `Description` on the three envelopes
- `internal/service/metrics/meter_describe.go` — the implementation and its two panics
- `internal/service/metrics/exporter_prometheus.go` — `appendHelpLine` + `appendEscapedHelp`
- `internal/service/metrics/otlp_request.go` — `otlpMetric.Description`
- `internal/service/metrics/exporter_text.go` — `appendMetricHeader`
- `internal/service/metrics/BENCH.md` §ADR 0067 — the cost
- `docs/pre-v1-published-shape-audit.md` §B — the three shapes and their price
- OpenTelemetry metrics data model — <https://opentelemetry.io/docs/specs/otel/metrics/data-model> (description is non-identifying)
- OpenTelemetry Prometheus/OpenMetrics compatibility — <https://opentelemetry.io/docs/specs/otel/compatibility/prometheus_and_openmetrics> ("descriptions become HELP metadata"; one HELP per name)
- Prometheus text exposition format 0.0.4 — <https://prometheus.io/docs/instrumenting/exposition_formats> (HELP escaping: backslash and line feed only)
- `opentelemetry/proto/metrics/v1/metrics.proto` — `string description = 2;`
