<!-- generated from framework/internal/kit/startup_bench_test.go — run `cd framework && GOWORK=off go test -run='^$' -bench=MainRunsADefaultCommand -benchtime=300x -count=3 ./internal/kit/` to refresh -->
# Benchmarks — `framework/internal/kit`

**The conclusion first: a warm `Main` that starts an app in the CLI
profile, runs its default command and stops it costs about 0.17 ms (p50
0.14 ms), p99 around 1 ms on a machine at load 14** — well inside the status
line's 40 ms budget. Since a CLI run stopped building the health probes, the
HTTP handler, the build's description and the start's announcement, it
allocates 278 times instead of 456. What it does not measure is the process's own start (exec, the Go
runtime, `init`), which a shell pays once per render when the binary is not
kept warm by a daemon.

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

Three runs of 1 000 iterations, in memory, logs discarded, under the shared
`ktn-heavy` slice (niced, three cores) while other agents compiled (load
average 14): the p99 is the machine's, the p50 and the allocations are the
run's.

| Benchmark | ns/op | p50 µs | p99 µs | B/op | allocs/op |
|---|---|---|---|---|---|
| `BenchmarkMainRunsADefaultCommand` | 170 580 | 140 | 1 039 | 26 448 | 278 |
| `BenchmarkMainRunsADefaultCommand` | 207 651 | 140 | 1 791 | 26 441 | 278 |
| `BenchmarkMainRunsADefaultCommand` | 172 915 | 143 | 833 | 26 441 | 278 |

Before the CLI run was trimmed (300 iterations, lighter load): 181 595 ns/op,
p50 164 µs, p99 468 µs, 37 167 B/op, 456 allocs/op.
