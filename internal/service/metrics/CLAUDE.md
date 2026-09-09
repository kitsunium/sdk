# internal/service/metrics/

## Purpose

In-memory `Meter` + lock-free instruments (`Counter`/`Gauge`/`Histogram`)
implementing `core/metrics`, plus a stdlib **text** Exporter (registered to
**stderr** on import — ADR 0030). Stdlib-only, cross-OS. ADR 0027. Emits core
sentinels `0.2.9.*`.

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
- **Registration via `var Text = metrics.RegisterExporter(...)`** — no `init()`.
- **The registered default writes to `os.Stderr`** (ADR 0030). Importing a
  package must not arm a writer on a stream the process may be using as a
  protocol channel; stdout is reachable only by asking for it explicitly with
  `NewTextExporter(name, os.Stdout)`.
- **The text exporter escapes label values** (`\\`, `\"`, `\n`, the Prometheus
  convention). A value is data; unescaped, one containing a quote or a newline
  forges a line a reader parses as another series.
- Cross-OS: 100 % portable (sync/atomic/math).

## Do NOT

- Discard writer errors — the text exporter buffers into `[]byte` then does one
  `Write` with a wrapped `EXPORT_FAILED` on failure.
- Add an `init()`.
- Point a *registered* exporter at `os.Stdout`. The import is invisible at the
  call site, so the default must be the stream nobody parses.
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
