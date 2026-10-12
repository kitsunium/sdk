<!-- generated from framework/kit/fresh_process_bench_test.go — run `cd framework && GOWORK=off go test -run='^$' -bench=FreshProcess -benchtime=200x -count=3 ./kit/` to refresh -->
# Benchmarks — `framework/kit`

**The conclusion first: a fresh process of the smallest kit product
(`testdata/cliprobe`: one service, one default fail-safe command) runs in
about 5.1–5.4 ms, p99 6–8 ms; an empty Go program takes 2.0 ms on the same
box. Of the ~3 ms between them, package initialisation is about 0.7 ms
(`GODEBUG=inittrace=1`); the rest is the binary — 20.6 MB, 11.6 MB of it code
reachable only from `App.Main`, which costs ~75 page faults and ~0.2 ms by
being linked at all.** Differences under ~0.15 ms are not measurable here:
the same source built from another directory, or with `-trimpath`, runs
0.1–0.3 ms apart (§Controls), because what a fresh process pays is mostly
the pages it touches, and the layout decides which.

Before kit's catalogues were loaded lazily and in linear time, the same
probe took 37 ms: the package's init checked every French key against a
sorted copy of the English catalogue, one sort per key.
`BenchmarkMainRunsADefaultCommand` in `framework/internal/kit` measures what
a warm process pays for the run itself: 0.14 ms.

## Reproducibility envelope

> **Numbers vary across machines.** This report stamps the box that produced
> them so cross-machine deltas can be evaluated honestly.

| Dimension | Value |
|---|---|
| CPU                | AMD EPYC 7351P 16-Core Processor (8 vCPU) |
| RAM                | 15 GiB (balloon) |
| OS / kernel        | Linux 6.12.101+deb13-amd64 |
| Architecture       | amd64 |
| Go toolchain       | go1.27.2 linux/amd64 |
| Git branch         | `fix/sdk-release-lock` |
| Git commit         | `132abe86` + the #276 change (pre-commit) |
| Date               | 2026-10-12 |

## Results

Three runs of 200 execs each, twice, alternating the tree before ADR 0172
(`b02d9574`) and after it and the #276 change.

| Benchmark | Tree | ns/op | p50 µs | p99 µs |
|---|---|---|---|---|
| `BenchmarkAFreshProcessRunsADefaultCommand` | before | 5 520 732 | 5 335 | 12 268 |
| `BenchmarkAFreshProcessRunsADefaultCommand` | before | 5 247 222 | 5 249 | 5 929 |
| `BenchmarkAFreshProcessRunsADefaultCommand` | before | 5 247 532 | 5 220 | 6 691 |
| `BenchmarkAFreshProcessRunsADefaultCommand` | after | 5 369 056 | 5 351 | 6 969 |
| `BenchmarkAFreshProcessRunsADefaultCommand` | after | 5 348 616 | 5 327 | 6 440 |
| `BenchmarkAFreshProcessRunsADefaultCommand` | after | 5 553 805 | 5 397 | 7 847 |
| `BenchmarkAFreshProcessRunsADefaultCommand` | before | 5 459 635 | 5 493 | 6 245 |
| `BenchmarkAFreshProcessRunsADefaultCommand` | before | 5 572 637 | 5 364 | 12 435 |
| `BenchmarkAFreshProcessRunsADefaultCommand` | before | 5 245 773 | 5 221 | 6 208 |
| `BenchmarkAFreshProcessRunsADefaultCommand` | after | 5 173 276 | 5 107 | 6 332 |
| `BenchmarkAFreshProcessRunsADefaultCommand` | after | 5 192 653 | 5 140 | 6 519 |
| `BenchmarkAFreshProcessRunsADefaultCommand` | after | 5 234 346 | 5 138 | 6 512 |

The benchmark alone does not separate the two trees. Prebuilt binaries
exec'd in ten alternated rounds of 300 (median of the rounds' p50) do:

| Binary | deps | inits | init median | wall p50 | CPU p50 |
|---|---|---|---|---|---|
| empty Go program | — | — | — | 1.99 ms | 2.15 ms |
| cliprobe before ADR 0172 | 369 | 188 | 1.18 ms | 5.07–5.11 ms | 5.90–5.96 ms |
| cliprobe after ADR 0172 | 364 | 184 | 1.12 ms | 5.05 ms | 5.87–5.88 ms |
| cliprobe after #276 | 364 | 183 | 1.05 ms | 4.84–4.89 ms | 5.65–5.69 ms |

(init median: the median of 200 alternated runs of the sum of
`inittrace`'s clock, which includes the trace's own writes.)

## Controls

The same source, `132abe86`, as a two-role binary (a client CLI role, a
daemon role with a listener and two jobs — a status line's shape), ten
alternated rounds of 300:

| Build | wall p50 | CPU p50 |
|---|---|---|
| built from one directory | 4.81–4.93 ms | 5.63–5.69 ms |
| built from another directory | 4.73 ms | 5.57 ms |
| built with `-trimpath` | 4.62–4.64 ms | 5.43 ms |

A change smaller than these gaps is judged by what it removes — packages
initialised, work done at the start — not by a wall time.
