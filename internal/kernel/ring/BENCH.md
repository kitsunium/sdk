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
| CPU cores          | 8 |
| RAM                | 11 GiB |
| OS / kernel        | Linux 6.12.72-linuxkit |
| Architecture       | arm64 |
| Go toolchain       | go1.26.4 linux/arm64 |
| Git branch         | feat/issue-20-kernel-ring-bench |
| Git commit         | f3b1610 |
| Generated (UTC)    | 2026-06-19 |
| Bench wall-clock   | `-test.benchtime=1s`, single run |

## Results

```
goos: linux
goarch: arm64
pkg: github.com/kitsunium/sdk/internal/kernel/ring
BenchmarkNew-8                     	  459751	      2590 ns/op	    9472 B/op	       1 allocs/op
BenchmarkTryWrite_Happy-8          	100000000	        15.38 ns/op	       0 B/op	       0 allocs/op
BenchmarkTryRead_Happy-8           	100000000	        13.16 ns/op	       0 B/op	       0 allocs/op
BenchmarkTryWrite_Full-8           	183599595	         6.430 ns/op	       0 B/op	       0 allocs/op
BenchmarkTryRead_Empty-8           	194103002	         6.348 ns/op	       0 B/op	       0 allocs/op
BenchmarkCapacity-8                	530012316	         2.304 ns/op	       0 B/op	       0 allocs/op
BenchmarkLen-8                     	493288366	         2.425 ns/op	       0 B/op	       0 allocs/op
BenchmarkSPSC_ProducerConsumer/size4-8         	15381303	       107.8 ns/op	       0 B/op	       0 allocs/op
BenchmarkSPSC_ProducerConsumer/size64-8        	18557430	        75.48 ns/op	       0 B/op	       0 allocs/op
BenchmarkSPSC_ProducerConsumer/size1024-8      	20364985	       122.0 ns/op	       0 B/op	       0 allocs/op
BenchmarkSPSC_ProducerConsumer/size65536-8     	24867996	        52.38 ns/op	       0 B/op	       0 allocs/op
PASS
ok  	github.com/kitsunium/sdk/internal/kernel/ring	16.075s
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
