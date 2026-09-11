# internal/service/metrics/

## Purpose

In-memory `Meter` + lock-free instruments (`Counter`/`Gauge`/`Histogram`)
implementing `core/metrics`, plus two stdlib Exporters — a **text** diagnostic
and a **Prometheus** text-exposition renderer, both registered to **stderr** on
import (ADR 0030). Stdlib-only, cross-OS. ADR 0027. Emits core sentinels
`0.2.9.*` and owns block `0.3.45.*` for the names the wire format refuses.

Instruments are keyed by name **and label set** — one name plus one label set is
one **series** — with a per-name cardinality bound that folds the excess into a
single aggregated overflow series.

## Contents

| File | Surface |
|---|---|
| `meter.go` | `memMeter` + `NewMeter` / `NewMeterWithConfig` + `Collect` (grouping, arena layout, per-name sort) |
| `series.go` | series identity: `sortLabels`, `validateLabels`, `appendSeriesKey`, `cloneLabels`, `compareLabels` |
| `series_store.go` | `seriesStore[T]` — one kind's series map, `lookup` (read lock) + `admit`/`overflow` (write lock) |
| `series_entry.go` | `seriesEntry[T]` — one live series: name, owned label set, instrument |
| `name_state.go` | `instrumentKind` + `nameState` (kind binding, series tally, cached overflow key) + `bindName` |
| `cardinality.go` | `MeterConfig` + `DefaultMaxSeriesPerInstrument` |
| `counter.go` | `memCounter` (atomic.Int64) |
| `gauge.go` | `memGauge` (atomic float64 bits, CAS) |
| `histogram.go` | `memHistogram` (sorted buckets + atomic counts/sum) |
| `exporter_text.go` | `textExporter` + default **stderr** `Text` + `NewTextExporter` |
| `exporter_prometheus.go` | `prometheusExporter` + default **stderr** `Prometheus` + `NewPrometheusExporter` + the two name grammars |
| `codes.go` / `errors.go` | `0.3.45.*` (INVALID_METRIC_NAME, INVALID_LABEL_NAME, RESERVED_LABEL_NAME) |

## Series identity

A series key is `uvarint(len(name)) name` followed by
`uvarint(len(k)) k uvarint(len(v)) v` for each label, **sorted by key**.

- **Sorted**, because a label set is a set. Without it `{a,b}` and `{b,a}` become
  two series that split one metric's total, and spend two slots of the bound on
  one identity.
- **Length-prefixed, not delimiter-separated.** A label value is data — a route,
  a tenant id, a header. With a delimiter, a caller who can influence one value
  can forge another series' key and have two unrelated series silently
  accumulate into one. Length prefixes make the encoding injective.

An empty label key, or the same key twice, **panics** with `InvalidLabel`
(`0.2.9.4`) — the same call the meter already makes for a cross-kind name reuse,
and safe for the same reason: a key is written at the call site, so it is wrong
on the first call or never. The alternative is a series no exporter can emit
(Prometheus and OTLP both reject an empty label name) failing far away, inside
the component the SDK told the caller to stop thinking about.

## Cardinality policy

`MaxSeriesPerInstrument` bounds the distinct label sets **one instrument name**
may hold — per name, so one exploding label on `http_requests_total` cannot
starve `db_queries_total` of the series it needs. Default
`DefaultMaxSeriesPerInstrument = 2000` (the figure the OpenTelemetry SDKs
settled on for the same problem).

**Non-positive clamps to the default.** It does not mean unbounded, and there is
no setting that does (ADR 0031). A caller who leaves the knob at zero has not
decided that memory is free; they have not yet learned the question exists.
A caller who genuinely wants a huge bound types a huge number, where a reviewer
can see it.

**Past the bound, a new label set is FOLDED, not rejected and not dropped.** It
goes into one aggregated series per name carrying
`sdk_metric_overflow="true"`, which sits outside the bound as one extra slot.
What that buys and what it costs:

| | |
|---|---|
| Memory is bounded | the point — an unbounded label set is a process-killing leak |
| Nothing is lost silently | a counter's grand total stays correct; every increment lands somewhere |
| The breakdown IS lost | irrecoverably — a folded observation's own labels are gone |
| Identity is arrival-order dependent | the first N label sets win, so two replicas can fold different sets and disagree about what is visible |
| The condition is visible | the overflow series shows up in every snapshot from then on, so an operator reading a dashboard learns their labels blew up |
| Overflow is allocation-free | see BENCH.md — otherwise the bound would trade a leak for GC pressure with the same cause |

Rejected alternatives: a **typed error** cannot be delivered from an accessor
whose signature hands back a `Counter` without changing every call site (the
same shape problem ADR 0031 solved for the resilience constructors, but here
there is no `Run` to fail later); **dropping** the observation is exactly the
inert behaviour ADR 0031 bans; **evicting** an admitted series would make the
visible set flap with traffic and lose the evicted totals outright.

`OverflowLabelKey` is reserved by convention, not enforced. A caller who passes
it explicitly writes into the overflow series. Enforcing it would cost a
comparison per label on the lookup path to prevent a collision nobody reaches by
accident.

## The Prometheus text exposition format

Reference: the **Prometheus text-based exposition format**, version `0.0.4`
(`text/plain; version=0.0.4; charset=utf-8`) —
<https://prometheus.io/docs/instrumenting/exposition_formats>. Everything below
is that specification, not a house convention; where the two could differ, the
tests carry values copied out of the specification's own example document.

**Shape.** One `# TYPE <name> <kind>` per instrument name, then that name's
series. That is exactly one pass over `SnapshotValue`, because the snapshot is
keyed by name — no regrouping, no composite key to re-parse (see
`internal/core/metrics/CLAUDE.md` §The snapshot shape). The format permits only
one `TYPE` line per name and requires it to precede the samples, which is what
the header-per-key walk gives for free.

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
| Why refusing is safe | an instrument name is STRUCTURE — a literal at the call site, constant for the process. It is wrong on the first scrape or never, exactly like the label KEY the meter already panics on. It cannot start failing in production because of traffic |
| Why the whole document | a truncated exposition parses as a complete one, so its missing series look like series that stopped existing |

`__`-prefixed label names are refused as `RESERVED_LABEL_NAME` although they are
syntactically legal: Prometheus reserves them for its own internal labels and
drops them during relabelling, so the series would silently lose a dimension at
the server. `le` is refused on a **histogram** only, where it would be a second
`le` on the bucket line, i.e. a duplicate label name the format rejects.

**Escaping.** Exactly three sequences in a label value: `\` → `\\`, `"` → `\"`,
newline → `\n` (shared with the text exporter through `appendEscapedValue`).
There is no fourth, deliberately: the 0.0.4 parser **errors on an unknown escape
sequence**, so emitting `\r` or `\t` would cost the whole scrape rather than one
label — strictly worse than passing the byte through, which cannot forge a line
because only an unescaped newline terminates one. `TestAppendEscapedValueCovers
TheFormat` pins both halves, and the external escaping test asserts the line
count as well as the bytes, so a forged line fails even if a golden string were
updated to match a bug. Names need no escaping at all — that is the second thing
validation buys.

**The overflow series is emitted like any other.** `sdk_metric_overflow` is a
legal label name by construction (`core/metrics/label.go` spells it with
underscores for this reason), so the folded series reaches the wire and an
operator can alert on `{sdk_metric_overflow="true"}`. Hiding it would restore
the silent failure the cardinality policy exists to avoid.

**Validation runs per scrape.** It is O(bytes of names + label keys), which is
noise next to the formatting, and this is the only layer that can do it: the
meter does not know which exporter its snapshot is going to.

## Conventions

- **Lock-free instruments.** The meter takes only the READ lock to resolve an
  existing series; the write lock is held for creation alone. That is what makes
  a per-observation lookup viable at all — labels move the lookup from start-up
  onto the request path.
- **Zero allocations on the lookup path**, labelled or not, and on the overflow
  path. The sorted label set and the encoded key live in stack arrays, and the
  map is indexed with `string(scratch)`, which does not copy. A key is converted
  to a `string` in exactly one place — `retain`, on insertion, where the map
  keeps it. Gated by `meter_alloc_test.go`; see BENCH.md.
- **The stores are keyed flat** by the whole series key, not nested by name: one
  map read resolves a labelled fetch where a nesting would cost two.
- **Kind and bound belong to the NAME**, not the series (`nameState`) — labels
  vary within one metric, its kind and its quota do not.
- **`Collect` carves one arena per kind** into exactly-sized per-name windows,
  using the meter's own series tally. Letting each name's slice grow on its own
  costs an allocation per name plus a doubling copy per many-series name — both
  scaling with cardinality, on the path a scraper walks every few seconds.
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
- **Both exporters escape label values** (`\\`, `\"`, `\n`) through the shared
  `appendEscapedValue`. A value is data; unescaped, one containing a quote or a
  newline forges a line a reader parses as another series.
- Cross-OS: 100 % portable (sync/atomic/math/strconv).

## Do NOT

- Discard writer errors — each exporter buffers into `[]byte` then does one
  `Write` with a wrapped `EXPORT_FAILED` on failure.
- Add an `init()`.
- Point a *registered* exporter at `os.Stdout`. The import is invisible at the
  call site, so the default must be the stream nobody parses.
- Transliterate a metric or label name in the Prometheus exporter. Mapping the
  offending bytes to `_` merges distinct instruments silently — see §The
  Prometheus text exposition format.
- Invent a fourth escape sequence. The 0.0.4 parser rejects anything but
  `\\`, `\"` and `\n`, so a `\r` would cost the whole scrape.
- Emit a placeholder `# HELP`. The format makes it optional precisely because
  there is not always a docstring to write.
- Convert a series key to a `string` before a map read. `m[string(b)]` does not
  allocate; `k := string(b); m[k]` does, once per observation.
- Clone a series' label set inside `Collect`. It is shared with the snapshot on
  purpose (see `internal/core/metrics/CLAUDE.md` §The snapshot shape).
- Add an "unbounded" cardinality setting.

## Verification

```
bazel test --config=race //internal/service/metrics:metrics_test
bazel test --config=alloc //internal/service/metrics:metrics_test   # the !race alloc gates
```
