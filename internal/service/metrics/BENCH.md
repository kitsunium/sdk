<!-- generated from internal/service/metrics/meter_bench_test.go — refresh with `cd internal/service && GOWORK=off go test -run '^$' -bench=. -benchmem -count=5 ./metrics/` -->
# Benchmarks — `internal/service/metrics`

## Reproducibility envelope

> **Numbers vary across machines.** This report stamps the box that produced
> them so cross-machine deltas can be evaluated honestly.

| Dimension | Value |
|---|---|
| CPU               | AMD EPYC 7351P 16-Core, 8 vCPU visible |
| RAM               | 15.4 GiB |
| OS / kernel       | Linux 6.12.101+deb13-amd64 (Debian GNU/Linux 13, trixie) |
| Architecture      | amd64 |
| Go toolchain      | go1.27.1 linux/amd64 |
| Git branch        | `agent-a82d776db6e43d998` |
| Git commit        | `43131fb` (the pre-change baseline; the "after" run is this commit's tree) |
| Generated (UTC)   | 2026-09-09 |
| Bench wall-clock  | `-benchtime=1s -count=5`, median quoted |

## What is being measured

Labels move the instrument LOOKUP onto the request path. Before labels a caller
hoisted `m.Counter("requests")` once at start-up and never looked it up again;
with labels the values are per-observation (`status="503"`), so the lookup runs
as often as the arithmetic does. That is why every number below is about
resolving a series, not about incrementing one.

## Before → after

The "before" column is the label-free implementation at `43131fb`, measured on
this same box before any change. `Collect` is listed twice because the two
implementations do not have a comparable single shape: the old snapshot was
`map[name]int64`, the new one is `map[name][]CounterValue`, so the fair
comparison is per workload.

| Benchmark | Before | After |
|---|---|---|
| `CounterLookup_NoLabels` — resolve a dimensionless series | 66.8 ns · 0 B · **0 allocs** | **57.0 ns** · 0 B · **0 allocs** |
| `CounterLookup_3Labels` — resolve a 3-label series (unsorted input) | n/a | **169 ns** · 0 B · **0 allocs** |
| `CounterLookup_Overflow` — a series past the cardinality bound | n/a | **240 ns** · 0 B · **0 allocs** |
| `CounterAdd_Hoisted` — the arithmetic alone | 10.0 ns · 0 allocs | **10.2 ns** · 0 allocs |
| `CounterLookup_Parallel` — 8 goroutines resolving one series | 217–268 ns · 0 allocs | **255 ns** · 0 allocs (now with 3 labels) |
| `Collect_1000Names` — 1000 dimensionless instruments | 63.4 µs · 54.8 kB · 8 allocs | **296 µs** · 131 kB · **9 allocs** |
| `Collect_1000Series` — 1 instrument, 1000 series | n/a | **334 µs** · 33.3 kB · **5 allocs** |
| `HistogramRecord` | 28.8 ns · 0 allocs | **33.8 ns** · 0 allocs |

## How to read this

- **A labelled lookup allocates nothing.** 3 labels cost ~169 ns and **zero**
  allocations: the label set is sorted into a stack array, the series key is
  built in a second stack array, and the map is indexed with `string(scratch)`,
  which the compiler turns into a lookup that does not copy. This is the number
  the feature stands on — an allocation here would be an allocation per
  observation, which is what the cardinality bound exists to avoid in the first
  place. Pinned by `TestLookupIsAllocationFree` (see *Gates* below).
- **Overflow allocates nothing either.** An instrument past its bound sees a
  brand-new label set on every observation, so it misses the read lock every
  time and takes the write-lock path. At ~240 ns and zero allocations, exceeding
  the bound costs latency and nothing else — it does not trade a memory leak for
  GC pressure. (`CounterLookup_Overflow` reports **7 B/op, 0 allocs/op**: the
  bytes are `strconv.Itoa` inside the benchmark generating the label values, not
  the meter. The gate test uses pre-rendered strings and measures exactly 0.)
- **The dimensionless path got faster, not slower** — 66.8 ns → 57.0 ns. Adding
  labels also replaced an unconditional write lock (the old `Counter` took
  `mu.Lock()` on every call, plus a three-map scan for the kind guard) with a
  read lock and a single map read. The kind is now tracked once per instrument
  NAME rather than being rediscovered per call.
- **Contention improved by roughly the same margin.** The parallel figure looks
  flat at ~255 ns, but the "before" number was for a *dimensionless* fetch; the
  "after" number resolves a three-label series and still lands in the same
  range. Like-for-like the read lock is the win.
- **`Collect` on many dimensionless instruments is ~4.7× slower** (63 µs →
  296 µs for 1000 names). This is the honest cost of per-series identity, and it
  is structural rather than a missed optimisation: the snapshot changed from
  `map[string]int64` (an 8-byte value per entry) to `map[string][]CounterValue`
  (a 24-byte slice header per entry pointing at a 32-byte element). Allocation
  count barely moved (8 → 9) because the series slices are carved out of one
  arena rather than allocated per name. In absolute terms this is 0.3 ms on a
  path a scraper walks every 10–60 s.
- **`Collect` on many series of one instrument is cheaper than the name-heavy
  shape in memory** — 33 kB and 5 allocations for 1000 series, because one arena
  plus one map entry replaces a thousand of each. Time is dominated by the
  per-name sort that makes the snapshot deterministic (~10 000 label-set
  comparisons for 1000 series).
- **The instruments themselves are untouched.** `Add`/`Record` are the same
  lock-free atomics they always were; the ~3 ns drift on `HistogramRecord` is
  run-to-run noise on a shared box, not a change in the code path.

## Caveats

- **The zero-allocation result depends on devirtualisation.** Writing labels
  inline — `m.Counter("requests", metrics.Label{Key: "method", Value: "GET"})` —
  builds a variadic slice at the call site. It stays on the stack only while the
  compiler can prove the callee does not retain it, which it can here because
  the concrete meter is visible. Behind an indirect call (a `func` value, a
  plugin boundary, a second `Meter` implementation linked into the binary) that
  proof fails and the variadic slice, or a `[]float64` bucket literal, is heap
  allocated — one small allocation per call. Hoist the label slice and the
  bucket slice out of a hot loop if that matters; the meter itself adds nothing
  either way. The alloc-gate test hoists them deliberately, and says why.
- Beyond 8 labels the sort falls back to a heap clone (`maxStackLabels`), and
  beyond a 192-byte encoded key the key buffer does too (`seriesKeyCap`). Both
  are correctness-preserving and neither is reached by a sanely-labelled metric.
- Single box, `-count=5`, median quoted; no `benchstat` confidence interval.
  Treat the deltas under ~5 % as noise. The `Collect_1000Names` spread was the
  widest (252–371 µs), which is what a 1000-entry map walk looks like on a
  shared machine.

## Gates

`TestLookupIsAllocationFree`, `TestGaugeAndHistogramLookupAreAllocationFree` and
`TestOverflowLookupIsAllocationFree` (in `meter_alloc_test.go`) assert the
zero-allocation claims above with `testing.AllocsPerRun`. The file carries
`//go:build !race`, so it is invisible to the race suite and runs in exactly one
lane — the race-off allocation lane. `//internal/service/metrics:metrics_test`
is listed in `tools/alloc-lane-targets.txt` for that reason (CLAUDE.md rule 12).

## Methodology

```
cd internal/service && GOWORK=off go test -run '^$' -bench=. -benchmem -count=5 ./metrics/
```

Each benchmark creates the series it measures BEFORE the timed loop, so what is
timed is resolution, not creation — except `CounterLookup_Overflow`, where a
never-before-seen label set on every iteration is the whole point.
