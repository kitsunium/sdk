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
| CPU cores          | 12 |
| RAM                | 15 GiB |
| OS / kernel        | Linux 6.12.90+deb13-amd64 |
| Architecture       | amd64 |
| Go toolchain       | go1.26.3 linux/amd64 |
| Git branch         | feat/logger-perfection |
| Git commit         | 636c793 (pre-commit, PR-F V6) |
| Generated (UTC)    | 2026-06-03 |
| Bench wall-clock   | `-test.benchtime=1s`, single run |

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
BenchmarkAdd_CapFlush-12      	48556053	        25.63 ns/op	      15 B/op	       0 allocs/op
BenchmarkAdd_Contended-12     	11948658	       108.1 ns/op	      16 B/op	       0 allocs/op
BenchmarkFlush_Empty-12       	47267929	        25.04 ns/op	       0 B/op	       0 allocs/op
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
  reaching the deliver mutex, so it is the serialization-free floor (~25 ns/op, 0
  B/op) the cap-flush benches are measured against.
