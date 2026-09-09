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
| Generated (UTC)   | 2026-09-09 |
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

`TestLookupIsAllocationFree`, `TestEveryInstrumentLookupIsAllocationFree` and
`TestOverflowLookupIsAllocationFree` (in `meter_alloc_test.go`) assert the
zero-allocation claims above with `testing.AllocsPerRun`, across every attribute
KIND and every synchronous instrument. The file carries `//go:build !race`, so
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
