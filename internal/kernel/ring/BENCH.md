<!-- generated from internal/kernel/ring/ring_bench_test.go — run `cd internal/kernel && GOWORK=off go test -run='^$' -bench=. -benchmem -benchtime=1s ./ring/` to refresh -->
# Benchmarks — `internal/kernel/ring`

Lock-free single-producer / single-consumer (SPSC) ring buffer built on
`sync/atomic` (ADR 0005 kernel/ring block `0.1.3.*`). These benchmarks isolate
every exported surface of the package and exist to defend the performance claim
that justifies the package: a non-blocking, zero-allocation FIFO hand-off
between exactly one producer and one consumer.

The happy-path benches (`TryWrite_Happy`, `TryRead_Happy`) keep the ring from
saturating/emptying so each operation lands on the fast path (two atomic loads,
one slot store/load, one atomic store) and proves **0 allocs/op**. The sentinel
benches (`TryWrite_Full`, `TryRead_Empty`) saturate/empty the ring so every call
returns the **pre-allocated** `Full` / `Empty` sentinel — also 0 allocs, which is
the regression guard for PR #15 Wave 6 finding #13 (sentinels must not be
constructed on the hot path). `Capacity` and `Len` cover the cheap reads.
`BenchmarkSPSC_ProducerConsumer` measures end-to-end throughput with a dedicated
producer/consumer goroutine pair (NOT `b.RunParallel`, which would spawn N
producers + N consumers and violate the SPSC invariant) across ring sizes 4, 64,
1024, and 65536 so the throughput-vs-depth curve is visible.

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
pkg: github.com/kitsunium/sdk/internal/kernel/ring
cpu: 12th Gen Intel(R) Core(TM) i7-1255U

benchmark                            median ns/op   spread   min–max          B/op   allocs/op
New-12                                      1 646    46.0%   1 638 – 2 395   9 472           1
TryWrite_Happy-12                           30.83     4.2%   30.79 – 32.10       0           0
TryRead_Happy-12                            30.86     3.4%   30.77 – 31.83       0           0
TryWrite_Full-12                            8.771     3.6%   8.742 – 9.062       0           0
TryRead_Empty-12                            8.302    10.1%   7.858 – 8.695       0           0
Capacity-12                                 3.337     2.6%   3.312 – 3.399       0           0
Len-12                                      4.709     3.7%   4.622 – 4.797       0           0
SPSC_ProducerConsumer/size4-12              306.5    10.5%   291.0 – 323.3       0           0
SPSC_ProducerConsumer/size64-12             244.3    19.9%   226.1 – 274.7       0           0
SPSC_ProducerConsumer/size1024-12           192.5    53.2%   188.2 – 290.6       0           0
SPSC_ProducerConsumer/size65536-12          186.5     5.6%   177.0 – 187.4       0           0
```

## How to read this

- **`BenchmarkNew`** — construction allocates the `queueRing` struct plus its
  `cap+1` slots backing array (`1024+1` ints here): **1 alloc/op**, the only
  benchmark that allocates. Everything below is the steady-state hot path.
- **`BenchmarkTryWrite_Happy` / `BenchmarkTryRead_Happy`** — single producer /
  single consumer, ring kept un-saturated / un-empty, so each call stays on the
  fast path. **0 allocs/op** confirms the slot store/load reuses the backing
  array.
- **`BenchmarkTryWrite_Full` / `BenchmarkTryRead_Empty`** — saturated / empty
  ring, so every call returns the package-level `Full` / `Empty` sentinel.
  **0 allocs/op** is the guard that these sentinels stay pre-allocated and are
  never constructed per call.
- **`BenchmarkCapacity` / `BenchmarkLen`** — the cheap reads: an immutable field
  widen and a two-atomic-load snapshot subtract. Both 0 allocs, low-single-digit
  ns/op.
- **`BenchmarkSPSC_ProducerConsumer/sizeN`** — one dedicated producer goroutine
  and one dedicated consumer goroutine move `b.N` records end-to-end across the
  lock-free buffer at ring depths 4 / 64 / 1024 / 65536. ns/op is per-record
  wall time across the full producer→consumer hand-off (including spin-retry on
  `Full`/`Empty`), not a single `TryWrite`. Deeper rings broadly amortise the
  back-pressure spin; the exact size-to-size ordering is scheduler-sensitive and
  jitters run-to-run on a shared machine, so read the magnitude (tens of ns per
  record), not a strictly monotonic curve.
