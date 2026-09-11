# internal/service/metrics/

## Purpose

In-memory `Meter` + lock-free instruments
(`Counter`/`UpDownCounter`/`Gauge`/`Histogram` and the three observable
families) implementing `core/metrics`, plus three stdlib Exporters — a **text**
diagnostic that renders the whole OTel model, a **Prometheus** text-exposition
**connector** that deliberately does not, and **OTLP/JSON**, the native wire the
model was adopted for, which loses nothing. All three are registered to
**stderr** on import (ADR 0030); the OTLP/**HTTP** emitter is constructed
explicitly and never registered. Stdlib-only, cross-OS. ADR 0027 / ADR 0044 /
ADR 0048 / ADR 0067. Emits core sentinels `0.2.9.*` and owns block `0.3.45.*` for what a
wire format refuses.

Instruments are keyed by name **and typed attribute set** — one name plus one
attribute set is one **series** — with a per-name cardinality bound that folds
the excess into a single aggregated overflow series.

## Contents

| File | Surface |
|---|---|
| `meter.go` | `memMeter` + `NewMeter` / `NewMeterWithConfig` / `newMemMeter` + `Collect` (observables, delta consumption, arena layout, per-name sort) |
| `meter_observable.go` | `observer` + the three `Observable*` registrations + `runObservers` + `observeSum`/`observeGauge` |
| `meter_describe.go` | `Describe` — the `core/metrics.Describer` half: the lazily-created `descriptions` map, the idempotent path, and the two panics |
| `carved.go` | `carved[V]` — one instrument name's slot in a collection arena |
| `series.go` | series identity: `sortAttrs`, `appendSeriesKey`, `cloneAttrs`, `compareAttrs` |
| `series_store.go` | `seriesStore[T]` — one snapshot group's series map, `lookup` (read lock) + `admit`/`overflow` (write lock) |
| `series_entry.go` | `seriesEntry[T]` — one live series: name, owned attribute set, instrument |
| `name_state.go` | `instrumentKind` (7) + `outputGroup` (3) + `nameState` (kind binding, series tally, cached overflow key) + `bindName` |
| `config.go` | `MeterConfig` (bound, temporality, resource, scope, clock) + `resolve` + `DefaultMaxSeriesPerInstrument` |
| `sum.go` | `memSum` — the storage behind BOTH counter kinds (atomic.Int64 + monotonic/observed flags + delta bookkeeping) |
| `gauge.go` | `memGauge` (atomic float64 bits, CAS) |
| `histogram.go` | `memHistogram` (sorted bounds + atomic counts/sum, delta-consuming snapshot) |
| `exporter_text.go` | `textExporter` + default **stderr** `Text` + `NewTextExporter` |
| `exporter_otlpjson.go` | `EncodeOTLPJSON` (the ENCODER — snapshot to bytes, no I/O) + the proto3-JSON scalar types (`otlpInt64`/`otlpUint64`/`otlpDouble`) + the two refusals + `otlpJSONExporter` + default **stderr** `OTLPJSON` + `NewOTLPJSONExporter` |
| `otlp_request.go` | the OTLP payload TREE — a Go mirror of the four `.proto` files, in schema field-number order, restricted to the fields this SDK produces |
| `exporter_otlphttp.go` | the EMITTER, and the only `net/http` in this package: `NewOTLPHTTPExporter` + `OTLPRetryable` + `OTLPMetricsPath` + `DefaultOTLPTimeout`/`DefaultOTLPMaxResponseBytes` + endpoint refusal + response classification + the default client's own connection pool (`newOTLPTransport`) |
| `otlphttp_config.go` | `OTLPHTTPConfig` (endpoint, client, headers, timeout, response cap) |
| `otlp_export_response.go` | `ExportMetricsServiceResponse` + `ExportMetricsPartialSuccess` + `otlpLenientInt64`, the number-OR-string 64-bit decoder the specification requires |
| `exporter_prometheus.go` | `prometheusExporter` + default **stderr** `Prometheus` + `NewPrometheusExporter` + the two name grammars |
| `codes.go` / `errors.go` | `0.3.45.*` (INVALID_METRIC_NAME, INVALID_LABEL_NAME, RESERVED_LABEL_NAME, UNSUPPORTED_TEMPORALITY, OTLP_UNRESOLVED_TEMPORALITY, OTLP_INVALID_BUCKET_LAYOUT, OTLP_ENDPOINT_INVALID, OTLP_EXPORT_REJECTED, OTLP_EXPORT_UNAVAILABLE, OTLP_PARTIAL_SUCCESS) |

## Series identity

A series key is `byte(instrumentKind)`, then `uvarint(len(name)) name`, then for
each attribute **sorted by key**: `uvarint(len(k)) k`, a **kind tag**, and the
value — length-prefixed for a string, one canonical byte for a bool, eight
big-endian bytes for an integer or a double.

- **Sorted**, because an attribute set is a set. Without it `{a,b}` and `{b,a}`
  become two series that split one metric's total, and spend two slots of the
  bound on one identity.
- **Length-prefixed, not delimiter-separated.** An attribute value is data — a
  route, a tenant id, a header. With a delimiter, a caller who can influence one
  value can forge another series' key and have two unrelated series silently
  accumulate into one. Length prefixes make the encoding injective.
- **Kind-tagged**, which is what typed attributes added. `String("v", "1")` and
  `Int64("v", 1)` render identically on any wire with one value type; without
  the tag they would be one series here too, which is the same
  forge-by-collision hazard one level up. The encoding lives on `AttrValue`
  (`AppendIdentity`) so every `Meter` implementation inherits it.
- **Opened by the INSTRUMENT KIND**, which the OTel sum model made necessary:
  four instrument kinds share one store because a Counter and an UpDownCounter
  produce the same point shape. Without the leading byte, `Counter("x")` and
  `UpDownCounter("x")` would resolve to the same key, the second call would HIT
  the read lock, and the cross-kind conflict would never reach `bindName` — one
  name would silently carry both monotonicities. One byte on a stack buffer buys
  the check back without a second map read per observation.

An empty attribute key, the same key twice, or a value no constructor ever set
**panics** with `InvalidAttribute` (`0.2.9.4`) — the same call the meter already
makes for a cross-kind name reuse, and safe for the same reason: a key and a
kind are both written at the call site, so they are wrong on the first call or
never. The alternative is a series no exporter can emit (Prometheus and OTLP
both reject an empty attribute name) failing far away, inside the component the
SDK told the caller to stop thinking about.

## Temporality: what `Collect` does

`MeterConfig.Temporality` is resolved once, at construction (§Configuration).

**Cumulative** — the default and what an unconfigured meter *does*: every
accumulator is READ, `SnapshotValue.StartTime` repeats across collections, and a
reader differences successive scrapes itself.

**Delta** — `Collect` becomes a MUTATION. Every synchronous sum is
`Swap(0)`-ed, every histogram bucket and both its totals with it, and
`StartTime` advances to the previous collection's `Time`. Read-and-reset is one
atomic step per slot precisely so no observation lands in two windows; the
several slots of a histogram are still separate atomics, so a `Record` racing a
collection can put its bucket in one window and its sum in the next — the same
skew the read path already had, and the instruments are lock-free on purpose.
What cannot happen is double counting.

Consequences, stated rather than discovered:

- **A delta meter has exactly one reader.** `Collect` is serialised against
  itself by a dedicated `collectMu`, so each window is whole; two *concurrent*
  collections would still split the observations between them, which is why the
  contract says one reader. `TestConcurrentCollectDoesNotSplitADeltaWindow` sums
  eight collections back to what was recorded.
- **A series that saw nothing in a delta window reports zero**, rather than
  disappearing. Omitting it would make a series flicker in and out of a
  dashboard, and the cardinality bound already keeps the set finite.
- **An OBSERVED series is never swapped.** Its callback already stored the right
  number for the window (§Observables), and swapping would report the same delta
  twice — once as itself, once as its own negation.
- **A gauge is untouched by either setting.** It carries no temporality, in this
  package and in the OTel model.

## Observables

`ObservableCounter` / `ObservableUpDownCounter` / `ObservableGauge` register a
callback; `Collect` runs every one of them **before** taking the snapshot lock,
because a callback resolves series through the ordinary fetch path and would
otherwise deadlock behind its own collection.

- The callback reports the **absolute** total (the OTel contract). Under
  cumulative that value IS the report; under delta the meter stores
  `absolute − previous` and remembers `absolute`. `previous` is a plain field,
  not an atomic, because `collectMu` makes `Collect` the only writer.
- **Registrations accumulate**, as the OTel API specifies: a second callback
  under one name adds to the first rather than replacing it, so two packages can
  contribute to one instrument.
- **A nil callback is dropped at registration**, not stored: calling it during a
  scrape would panic far from the wiring that caused it. The NAME is still
  bound, so a later synchronous fetch of it still conflicts.
- **The cardinality bound applies**, and it bites harder here: a loop inside a
  callback can mint a thousand series in one collection. Past the bound every
  further attribute set lands in ONE overflow series, and because an observable
  **overwrites** rather than adds, that series shows the last set reported and
  not their total. An observable's attribute set should be small and fixed.
- A callback runs inline, so a slow one is a slow scrape for every instrument,
  and it must not call `Collect` on its own meter.

## Configuration

`MeterConfig.resolve` clamps or refuses every knob; none is left inert
(ADR 0031).

| Knob | Unset | Rule |
|---|---|---|
| `MaxSeriesPerInstrument` | `DefaultMaxSeriesPerInstrument` (2000) | non-positive **clamps**; there is no "unbounded" |
| `Temporality` | `Cumulative` | unset **clamps** — it describes what the meter does; a cast value **refuses** (`InvalidTemporality`) |
| `Resource` | `service.name=unknown_service` | **clamps** to the value the OTel spec mandates for exactly this case |
| `Scope` | `Name = DefaultScopeName` | **clamps** to the one truthful default; `Version` stays empty because the spec makes it optional |
| `Clock` | `clock.System` | the same default every other port in this SDK takes |

## Cardinality policy

`MaxSeriesPerInstrument` bounds the distinct attribute sets **one instrument
name** may hold — per name, so one exploding attribute on `http_requests_total`
cannot starve `db_queries_total` of the series it needs. Default
`DefaultMaxSeriesPerInstrument = 2000` (the figure the OpenTelemetry SDKs
settled on for the same problem).

**Non-positive clamps to the default.** It does not mean unbounded, and there is
no setting that does (ADR 0031). A caller who leaves the knob at zero has not
decided that memory is free; they have not yet learned the question exists.
A caller who genuinely wants a huge bound types a huge number, where a reviewer
can see it.

**Past the bound, a new attribute set is FOLDED, not rejected and not dropped.**
It goes into one aggregated series per name carrying
`sdk_metric_overflow=true` — a **bool** attribute now that the model has types;
before, it had to be the string `"true"` because a value was a string everywhere
the snapshot was going, and that reason expired. The Prometheus rendering is
byte-identical either way. The series sits outside the bound as one extra slot.
What that buys and what it costs:

| | |
|---|---|
| Memory is bounded | the point — an unbounded attribute set is a process-killing leak |
| Nothing is lost silently | a counter's grand total stays correct; every increment lands somewhere |
| The breakdown IS lost | irrecoverably — a folded observation's own attributes are gone |
| An OBSERVABLE loses more | its reports overwrite rather than add, so the folded series shows the last set reported |
| Identity is arrival-order dependent | the first N attribute sets win, so two replicas can fold different sets and disagree about what is visible |
| The condition is visible | the overflow series shows up in every snapshot from then on, so an operator reading a dashboard learns their attributes blew up |
| Overflow is allocation-free | see BENCH.md — otherwise the bound would trade a leak for GC pressure with the same cause |

Rejected alternatives: a **typed error** cannot be delivered from an accessor
whose signature hands back a Counter without changing every call site (the same
shape problem ADR 0031 solved for the resilience constructors, but here there is
no `Run` to fail later); **dropping** the observation is exactly the inert
behaviour ADR 0031 bans; **evicting** an admitted series would make the visible
set flap with traffic and lose the evicted totals outright.

`OverflowAttrKey` is reserved by convention, not enforced. A caller who passes
it explicitly as a bool writes into the overflow series. Enforcing it would cost
a comparison per attribute on the lookup path to prevent a collision nobody
reaches by accident.

## The text exporter

A **lossless** diagnostic: it is the one place a caller can see the whole model.

```
# resource service.name="orders"
# scope name="github.com/acme/orders" version="1.4.0"
# window start="2026-01-02T03:04:05Z" end="2026-01-02T03:04:15Z"
# metric http_requests_total sum cumulative monotonic description="Requests served"
http_requests_total{cached=true,method="GET",status=503} 3
# metric in_flight gauge
in_flight 2.5
# metric latency histogram cumulative
latency 42
```

One `# metric` header per instrument name, then that name's series — one pass
over the snapshot, because the snapshot is keyed by name. A gauge's header
carries neither temporality nor monotonicity, because a gauge has neither.

`description="…"` goes **last** so that the qualifier positions every existing
grep depends on do not move, and is omitted entirely when there is none — the
same call `# scope`'s optional `version` already makes. This exporter RENDERS it
because rendering the whole model is what the file is for: an exporter that
dropped the description would answer "did my `Describe` call reach the
snapshot?" with silence, which is the one question a diagnostic exists to
settle. It is QUOTED here, so unlike the Prometheus docstring it escapes the
double quote too — the two exporters follow the two grammars they are writing,
which is not an inconsistency. A
string attribute value is quoted and escaped; a bool, integer or double is
printed **bare**, so the attribute's TYPE is visible rather than flattened the
way a wire format flattens it. A histogram renders its observation count only —
the bucket layout is reachable through the Snapshot API, and this is a
diagnostic, not a wire format.

## The Prometheus text exposition connector

Reference: the **Prometheus text-based exposition format**, version `0.0.4`
(`text/plain; version=0.0.4; charset=utf-8`) —
<https://prometheus.io/docs/instrumenting/exposition_formats>. Everything in
this section is that specification, not a house convention; where the two could
differ, the tests carry values copied out of the specification's own example
document.

**Shape.** One `# TYPE <name> <kind>` per instrument name, then that name's
series. That is exactly one pass over `SnapshotValue`, because the snapshot is
keyed by name — no regrouping, no composite key to re-parse (see
`internal/core/metrics/CLAUDE.md` §The snapshot shape). The format permits only
one `TYPE` line per name and requires it to precede the samples, which is what
the header-per-key walk gives for free.

**A non-monotonic sum is typed `gauge`, not `counter`.** That is the
OpenTelemetry-to-Prometheus mapping and it is not a convenience: `rate()` on a
counter treats every decrease as a process restart and re-extrapolates from
zero, so an UpDownCounter exposed as a counter would report a fabricated spike
each time its value fell.

**`# HELP`, since ADR 0067.** That day arrived: the `Meter` grew a `Describer`
sibling, and the OTel-to-Prometheus interoperability specification says outright
that "OTLP metric point descriptions become HELP metadata". The line lands
exactly where the old comment promised — above `# TYPE`, inside the same
header-per-name step, so the format's own rule ("only one HELP line may exist
for any given metric name") holds for free.

An **absent** description still emits nothing at all. HELP is optional in the
format, and `# HELP name ` with nothing after it is the placeholder this
exporter refused to invent for as long as there was nothing real to print
(rule 5). The undescribed document is byte-for-byte what it was before ADR 0067,
which is pinned.

**The docstring is escaped with the format's OWN two escapes** — a backslash
doubles, a line feed becomes `\n` — and a double quote is deliberately left
alone. A label value is a QUOTED token, so a quote inside it would close the
value early and `appendEscapedValue` escapes it; a HELP docstring is the
unquoted remainder of the line, the format names only those two characters, and
escaping the quote anyway would put a literal backslash into the help text an
operator reads. `appendEscapedHelp` is a separate function for that one
difference. The line feed is the dangerous one: unescaped, it ends the comment
and the rest of the description parses as a **sample line** — the same forged-
line hazard the adversarial label-value test pins, one line higher up.

**Histograms.** A histogram family is `_bucket{le="…"}` + `_sum` + `_count`,
and `le="+Inf"` is mandatory. The meter stores a **per-bucket** count (`Record`
increments exactly one slot); the format wants a **cumulative** one, so the
exporter carries a running total across the ladder. `le="+Inf"` reports that
running total rather than `HistogramValue.Count`: at rest the two are equal,
and under a concurrent `Record` they can differ by the observations in flight
because `Record` bumps its bucket and the total as two separate atomics.
Deriving `+Inf` from `Count` instead could put it BELOW the bucket beneath it —
a non-monotonic ladder, which is the worse of the two violations. Nothing here
restores atomicity; only a lock would, and the instruments are lock-free on
purpose. A non-finite declared bound is **skipped** (its count still rides the
running total): `+Inf` is already the mandatory last line, so emitting it again
would forge a duplicate series, and `NaN` is not an ordering.

**Values.** `strconv.FormatFloat(v, 'g', -1, 64)`. The format defines a value as
"a float represented as required by Go's `ParseFloat()`" and names `NaN`,
`+Inf`, `-Inf` — which is precisely what that call emits, including the
exponent-notation threshold that produced the specification's own published
`1.7560473e+07`. Shortest-round-trip is also what keeps two distinct bucket
bounds from ever spelling the same `le`, i.e. from forging a duplicate series.

**Names are REFUSED, never rewritten.** A metric name must match
`[a-zA-Z_:][a-zA-Z0-9_:]*` and a label name `[a-zA-Z_][a-zA-Z0-9_]*` — the
colon is legal in the first and not in the second. A name outside its grammar
fails the whole `Export` with `INVALID_METRIC_NAME` / `INVALID_LABEL_NAME`, and
**nothing is written**.

| | |
|---|---|
| Why not transliterate | mapping the offending bytes to `_` is not injective: `a.b`, `a-b` and `a b` all become `a_b`, so two distinct instruments silently merge into one family — and a counter and a histogram can merge into one name. That is the same forge-by-collision hazard the length-prefixed series key exists to prevent |
| Why not skip the offender | that is the inert behaviour ADR 0031 bans: the misconfiguration would never surface |
| Why refusing is safe | an instrument name is STRUCTURE — a literal at the call site, constant for the process. It is wrong on the first scrape or never, exactly like the attribute KEY the meter already panics on. It cannot start failing in production because of traffic |
| Why the whole document | a truncated exposition parses as a complete one, so its missing series look like series that stopped existing |

`__`-prefixed label names are refused as `RESERVED_LABEL_NAME` although they are
syntactically legal: Prometheus reserves them for its own internal labels and
drops them during relabelling, so the series would silently lose a dimension at
the server. `le` is refused on a **histogram** only, where it would be a second
`le` on the bucket line, i.e. a duplicate label name the format rejects.

**Escaping.** Exactly three sequences in a string attribute value: `\` → `\\`,
`"` → `\"`, newline → `\n` (shared with the text exporter through
`appendEscapedValue`). There is no fourth, deliberately: the 0.0.4 parser
**errors on an unknown escape sequence**, so emitting `\r` or `\t` would cost
the whole scrape rather than one label — strictly worse than passing the byte
through, which cannot forge a line because only an unescaped newline terminates
one. Only a STRING value ever needs it: a bool, an integer and a double render
from `[0-9a-zA-Z+-.]` alone, which is what lets `appendPromLabels` escape one
case and append the other three verbatim. Names need no escaping at all — that
is the second thing validation buys.

**The overflow series is emitted like any other.** `sdk_metric_overflow` is a
legal label name by construction (`core/metrics/attr_value.go` spells it with
underscores for this reason), so the folded series reaches the wire and an
operator can alert on `{sdk_metric_overflow="true"}`. Hiding it would restore
the silent failure the cardinality policy exists to avoid.

**Validation runs per scrape.** It is O(bytes of names + attribute keys), which
is noise next to the formatting, and this is the only layer that can do it: the
meter does not know which exporter its snapshot is going to.

## What the Prometheus connector loses

This exporter is a **deliberately lossy connector**, not a rendering of the
SDK's data model. The model is OpenTelemetry's; the exposition format predates
most of it and has no field for the rest. It is kept because a Prometheus
deployment is a real destination — on the condition that its losses are named.
Each row below has an executable test.

| Lost | What happens | Why it is a loss and not a bug |
|---|---|---|
| **Temporality** | a delta snapshot is **REFUSED**, `UNSUPPORTED_TEMPORALITY` (`0.3.45.4`), nothing written | the format has no temporality field and a Prometheus server reads every counter as cumulative — `rate()` differences successive scrapes itself. Handing it deltas means differencing numbers that are already differences (the reported rate becomes the second derivative) and every window smaller than the last reads as a counter reset. Nothing in the document would say so and no dashboard would look broken. Refusing is safe for the same reason refusing a name is: a meter's temporality is fixed at construction, so this fails on the first scrape or never |
| **The attribute's TYPE** | every value is rendered to a string; `Int64("v", 1)` and `String("v", "1")` — two series in the snapshot — become ONE series on the wire | a Prometheus label value **is** a string; there is no second option and no encoding avoids it. Unlike a mangled NAME, there is no injective alternative here, which is exactly why this one is documented rather than refused. The rendering follows the OTel→Prometheus interoperability specification |
| **`Resource`** | dropped entirely; no `target_info` metric is emitted | `service.name` cannot even be SPELLED — a Prometheus label name is `[a-zA-Z_][a-zA-Z0-9_]*` and the dot is outside it. The interoperability spec answers this by mangling the key; this connector refuses non-injective name rewriting everywhere else in this very file, and consistency with our own rule beats consistency with theirs. Whatever producer identity a Prometheus deployment has comes from the scrape target's own labels (`job`, `instance`), which the server attaches |
| **`InstrumentationScope`** | dropped entirely | same wall: the format has no place for a second identity, and `otel_scope_name` would be an invented label the caller never wrote |
| **OTel-conventional attribute KEYS** | `http.request.method` is **REFUSED** by name (`INVALID_LABEL_NAME`) | the sharpest consequence of adopting the model: the dotted spelling is what the semantic conventions specify, and this wire cannot carry it. The refusal is loud and deterministic — a key is a literal at the call site — which is the whole reason refusing beats mangling |
| **Exemplars** | none | the SDK produces none at all (ADR 0044 §Deferred): no tracing domain, so no span id to attach |

The description is the one thing on that list's opposite side: it is the only
part of the OTel Metric this connector gained rather than lost, because the
exposition format has had a place for it since before OTel existed.

## The OTLP/JSON encoder

Reference: the OTLP specification (<https://opentelemetry.io/docs/specs/otlp/>),
§JSON Protobuf Encoding for the encoding rules and §OTLP/HTTP Request for the
path and content type; `opentelemetry/proto/{metrics,common,resource}/v1/*.proto`
and `collector/metrics/v1/metrics_service.proto` for the shapes and field
numbers. Everything below is those documents. ADR 0048.

This is the wire the data model was adopted for, and unlike the Prometheus
connector it is **lossless**: temporality, the attribute's type, the `Resource`
and the `Scope` all travel.

`EncodeOTLPJSON(SnapshotValue) ([]byte, error)` is the encoder and does no I/O
at all — it returns exactly the body of a POST to `/v1/metrics`. That is the
whole boundary between the two surfaces: an encoding defect and a network defect
are never the same investigation, and the conformance test needs no server.

**The mapping is a walk, not a reconstruction**, which is what ADR 0044
§Decision 9 shaped `SnapshotValue` for. Sums, then gauges, then histograms, each
in ascending name order — the same walk the text exporter takes, so the document
renders byte-identically twice in a row. The two single-element levels
(`resourceMetrics[0]`, `scopeMetrics[0]`) are single because one Meter has one
Resource and one Scope. `Counts` rides through unchanged: the snapshot is
already **per bucket**, which is what `bucketCounts` wants — the conversion the
Prometheus connector has to do does not exist here.

**Four protobuf-JSON rules, each of which is invisible until a receiver drops
the payload.** OTLP/JSON is protobuf-JSON, not "the obvious JSON for this
struct":

| Rule | Consequence of getting it wrong |
|---|---|
| Field names are **lowerCamelCase** (`dataPoints`, `startTimeUnixNano`) | a receiver MUST ignore unknown fields, so a snake_case payload arrives as an empty request and is **accepted** |
| 64-bit integers are **decimal strings** — `"4200"`, not `4200` — and either form is accepted on decode | a JSON number is a double in most parsers: a counter past 2⁵³ silently loses its low bits |
| Enums are **integers**. This is OTLP *overriding* generic proto3 JSON, which uses names | `"AGGREGATION_TEMPORALITY_DELTA"` is an unknown value and reads as UNSPECIFIED |
| A receiver MUST ignore unknown fields | applies to us as a receiver too — the collector's response is parsed leniently |

`AGGREGATION_TEMPORALITY_UNSPECIFIED = 0`, `DELTA = 1`, `CUMULATIVE = 2`, from
`metrics.proto`.

**Field order inside each message is the schema's FIELD-NUMBER order.**
protobuf-JSON imposes none, so any order is valid; field-number order is used
because it is *derivable from the document*, which is what makes the expected
bytes in the tests checkable against the `.proto` field by field rather than
against taste. The visible evidence that the order came from the schema:
`NumberDataPoint` puts `attributes` **after** the value, because it is field 7 —
it replaced a `labels` field that used to sit at 1.

**Three fields are emitted at their zero, because presence is the meaning.**
proto3-JSON says a serializer *should* omit a default-valued field; these three
are exceptions and each has a reason:

- **`asInt` / `asDouble`** are `oneof` members, which have explicit presence.
  Omitted, the data point selects no case at all — a counter sitting at 0 would
  encode as a point with no value.
- **`sum` on a histogram point** is declared `optional double`. Present-and-zero
  means the observations summed to zero; absent means no sum was recorded. This
  SDK always has one, so it is a pointer in the Go shape and always emitted.
- **`isMonotonic`** would legitimately be omitted when false, and is emitted
  anyway. It is the one field that tells a Counter from an UpDownCounter, and a
  field that vanishes exactly when it carries the surprising answer is one a
  reader cannot trust.

Everything the SDK does not produce is **absent**, not blank: `unit`,
`schemaUrl`, `flags`, `exemplars`, `droppedAttributesCount`, histogram
`min`/`max`, an empty `attributes` array, an absent scope `version` (rule 5).

- **`description`** (ADR 0067) is produced now, and it is omitted when empty —
  the OPPOSITE call from the three fields above, for a reason in the schema
  rather than in taste. It is `string description = 2;`: a plain proto3 string
  with no `optional`, so it has **no explicit presence**, and `""` is
  indistinguishable from absent to a receiver. The three always-emitted fields
  each have the property this one lacks — `asInt`/`asDouble` are oneof members,
  `sum` is `optional double`, and `isMonotonic`'s surprising answer is `false`.
  An empty description has no surprising answer: it means nobody wrote one.

**Two refusals, both structural.** Each aborts the whole document before a byte
is produced, for the reason the Prometheus connector aborts on a bad name: a
truncated payload is not a payload, and a receiver would reject the request
rather than the series that could not be spelled.

| Refusal | Why the wire cannot carry it |
|---|---|
| `OTLP_UNRESOLVED_TEMPORALITY` (`0.3.45.5`) — a temporality that is neither delta nor cumulative | the schema's own comment reads "UNSPECIFIED is the default AggregationTemporality, it MUST not be used". A receiver handed 0 drops the metric or guesses which window the number covers, and guessing is the failure ADR 0044 exists to prevent. A Meter resolves the knob at construction, so this is reachable only from a hand-built or cast snapshot — it fails on the first export or never |
| `OTLP_INVALID_BUCKET_LAYOUT` (`0.3.45.6`) — counts not one longer than the bounds, or bounds that are non-finite or not strictly increasing | both invariants are stated in `metrics.proto`. A non-finite bound breaks the second by construction: the bucket above the last declared bound is *already* `(bound, +infinity)`, so declaring `+Inf` forges a second, permanently empty bucket over the same range, and `NaN` is not an ordering. `Meter.Histogram` takes bounds from the caller and does not dedupe them, so all three are reachable |

**The Prometheus connector SKIPS a non-finite bound; this one refuses.** That is
not an inconsistency: there, `le="+Inf"` is already a mandatory separate line, so
skipping costs nothing. Here, skipping would break
`len(bucketCounts) == len(explicitBounds) + 1`, i.e. it would produce a malformed
payload rather than a merely lossy one.

**Non-finite doubles are named, not fatal.** `encoding/json` refuses `NaN` and
`±Inf` outright; proto3-JSON spells them `"NaN"`, `"Infinity"`, `"-Infinity"`. A
gauge is whatever was sampled, so a NaN reading must not fail an entire export.
Finite values render shortest-round-trip (`'g'`, `-1`), every form of which is a
valid JSON number — including the exponent notation `1e+21`.

**HTML escaping is OFF.** `encoding/json` escapes `<`, `>` and `&` into their
`\u00xx` forms by default, a defence for JSON embedded in a `<script>` element.
An OTLP body never is, and OTel-conventional attributes carry URLs (`url.full`,
`http.route`) whose query separator is exactly `&`. `json.Encoder` is the only
way to turn it off, and it appends a newline a single-document body must not
carry — hence `marshalOTLPJSON`.

**An unset `time.Time` encodes as 0**, and the guard is load-bearing rather than
defensive noise: `UnixNano`'s result is documented as *undefined* out of range,
and the zero `Time` returns `-6795364578871345152`, which cast to a `uint64`
nanosecond timestamp reads as the year 2339. Zero is what the schema means by an
unknown timestamp; a plausible wrong date is what no dashboard can detect. A
test asserts the garbage value never appears.

**The writer-bound exporter terminates each document with a newline**, so a
stream of exports is NDJSON. `EncodeOTLPJSON` does not — an HTTP body is one
document.

## The OTLP/HTTP emitter

`exporter_otlphttp.go` is the only file in this package that imports `net/http`.
It calls `EncodeOTLPJSON` and adds transport, nothing else.

It is **never registered**. The registry is reached by importing a package, and
arming a *network client* from an import is a step past the hazard ADR 0030
already refuses: there is no endpoint that could be a correct default, and a
wrong one turns every `Export` into a POST at whatever answers on that address.
A test asserts no OTLP/HTTP exporter appears in `AvailableExporters()`.

**The endpoint is a full URL used as-is** — no path is appended, no scheme
guessed — which mirrors the specification's own per-signal endpoint variable.
It is refused at construction (`OTLP_ENDPOINT_INVALID`, `0.3.45.7`) when it is
not absolute `http(s)` with a host and a non-root path. The path check earns its
keep: a bare `http://collector:4318` **connects**, answers 404, and looks
exactly like a collector that is up. Refusing at wiring is strictly earlier than
refusing at the first scrape, and — unlike the `resilience` constructors — no
published signature forces the refusal to be deferred into an always-failing
value, so the constructor returns `(Exporter, error)`.

**Bounds, in both directions that matter.**

| | |
|---|---|
| Round trip | `DefaultOTLPTimeout` = 10 s, the value the OpenTelemetry protocol exporter specification defaults `OTEL_EXPORTER_OTLP_TIMEOUT` to. Non-positive **clamps**; there is no "no timeout" setting, because a stalled collector must never wedge the scraping goroutine (ADR 0031) |
| Response body | read through `io.LimitReader` (`DefaultOTLPMaxResponseBytes`, 1 MiB). It is the one length a REMOTE party controls in this exchange — so the drain that recycles the connection after the verdict is bounded by the same default. It was not, and a collector streaming an endless body held `Export` forever whenever the client had no deadline |
| Request body | deliberately **not** capped: its size is a property of the caller's own cardinality, any SDK-chosen ceiling would be arbitrary, and the collector answers 413 for one it will not take |
| Redirects | refused with `http.ErrUseLastResponse` (CWE-918). A 30x would otherwise bounce the POST — `Authorization` header included — at whatever host the response names, past an allowlist that only saw the configured endpoint. The unfollowed 30x is classified as a permanent rejection, which is what a misconfigured endpoint is |
| A caller-supplied `http.Client` | used **as-is**, deadline, redirect policy and `Transport` included. It is the seam for a proxy, an mTLS identity or an SSRF allowlist, and second-guessing it would defeat the seam — so one riding `http.DefaultTransport` keeps the exposure below, and giving it a `Transport` of its own is how the caller removes it |
| The default client's pool | its OWN: a clone of `http.DefaultTransport`, proxy environment included, sharing none of its connections — see below |

**The default client owns its connection pool, because a shared one gave wrong
verdicts.** It used to ride `http.DefaultTransport`, and net/http puts a
BODILESS response's connection back in the idle pool *before* handing the
response to the waiting round trip; a `CloseIdleConnections` on that pool
landing in between closes the connection under it, and the round trip reports
`HTTP/1.x transport connection broken: http: CloseIdleConnections called` for an
answer that had already arrived. Every `httptest.Server.Close` and every
`http.DefaultClient.CloseIdleConnections` in the process is such a call. The
verdict was then `OTLP_EXPORT_UNAVAILABLE` whatever the collector said: a 200
became retryable — the caller's retry replays accepted data, which double-counts
a delta point — a 429 lost its `Retry-After`, and a 413 became retryable. CI saw
it as a `TestOTLPHTTPSurfacesRetryAfter` flake; with the OS threads
oversubscribed it failed 4 times in 800 runs of this file, and 0 in 600 since.
`TestOTLPHTTPDefaultClientOwnsItsConnectionPool` proves the isolation
deterministically — the emptying call between exports, and the connection must
survive it.

The clone is a snapshot taken at construction. Where `http.DefaultTransport` has
been replaced by something other than an `*http.Transport`, a fresh transport
with `Proxy: http.ProxyFromEnvironment` and net/http's 90 s idle timeout stands
in. `TestOTLPHTTPDefaultClientHonoursTheProxyEnvironment` drives both branches
through a real `HTTP_PROXY` — in a CHILD process, because net/http reads the
proxy environment once per process and never proxies loopback.
`TestOTLPHTTPProxyChild` is that child: it self-skips unless its parent set
`KTN_OTLP_METRICS_PROXY_CHILD`, so it is discovered and run like any test and
needs no tag, no `manual` target and no compensating lane (CLAUDE.md rule 12).

**Each exporter therefore owns a pool, and nothing closes it.** The `Exporter`
port has no lifecycle method and none was invented for this; idle connections
are reaped by the transport's idle timeout. One exporter per collector, built
once, is the intended shape — building one per export holds a pool per call
until that timeout.

**It does not retry, and that is a decision.** The specification asks a client to
honour `Retry-After` and otherwise back off exponentially;
`internal/service/resilience` already ships that policy, and a backoff hidden
inside `Export` would be a second one a caller cannot see, tune or cancel — with
no `context` to cancel it with. What this exporter supplies instead is the
**classification** a retry policy needs, in exactly the shape
`resilience.RetryConfig.Retryable` wants:

```go
resilience.NewRetry(resilience.RetryConfig{
    MaxAttempts: 3,
    BaseDelay:   time.Second,
    Retryable:   metrics.OTLPRetryable,
})
```

**Three verdicts, from the specification's own sections.**

| Response | Verdict | Retryable | Section |
|---|---|---|---|
| 2xx, `partialSuccess` unset or zero | `nil` | — | Full Success |
| 2xx, `rejectedDataPoints != 0` | `OTLP_PARTIAL_SUCCESS` (`0.3.45.10`) | **no** | Partial Success — "the client MUST NOT retry" |
| 429 / 502 / 503 / 504 | `OTLP_EXPORT_UNAVAILABLE` (`0.3.45.9`) | yes | Retryable Response Codes |
| any other 4xx/5xx, and an unfollowed 3xx | `OTLP_EXPORT_REJECTED` (`0.3.45.8`) | no | Failures — "all other 4xx or 5xx … MUST NOT be retried" |
| transport fault, no response | `OTLP_EXPORT_UNAVAILABLE` | yes | All Other Responses — "the client SHOULD retry" |

The retryable set is spelled out rather than derived from the status class,
because **5xx is not retryable as a class**: a 500 or a 501 means the same
request will fail the same way. Both halves have a test.

- **A partial success is an ERROR.** The status was 200 and data was lost.
  Returning `nil` would report a success that did not fully happen, on every
  scrape.
- **An unparseable 2xx body is a SUCCESS.** The 200 already said the request was
  accepted; turning a malformed or proxied response into a lost-data verdict
  would invent a failure forever. It is also the posture the specification asks
  a receiver to take in the other direction.
- **`rejectedDataPoints` decodes from a number OR a string** (`otlpLenientInt64`),
  because the specification says either is accepted on decode and collectors
  differ. A decoder that read one form would silently read every partial success
  as a full one against the other kind.

**Nothing untrusted is echoed.** The collector's `errorMessage` is decoded into
the response shape and deliberately **not** attached to the error: it is
unbounded remote-controlled text and an `errs` Field goes straight into
structured logs. The rejected COUNT is what an operator alerts on. `Retry-After`
is surfaced only in its **delta-seconds** form — converting an HTTP-date means
comparing the collector's clock to ours, which is the sort of quiet assumption
that surfaces months later as a retry storm. The endpoint never reaches a
rendered message: a transport cause is wrapped (so it stays reachable through
`errors.Unwrap`), and `errs` renders the Public string only, so the `*url.Error`
does not leak into a log line.

**Headers are set before `Content-Type`**, so the specification's
`application/json` always wins and a caller cannot mislabel the body by
accident. Header values are secrets: written, never read back, never echoed. The
map is cloned at construction so a later caller mutation cannot change the wire.

## Conventions

- **Lock-free instruments.** The meter takes only the READ lock to resolve an
  existing series; the write lock is held for creation alone. That is what makes
  a per-observation lookup viable at all — attributes move the lookup from
  start-up onto the request path.
- **Zero allocations on the lookup path**, attributed or not, and on the
  overflow path. The sorted attribute set and the encoded key live in stack
  arrays, and the map is indexed with `string(scratch)`, which does not copy. A
  key is converted to a `string` in exactly one place — `retain`, on insertion,
  where the map keeps it. Gated by `meter_alloc_test.go`; see BENCH.md.
- **`NewMeter` and `NewMeterWithConfig` are one-line wrappers over
  `newMemMeter`, and that is load-bearing.** Both must stay INLINABLE: inlining
  is what carries the concrete meter type to a caller's call sites, and that is
  what lets the compiler prove a variadic attribute slice does not escape.
  Behind an opaque interface it must assume it does, and every attributed
  observation costs one small heap allocation. Measured — growing either
  constructor past the inlining budget fails the alloc gate.
- **`admit` takes the series identity in FOUR separate arguments**, and the
  meter back-pointer lives on the store instead. Go's escape analysis is
  field-insensitive on a struct parameter, so grouping (name, kind, key, attrs)
  into one value makes the whole group escape the moment the name is stored on
  an entry — two allocations per observation. Measured.
- **The stores are keyed flat** by the whole series key, not nested by name: one
  map read resolves an attributed fetch where a nesting would cost two.
- **A description belongs to the NAME too, but NOT to `nameState`** — it lives
  in its own `descriptions map[string]string`, nil until the first `Describe`.
  A `nameState` carries an instrument KIND and a description is non-identifying
  and implies none, so storing it there would force a caller to mint the
  instrument before documenting it. The nil map READS as the empty one, so an
  undescribed meter allocates nothing and `Collect` still finds `""` without a
  branch. Nothing on the fetch path reads it; gated by
  `TestDescribedMeterLookupIsAllocationFree` and priced in BENCH.md §ADR 0067
  (~25 ns per described NAME per scrape, zero per observation).
- **Kind and bound belong to the NAME**, not the series (`nameState`) —
  attributes vary within one metric, its kind and its quota do not. Four
  instrument kinds map to one output GROUP, because a Counter and an
  UpDownCounter produce one point shape.
- **`Collect` carves one arena per group** into exactly-sized per-name windows,
  using the meter's own series tally, and yields them through a
  range-over-func so each collector wraps its window in its own metric type.
  Letting each name's slice grow on its own costs an allocation per name plus a
  doubling copy per many-series name — both scaling with cardinality, on the
  path a scraper walks every few seconds.
- **`Collect` is serialised against itself** by `collectMu`: it is a mutation
  under delta temporality and a mutation under any temporality once an
  observable is registered.
- **Registration via `var Text = metrics.RegisterExporter(...)`** (and
  `var Prometheus = …`, `var OTLPJSON = …`) — no `init()`.
- **An OTLP surface never touches the observation path.** `EncodeOTLPJSON`
  builds a message tree and hands it to `encoding/json`; that is fine because an
  export is a SCRAPE-rate operation, while the allocation budget this package
  defends is per OBSERVATION. Do not "optimise" the encoder by hand-appending
  bytes — the escaping `encoding/json` gets right for free is a correctness
  property (an attribute value is data), and there is no measurement asking for
  the trade.
- **All three registered defaults write to `os.Stderr`** (ADR 0030). Importing a
  package must not arm a writer on a stream the process may be using as a
  protocol channel; stdout is reachable only by asking for it explicitly with
  `NewTextExporter(name, os.Stdout)`. The temptation is stronger for the
  Prometheus exporter — an exposition document *looks* like something a caller
  wants on stdout — but a scrape endpoint hands the exporter its
  `http.ResponseWriter` and never touches the registered default, so nothing is
  gained by making the import dangerous. One regression test per surface. The
  OTLP/HTTP emitter goes one step further and is **not registered at all**: an
  import that arms a network client is worse than one that arms a writer,
  because there is no endpoint that could be a correct default.
- **The two TEXT exporters escape STRING attribute values** (`\\`, `\"`, `\n`)
  through the shared `appendEscapedValue`; the OTLP encoder does not need it,
  because `encoding/json` owns JSON string escaping and doing it twice would
  double every backslash on the wire. A value is data; unescaped, one containing a
  quote or a newline forges a line a reader parses as another series.
- Cross-OS: 100 % portable (sync/atomic/math/strconv/time/encoding-json/net-http).

## Do NOT

- Discard writer errors — each exporter buffers into `[]byte` then does one
  `Write` with a wrapped `EXPORT_FAILED` on failure.
- Add an `init()`.
- Point a *registered* exporter at `os.Stdout`. The import is invisible at the
  call site, so the default must be the stream nobody parses.
- Transliterate a metric or attribute key in the Prometheus connector. Mapping
  the offending bytes to `_` merges distinct instruments silently — see §The
  Prometheus text exposition connector.
- Emit a `target_info` metric to smuggle the Resource through. It needs the same
  non-injective mangling, on the one key (`service.name`) that most matters.
- Let a delta snapshot reach the Prometheus wire. It is refused, on purpose.
- Invent a fourth escape sequence. The 0.0.4 parser rejects anything but
  `\\`, `\"` and `\n`, so a `\r` would cost the whole scrape.
- Emit a placeholder `# HELP`, or one for an empty description. The format makes
  it optional precisely because there is not always a docstring to write.
- Escape a double quote inside a Prometheus `# HELP` docstring. It is not a
  quoted token; the format names `\\` and `\n` and nothing else, and a third
  escape would put a literal backslash into the help text.
- Put a description on a data POINT, or in the series key. It is non-identifying
  in the OTel data model — two streams differing only by their description are
  one stream.
- Thread a description through `Counter`/`Gauge`/`Histogram`. It would put a
  second string on the one variadic call the compiler has to prove
  non-escaping, and it would let two call sites disagree about the
  documentation of one metric while both look correct.
- Convert a series key to a `string` before a map read. `m[string(b)]` does not
  allocate; `k := string(b); m[k]` does, once per observation.
- Clone a series' attribute set inside `Collect`. It is shared with the snapshot
  on purpose (see `internal/core/metrics/CLAUDE.md` §The snapshot shape).
- Swap an OBSERVED series to zero during a delta collection. Its callback
  already stored the window.
- Grow `NewMeter` / `NewMeterWithConfig` past the inlining budget. See
  §Conventions.
- Add an "unbounded" cardinality setting.
- Register the OTLP/**HTTP** emitter, or give it a default endpoint. See
  §The OTLP/HTTP emitter.
- Build the default client without its own `Transport`. A nil one is
  `http.DefaultTransport`, which any code in the process can empty — see
  §The OTLP/HTTP emitter. And do not replace, wrap or clone the `Transport` of
  a caller-SUPPLIED client: that choice is the caller's.
- Drain a response without a bound, or bound the drain by the configured
  `MaxResponseBytes`: that knob caps what is read into memory, and a caller
  who lowered it must not lose connection reuse on a conforming response.
- Retry inside `Export`. `resilience` owns backoff; this package classifies
  (`OTLPRetryable`). A hidden loop cannot be tuned or cancelled by the caller
  who owns the scrape, and `Export` has no `context` to cancel it with.
- Emit `AGGREGATION_TEMPORALITY_UNSPECIFIED` (0). The schema says it MUST NOT be
  used; the encoder refuses instead.
- Emit an enum by NAME, or a 64-bit integer as a JSON number. OTLP/JSON forbids
  the first outright, and the second loses the low bits of anything past 2⁵³.
- Omit `asInt`/`asDouble`, the histogram `sum`, or `isMonotonic` when they hold
  their zero. Presence is the meaning — see §The OTLP/JSON encoder.
- Skip a non-finite histogram bound the way the Prometheus connector does. It
  breaks `len(bucketCounts) == len(explicitBounds) + 1`.
- Turn HTML escaping back on in the OTLP encoder, or reach for `json.Marshal`
  instead of the configured `json.Encoder`.
- Attach a collector's `errorMessage`, a `Retry-After` HTTP-date, or the
  endpoint to an error. All three are remote-controlled or secret; a Field goes
  into structured logs.

## Verification

```
bazel test --config=race //internal/service/metrics:metrics_test
bazel test --config=alloc //internal/service/metrics:metrics_test   # the !race alloc gates
# Fallback (no Bazel):
cd internal/service && GOWORK=off go test -race ./metrics/...
```
