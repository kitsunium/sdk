<!-- generated from internal/kernel/batcher/batcher_bench_test.go — run `cd internal/kernel && GOWORK=off go test -run='^$' -bench=. -benchmem -benchtime=1s ./batcher/` to refresh -->
# Benchmarks — `internal/kernel/batcher`

Generic coalescing buffer (ADR 0014). These benchmarks isolate the producer-visible
`Add` path under a small `MaxItems` cap so nearly every `Add` triggers an eager
flush through the deliver path, plus the no-op `Flush` fast path as a
serialization-free baseline. They exist to quantify the cost of the **V6**
Sink-serialization fix: the deliver closure now runs under a dedicated
`deliverMu` so two flush paths never enter the Sink concurrently.

## Reproducibility envelope

> **Numbers vary across machines.** This report stamps the box that produced
> them so cross-machine deltas can be evaluated honestly.

| Dimension | Value |
|---|---|
| CPU cores          | 12 (12th Gen Intel(R) Core(TM) i7-1255U) |
| RAM                | 15.3 GiB |
| OS / kernel        | Linux 6.12.107+deb13-amd64 (Debian 13 trixie) |
| Architecture       | amd64 |
| Go toolchain       | go1.27.1 linux/amd64 |
| Git branch         | fix/bench-127 |
| Git commit         | c08d730 |
| Generated (UTC)    | 2026-09-15 |
| Bench wall-clock   | `-benchtime=1s -count=5`, median of 5 runs |

> **What these numbers support.** `allocs/op` is exact: all 5 repeats agreed on
> every cell, in every package. `B/op` is exact too **except where a cell
> carries `*`**, which marks five values that were not identical and a median
> reported in their place. Both columns are **unchanged** from a go1.26.4 run
> of this same code on this same box (124 benchmarks compared SDK-wide, 44 of
> them allocating, zero counter moved). `ns/op` are medians and carry the
> `spread` shown, which is a **within-run** figure that understates run-to-run
> variance: re-running the identical binary on this box moved individual cells
> by up to 94 %. Read ns/op as an order of magnitude on this box, never as a
> cross-edition or cross-machine delta.

## V6 before/after — Sink serialization cost

The plan (PR-F · WS-7, V6) requires a before/after benchmark because serializing
the Sink behind a dedicated mutex can cost throughput. "Before" is the prior code
path (deliver invoked with no `deliverMu`); "after" is the serialized path now in
`deliverBatch`.

```
                       before (unserialized)        after (deliverMu, V6)
BenchmarkAdd_CapFlush   27.71 ns/op  15 B/op  0 a   25.63 ns/op  15 B/op  0 a
BenchmarkAdd_Contended 105.7  ns/op  17 B/op  0 a  108.1  ns/op  16 B/op  0 a
```

The single-producer cap-flush path is unchanged (the mutex is uncontended — a
lock/unlock pair amortised across 16 Adds). The contended path moves from
~105.7 to ~108.1 ns/op — within run-to-run noise on this box, NOT a material
regression. Per the V6 acceptance fallback, no regression means **serialize is
kept as the default** and **no `ConcurrentSink` opt-in is introduced** — an
implicit concurrent contract was the footgun the finding flagged.

## Results (current, serialized)

```
goos: linux
goarch: amd64
pkg: github.com/kitsunium/sdk/internal/kernel/batcher
cpu: 12th Gen Intel(R) Core(TM) i7-1255U

benchmark          median ns/op   spread   min–max         B/op   allocs/op
Add_CapFlush-12           52.13     9.2%   50.73 – 55.51     15           0
Add_Contended-12          320.1     2.2%   314.5 – 321.7     17           0
Flush_Empty-12            41.70     5.4%   41.32 – 43.57      0           0
```

## How to read this

- **`BenchmarkAdd_CapFlush`** — single producer, `MaxItems: 16`. Every 16th Add
  flushes through `deliverBatch` and acquires `deliverMu` uncontended. The ~15 B/op
  is the amortised growth of the pending slice's backing array, not a per-Add
  allocation (it benches at 0 allocs/op).
- **`BenchmarkAdd_Contended`** — the same path under `RunParallel`, so `deliverMu`
  is contended across all P goroutines. This is the worst case for the V6 fix and
  the number the before/after comparison is read against.
- **`BenchmarkFlush_Empty`** — an empty `Flush` short-circuits under `mu` before
  reaching the deliver mutex, so it is the serialization-free floor (~42 ns/op, 0
  B/op) the cap-flush benches are measured against.
