<!-- generated from internal/service/metrics/meter_bench_test.go — refresh with `cd internal/service && GOWORK=off go test -run '^$' -bench=. -benchmem -count=7 ./metrics/` -->
# Benchmarks — `internal/service/metrics`

## Reproducibility envelope

> **Numbers vary across machines.** This report stamps the box that produced
> them so cross-machine deltas can be evaluated honestly.

| Dimension | Value |
|---|---|
| CPU               | AMD EPYC 7351P 16-Core, 8 vCPU visible |
| RAM               | 15.2 GiB |
| OS / kernel       | Linux 6.12.101+deb13-amd64 (Debian GNU/Linux 13, trixie) |
| Architecture      | amd64 |
| Go toolchain      | go1.27.1 linux/amd64 |
| Git branch        | `agent-a4e0226da1d912847` |
| Git commit        | `167ec6b` (the "after" tree; the "before" column was measured on the same box, same session, on the pre-ADR-0044 tree) |
| Generated (UTC)   | 2026-09-09; §ADR 0067 re-measured 2026-09-10 on the same box (branch `agent-a8b94605d1380fe5e`, commit `c30e2ad`, go1.27.1) |
| Bench wall-clock  | before: `-benchtime=1s -count=5`; after: `-count=7`, median quoted |

## What is being measured

Adopting the OpenTelemetry data model (ADR 0044) touched the observation path in
three places, and the question this report answers is whether any of them cost
anything:

1. **An attribute grew from 32 to 48 bytes.** `LabelValue{Key, Value string}`
   became `AttrValue{Key string; kind; num; str}`, so the stack scratch every
   lookup sorts through is 50 % larger.
2. **The series key gained two tags.** One byte for the instrument KIND at the
   front (four instrument kinds now share one store), and one byte per attribute
   for its VALUE kind — which is what keeps `String("v", "1")` and
   `Int64("v", 1)` two series.
3. **The snapshot grew an envelope per metric.** A map value went from a 24-byte
   slice header to a 40-byte `SumMetricValue{Temporality, Monotonic, Points}`,
   and the collect-time sort compares typed values rather than two strings.

The LOOKUP is still what matters most: attribute values are per-observation
(`http.response.status_code=503`), so resolving a series runs as often as the
arithmetic does.

## Before → after

"Before" is the string-label implementation measured on this box before the
change; `_NoAttrs` / `_3Attrs` were then named `_NoLabels` / `_3Labels` and are
the same code path.

| Benchmark | Before | After |
|---|---|---|
| `CounterLookup_NoAttrs` — resolve a dimensionless series | 57.9 ns · 0 B · **0 allocs** | **62.1 ns** · 0 B · **0 allocs** |
| `CounterLookup_3Attrs` — resolve a 3-attribute series (unsorted input) | 171.9 ns · 0 B · **0 allocs** | **206.8 ns** · 0 B · **0 allocs** |
| `CounterLookup_Overflow` — a series past the cardinality bound | 233.1 ns · 0 allocs | **238.7 ns** · 0 allocs |
| `CounterAdd_Hoisted` — the arithmetic alone | 10.1 ns · 0 allocs | **10.0 ns** · 0 allocs |
| `CounterLookup_Parallel` — 8 goroutines resolving one series | 214.7 ns · 0 allocs | **200.6 ns** · 0 allocs |
| `Collect_1000Names` — 1000 dimensionless instruments | 209.5 µs · 131 kB · 9 allocs | **244.1 µs** · 148 kB · **9 allocs** |
| `Collect_1000Series` — 1 instrument, 1000 series | 331.1 µs · 33.3 kB · 5 allocs | **430.6 µs** · 33.3 kB · **5 allocs** |
| `HistogramRecord` | 28.9 ns · 0 allocs | **28.9 ns** · 0 allocs |
| `CounterLookup_3TypedAttrs` — 3 attributes, mixed kinds | n/a | **210.3 ns** · 0 B · **0 allocs** |
| `UpDownCounterLookup_3Attrs` — the non-monotonic sum | n/a | **196.8 ns** · 0 B · **0 allocs** |
| `Collect_1000Series_Delta` — a delta reader consuming the window | n/a | **422.3 µs** · 33.3 kB · **5 allocs** |
| `Collect_100Observables` — 100 callbacks run inside one collection | n/a | **32.4 µs** · 16.6 kB · 107 allocs |

## How to read this

- **A typed attributed lookup still allocates nothing.** Three attributes cost
  ~207 ns and **zero** allocations, and mixing the kinds
  (`Int64`+`String`+`Bool`) costs the same ~210 ns rather than a `strconv` call:
  the identity encoding writes an integer or a double as eight fixed bytes
  straight into the stack key buffer. This is the number the whole feature
  stands on — an allocation here would be an allocation per observation. Pinned
  by `TestLookupIsAllocationFree`, which now includes an all-four-kinds case.
- **The lookup path is ~20 % slower and that is the price of the model.** 172 →
  207 ns for three attributes, 58 → 62 ns dimensionless. It buys: a 48-byte
  attribute instead of 32 (so `sortAttrs` copies 50 % more), a kind tag per
  value, an instrument-kind byte at the head of every key, and one extra pointer
  hop because the store now reaches its meter through a back-pointer. 35 ns per
  attributed observation is the honest cost of a dimension that carries its own
  type, and the allocation budget — the number that decides whether a metric can
  live on a request path at all — did not move.
- **The UpDownCounter is not a slower Counter.** 197 ns against 207 ns for the
  same three attributes; it shares the storage, the store and the lookup, and
  differs by one immutable bool checked on `Add`.
- **Overflow still allocates nothing.** An instrument past its bound sees a
  brand-new attribute set on every observation, so it misses the read lock every
  time and takes the write-lock path. At ~239 ns and zero allocations, exceeding
  the bound costs latency and nothing else — it does not trade a memory leak for
  GC pressure. (`CounterLookup_Overflow` reports **7 B/op, 0 allocs/op**: the
  bytes are `strconv.Itoa` inside the benchmark generating the attribute values,
  not the meter. The gate test uses pre-rendered strings and measures exactly 0.)
- **`Collect` pays for the richer payload, and the cost is structural.**
  `Collect_1000Series` is 331 → 431 µs. Two causes, both intrinsic: each map
  value is now a 40-byte metric envelope instead of a 24-byte slice header (so
  every append writes the envelope back), and the per-name sort compares typed
  values through a kind switch instead of `strings.Compare` on two strings —
  ~10 000 comparisons for 1000 series. `Collect_1000Names` is 209 → 244 µs and
  148 kB instead of 131 kB for the same reason. **Allocation count did not
  move** (9 and 5), because the arena still carves every name's window out of
  one backing array. In absolute terms this is 0.4 ms on a path a scraper walks
  every 10–60 s.
- **A delta collection is not more expensive than a cumulative one** — 422 µs
  against 431 µs, i.e. inside the noise. Consuming a window is `Swap(0)` where
  cumulative does `Load()`, on the same atomic.
- **An observable costs about 320 ns per registered callback per collection**
  (32.4 µs for 100), with one allocation each: the reporting closure handed to
  the callback. That closure is created once per observable per collection and
  is what lets a callback report many series; it lives on the collection path,
  not the observation path, so it is charged to the scrape.
- **The instruments themselves are untouched.** `Add`/`Record` are the same
  lock-free atomics they always were: 10.0 ns and 28.9 ns, both flat.
- **Contention improved slightly** (215 → 201 ns), which is run-to-run drift on
  a read-lock path that did not change.

## ADR 0067 — what an instrument description costs

The description is the second measurement in this file with a claim attached to
it, and the claim is narrow: **it must cost the observation path nothing.** That
is why `Describer` is `Describe(name, description string)` on a sibling port and
not a description parameter threaded through `Counter`/`Gauge`/`Histogram` — a
second string on a variadic call is a second thing the compiler has to prove
non-escaping, on the one path in this package that runs per observation.

Both trees measured on this box, same session, `-count=5` (`-count=7` for the
three collect rows, which are the noisy ones).

| Benchmark | Before ADR 0067 | After ADR 0067 |
|---|---|---|
| `CounterLookup_NoAttrs` | 63.6 ns · **0 allocs** | **62.1 ns** · **0 allocs** |
| `CounterLookup_3Attrs` | 198.7 ns · **0 allocs** | **188.9 ns** · **0 allocs** |
| `CounterLookup_3TypedAttrs` | 207.3 ns · **0 allocs** | **198.3 ns** · **0 allocs** |
| `CounterLookup_Overflow` | 243.4 ns · **0 allocs** | **252.9 ns** · **0 allocs** |
| `CounterAdd_Hoisted` | 10.12 ns · **0 allocs** | **10.04 ns** · **0 allocs** |
| `HistogramRecord` | 34.9 ns · **0 allocs** | **28.8 ns** · **0 allocs** |
| `Collect_1000Names` | 244 µs · 147 681 B · **9 allocs** | **249 µs** · 180 448 B · **9 allocs** |
| `Collect_1000Series` | 431 µs · 33 328 B · **5 allocs** | **419 µs** · 33 488 B · **5 allocs** |
| `Collect_100Observables` | 32.4 µs · 16 568 B · 107 allocs | **32.9 µs** · 19 512 B · 107 allocs |

And the three benchmarks the change added:

| Benchmark | Result |
|---|---|
| `CounterLookup_3Attrs_Described` — the same fetch on a meter that HAS a description | **191.4 ns** · 0 B · **0 allocs** |
| `Collect_1000Names_Described` — 1000 described instruments, scraped | **275 µs** · 180 448 B · **9 allocs** |
| `Describe` — the wiring-time call, on its idempotent path | **48.2 ns** · 0 B · **0 allocs** |

### How to read this

- **The observation path did not move, and still allocates nothing.**
  `CounterLookup_3Attrs_Described` (191.4 ns) against `CounterLookup_3Attrs`
  (188.9 ns) is the load-bearing comparison: same benchmark, same series, the
  only difference being that the meter's `descriptions` map exists and is
  non-empty. 2.5 ns apart, i.e. inside the run-to-run spread of either one.
  Nothing on the fetch path reads the map, and this is what proves it.
  `TestDescribedMeterLookupIsAllocationFree` is the gate.
- **An undescribed meter allocates the map at all.** It is nil until the first
  `Describe`, and a nil map READS as the empty one in Go, so `Collect` finds
  `""` for every name without a branch and without a `make`. The struct grew by
  one map header (8 bytes, once per meter).
- **The description is read once per instrument NAME per collection, and that
  is the whole cost.** 1000 described names cost 275 µs against 249 µs
  undescribed — about **25 ns per name per scrape**, which is one map lookup.
  It is charged to the scraper, which walks every 10–60 s, and not to the
  request path.
- **The snapshot grew 16 bytes per metric NAME, not per series and not per
  observation.** A `string` header on each of the three metric envelopes:
  `Collect_1000Series` (one name, a thousand series) gained 160 B in total,
  while `Collect_1000Names` gained 32 767 B — the field times the map's slot
  count, rounded up by the runtime. **Allocation COUNT did not move** anywhere
  (9, 5 and 107), because the arena still carves every name's window out of one
  backing array and the description rides in the envelope that was already
  being written.
- **`Describe` itself is 48 ns and allocates nothing** on the idempotent path
  (write lock, one map read, one string compare). It runs once per instrument
  name at start-up; the number is here for completeness, not because anything
  depends on it.
- `HistogramRecord` reading 34.9 → 28.8 ns and `Collect_1000Names` reading
  244 → 249 µs are **contention, not signal**. The "before" pass shared the box
  with other jobs — its own `Collect_1000Names` samples spread 328–668 µs — and
  the focused `-count=7` re-run quoted above is the tighter one. The columns
  that mean anything here are the allocation columns, which are invariants.

## Caveats

- **The zero-allocation result depends on devirtualisation, and the
  constructors are shaped to preserve it.** Writing attributes inline —
  `m.Counter("requests", metrics.String("method", "GET"))` — builds a variadic
  slice at the call site. It stays on the stack only while the compiler can
  prove the callee does not retain it, which it can only when the meter's
  CONCRETE type is visible. `NewMeter` and `NewMeterWithConfig` are therefore
  one-line wrappers over an unexported `newMemMeter`, so both stay inlinable and
  carry `*memMeter` to the call sites below them. This is not theoretical: while
  ADR 0044 was being written, the constructor briefly grew past the inlining
  budget and every attributed observation regained one 48-byte allocation, which
  the alloc gate caught. Behind a genuinely indirect call (a `func` value, a
  second `Meter` implementation linked into the binary) the proof still fails —
  hoist the attribute slice out of a hot loop if that matters; the meter itself
  adds nothing either way. The alloc-gate test hoists them deliberately, and
  says why.
- **`admit` takes four separate identity arguments on purpose.** Grouping
  (name, kind, key, attrs) into one struct to shorten the signature costs **two**
  allocations per observation: Go's escape analysis is field-insensitive on a
  struct parameter, so the whole group escapes as soon as the name is stored on
  an entry, dragging the caller's stack scratch to the heap. Measured, then
  reverted; the meter back-pointer moved onto the store instead.
- Beyond 8 attributes the sort falls back to a heap clone (`maxStackAttrs`), and
  beyond a 192-byte encoded key the key buffer does too (`seriesKeyCap`). Both
  are correctness-preserving and neither is reached by a sanely-attributed
  metric.
- Single box, `-count=7`, median quoted; no `benchstat` confidence interval.
  Treat the deltas under ~5 % as noise. The spreads in the quoted run were tight
  (`Collect_1000Names` 236–268 µs, `CounterLookup_3Attrs` 204–208 ns); an
  earlier run on the same tree, taken while other work shared the box, spread
  `Collect_1000Names` from 252 µs to 1202 µs — if a re-run looks like that, the
  machine is contended and the numbers are not comparable.

## Gates

`TestLookupIsAllocationFree`, `TestEveryInstrumentLookupIsAllocationFree`,
`TestOverflowLookupIsAllocationFree` and `TestDescribedMeterLookupIsAllocationFree`
(in `meter_alloc_test.go`) assert the zero-allocation claims above with
`testing.AllocsPerRun`, across every attribute KIND, every synchronous
instrument, the overflow path and a meter carrying a description. The file carries `//go:build !race`, so
it is invisible to the race suite and runs in exactly one lane — the race-off
allocation lane. `//internal/service/metrics:metrics_test` is listed in
`tools/alloc-lane-targets.txt` for that reason (CLAUDE.md rule 12).

## Methodology

```
cd internal/service && GOWORK=off go test -run '^$' -bench=. -benchmem -count=7 ./metrics/
```

Each benchmark creates the series it measures BEFORE the timed loop, so what is
timed is resolution, not creation — except `CounterLookup_Overflow`, where a
never-before-seen attribute set on every observation is the whole point, and
`Collect_100Observables`, where running the callbacks is the measurement.
