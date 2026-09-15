<!-- generated from internal/kernel/buffer/buffer_bench_test.go — run `cd internal/kernel && GOWORK=off go test -run='^$' -bench=. -benchmem -benchtime=1s ./buffer/` to refresh -->
# Benchmarks — `internal/kernel/buffer`

Pooled `*[]byte` scratch buffer (ADR 0010), a thin byte-slice specialisation over
`internal/kernel/recycler.CappedPool`. These benchmarks isolate the warm-pool
`Get` and the canonical round trip a call site actually performs. They exist to
guard the documented performance contract: **steady-state `Get`/`Put` is
0 allocs/op**. Storing `*[]byte` rather than `[]byte` is what keeps the path
allocation-free — no slice-header boxing into `sync.Pool`'s `any` payload on
every `Put`. Any regression to allocs/op > 0 on the warm path is a bug.

There is deliberately **no isolated `Put` benchmark**. `Put` cannot be measured
on its own for a `sync.Pool`-backed recycler: re-inserting a pointer every
iteration without a paired `Get` grows the pool's per-P shared slice until the
next GC, so the reported ns/op and B/op describe pool growth rather than the
cost of a real `Put`. The round trip is the only honest measurement of the
`Put` half.

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

> **This edition changes the reference platform.** The previous edition was
> measured on **arm64** (8-core, Linux 6.12.72-linuxkit) under **go1.26.4**; this
> one is **amd64** on the box stamped above. The two editions are NOT
> comparable — a reader drawing a delta across them would be measuring the
> architecture, not the code. The whole table was re-measured here rather
> than half-updated, so the cells stay comparable with each other.

## Results

```
goos: linux
goarch: amd64
pkg: github.com/kitsunium/sdk/internal/kernel/buffer
cpu: 12th Gen Intel(R) Core(TM) i7-1255U

benchmark             median ns/op   spread   min–max         B/op   allocs/op
Get_Steadystate-12           23.86     6.7%   23.57 – 25.17      0           0
GetPut_RoundTrip-12          24.19     4.6%   23.92 – 25.04      0           0
GetPut_Parallel-12           6.518    47.9%   3.972 – 7.096      0           0
```

## How to read this

- **`BenchmarkGet_Steadystate`** — the pool is primed before the timer, so every
  benched `Get` recycles a `*[]byte` instead of running the factory. The
  0 allocs/op figure is the steady-state contract the CI gate enforces.
- **`BenchmarkGetPut_RoundTrip`** — the canonical call-site pattern (borrow, write
  one byte into the backing array, return). Steady state is 0 allocs/op; the write
  ensures the bench reflects real scratch use rather than an empty borrow. This is
  also the honest measurement of `Put`'s cost (see note above).
- **`BenchmarkGetPut_Parallel`** — the round trip under `RunParallel`. `sync.Pool`'s
  per-P cache keeps each goroutine's borrow/return lock-free, so the per-op cost
  drops below the single-threaded number and stays 0-alloc under contention.
