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
| CPU cores          | 8 |
| RAM                | 11 GiB |
| OS / kernel        | Linux 6.12.72-linuxkit |
| Architecture       | arm64 |
| Go toolchain       | go1.26.4 linux/arm64 |
| Git branch         | feat/issue-18-kernel-buffer-bench |
| Git commit         | f3b1610 |
| Generated (UTC)    | 2026-06-19 |
| Bench wall-clock   | `-test.benchtime=1s`, single run |

## Results

```
goos: linux
goarch: arm64
pkg: github.com/kitsunium/sdk/internal/kernel/buffer
BenchmarkGet_Steadystate-8    	91908540	        13.03 ns/op	       0 B/op	       0 allocs/op
BenchmarkGetPut_RoundTrip-8   	88810491	        13.34 ns/op	       0 B/op	       0 allocs/op
BenchmarkGetPut_Parallel-8    	292483033	         3.782 ns/op	       0 B/op	       0 allocs/op
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
