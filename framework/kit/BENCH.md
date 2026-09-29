<!-- generated from framework/kit/fresh_process_bench_test.go — run `cd framework && GOWORK=off go test -run='^$' -bench=FreshProcess -benchtime=200x -count=3 ./kit/` to refresh -->
# Benchmarks — `framework/kit`

**The conclusion first: a fresh process of the smallest kit product
(`testdata/cliprobe`: one service, one default fail-safe command) runs in
about 7 ms, p99 14–27 ms; an empty Go program takes 2.3 ms on the same box.**
Before kit's catalogues were loaded lazily and in linear time, the same
probe took 37 ms: the package's init checked every French key against a
sorted copy of the English catalogue, one sort per key — 47 ms, 5.5 MB and
14 230 allocations on every start, whatever the product did.

What is left (~5 ms over an empty program) is the binary's size — 23.6 MB
against 2.1 MB, a framework that links every subsystem an app may start —
and about 2.8 ms of package initialisation across 200 packages, the
largest kit's own (0.5 ms: its declaration grammars) and the model's
(0.46 ms). `BenchmarkMainRunsADefaultCommand` in `framework/internal/kit`
measures what a warm process pays for the run itself: 0.14 ms.

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
| Date               | 2026-09-29 |

## Results

Three runs of 200 execs under the shared `ktn-heavy` slice (niced, three
cores), load average 6.5.

| Benchmark | ns/op | p50 µs | p99 µs |
|---|---|---|---|
| `BenchmarkAFreshProcessRunsADefaultCommand` | 7 541 593 | 6 949 | 14 376 |
| `BenchmarkAFreshProcessRunsADefaultCommand` | 8 474 348 | 7 279 | 27 463 |
| `BenchmarkAFreshProcessRunsADefaultCommand` | 7 685 260 | 7 244 | 16 888 |

The same slice, 200 execs each, for reference: an empty Go program p50
2.26 ms, p99 3.79 ms; the probe before the catalogue fix p50 36.8 ms, p99
58.7 ms.
