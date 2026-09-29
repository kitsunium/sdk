<!-- generated from framework/telemetry/telemetry_bench_test.go — run `cd framework && GOWORK=off go test -run='^$' -bench=. -benchmem -benchtime=2s ./telemetry/` to refresh -->
# Benchmarks — `framework/telemetry`

**The conclusion first: emitting costs a producer 11 ns and no allocation,
and a product with no exporter pays 3 ns for the interface call.** The
zero-allocation claim is also a test (`TestEmitAllocatesNothing`, race off,
in the alloc lane), so it cannot quietly stop being true.

## Reproducibility envelope

> **Numbers vary across machines.** This report stamps the box that produced
> them so cross-machine deltas can be evaluated honestly.

| Dimension | Value |
|---|---|
| CPU                | Intel(R) Core(TM) i5-3210M CPU @ 2.50GHz (2 cores, 4 threads; run with GOMAXPROCS=2) |
| RAM                | 15 GiB |
| OS / kernel        | Linux 6.12.107+deb13-amd64 |
| Architecture       | amd64 |
| Go toolchain       | go1.27.1 linux/amd64 |
| Git branch         | `feat/framework-design-first` |
| Git commit         | `3f305b32` (pre-commit) |
| Date               | 2026-09-28 |

## Results

| Benchmark | ns/op | B/op | allocs/op |
|---|---|---|---|
| `BenchmarkEmit`         | 11.09 | 0 | 0 |
| `BenchmarkEmitNop`      | 2.965 | 0 | 0 |
| `BenchmarkEmitParallel` | 21.59 | 0 | 0 |

## Reading them

- `BenchmarkEmit` runs with nobody draining, so after the first 1 048 576
  events the ring is full and the loop measures the DROP path: a load of the
  tail, a load of the slot's sequence, one counter increment. The claim path
  (a compare-and-swap, a 64-byte copy, a store, a non-blocking channel send)
  is the first million iterations; both are allocation-free.
- `BenchmarkEmitParallel` puts producers on one tail: the compare-and-swap
  contends, which is the price of one ring shared by every goroutine of a
  process. It stays below what one `trace` span costs to build.
