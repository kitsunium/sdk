# internal/service/metrics/

## Purpose

In-memory `Meter` + lock-free instruments
(`Counter`/`UpDownCounter`/`Gauge`/`Histogram` and the three observable
families) implementing `core/metrics`, plus two stdlib Exporters — a **text**
diagnostic that renders the whole OTel model, and a **Prometheus** text-exposition
**connector** that deliberately does not. Both are registered to **stderr** on
import (ADR 0030). Stdlib-only, cross-OS. ADR 0027 / ADR 0044. Emits core
sentinels `0.2.9.*` and owns block `0.3.45.*` for what the wire format refuses.

Instruments are keyed by name **and typed attribute set** — one name plus one
attribute set is one **series** — with a per-name cardinality bound that folds
the excess into a single aggregated overflow series.

## Contents

| File | Surface |
|---|---|
| `meter.go` | `memMeter` + `NewMeter` / `NewMeterWithConfig` / `newMemMeter` + `Collect` (observables, delta consumption, arena layout, per-name sort) |
| `meter_observable.go` | `observer` + the three `Observable*` registrations + `runObservers` + `observeSum`/`observeGauge` |
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
| `exporter_prometheus.go` | `prometheusExporter` + default **stderr** `Prometheus` + `NewPrometheusExporter` + the two name grammars |
| `codes.go` / `errors.go` | `0.3.45.*` (INVALID_METRIC_NAME, INVALID_LABEL_NAME, RESERVED_LABEL_NAME, UNSUPPORTED_TEMPORALITY) |

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
# metric http_requests_total sum cumulative monotonic
http_requests_total{cached=true,method="GET",status=503} 3
# metric in_flight gauge
in_flight 2.5
# metric latency histogram cumulative
latency 42
```

One `# metric` header per instrument name, then that name's series — one pass
over the snapshot, because the snapshot is keyed by name. A gauge's header
carries neither temporality nor monotonicity, because a gauge has neither. A
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

**No `# HELP`.** HELP is optional in the format and carries a *docstring*; the
`Meter` records no description for an instrument, so the only HELP this exporter
could write is the metric name repeated back or a fixed sentence restating the
TYPE line. Both are placeholders, and this repo does not ship placeholders
(rule 5). The day `Meter` grows a description, HELP lands on the line above
`# TYPE` and nothing else about the document changes.

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
  `var Prometheus = …`) — no `init()`.
- **Both registered defaults write to `os.Stderr`** (ADR 0030). Importing a
  package must not arm a writer on a stream the process may be using as a
  protocol channel; stdout is reachable only by asking for it explicitly with
  `NewTextExporter(name, os.Stdout)`. The temptation is stronger for the
  Prometheus exporter — an exposition document *looks* like something a caller
  wants on stdout — but a scrape endpoint hands the exporter its
  `http.ResponseWriter` and never touches the registered default, so nothing is
  gained by making the import dangerous. One regression test per surface.
- **Both exporters escape STRING attribute values** (`\\`, `\"`, `\n`) through
  the shared `appendEscapedValue`. A value is data; unescaped, one containing a
  quote or a newline forges a line a reader parses as another series.
- Cross-OS: 100 % portable (sync/atomic/math/strconv/time).

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
- Emit a placeholder `# HELP`. The format makes it optional precisely because
  there is not always a docstring to write.
- Convert a series key to a `string` before a map read. `m[string(b)]` does not
  allocate; `k := string(b); m[k]` does, once per observation.
- Clone a series' attribute set inside `Collect`. It is shared with the snapshot
  on purpose (see `internal/core/metrics/CLAUDE.md` §The snapshot shape).
- Swap an OBSERVED series to zero during a delta collection. Its callback
  already stored the window.
- Grow `NewMeter` / `NewMeterWithConfig` past the inlining budget. See
  §Conventions.
- Add an "unbounded" cardinality setting.

## Verification

```
bazel test --config=race //internal/service/metrics:metrics_test
bazel test --config=alloc //internal/service/metrics:metrics_test   # the !race alloc gates
```
