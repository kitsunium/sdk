<!-- generated from framework/kit/fresh_process_bench_test.go — run `cd framework && GOWORK=off go test -run='^$' -bench=FreshProcess -benchtime=200x -count=3 ./kit/` to refresh -->
# Benchmarks — `framework/kit`

**The conclusion first: a fresh process of the smallest kit product
(`testdata/cliprobe`: one service, one default fail-safe command) runs in
about 5.1–5.4 ms, p99 6–8 ms; an empty Go program takes 2.0 ms on the same
box. Of the ~3 ms between them, package initialisation is about 0.7 ms
(`GODEBUG=inittrace=1`); the rest is the binary — 20.6 MB, 11.6 MB of it code
reachable only from `App.Main`, which costs ~75 page faults and ~0.2 ms by
being linked at all.** Differences under ~0.2 ms are not measurable here:
the same bytes exec'd from two paths ran 0.19 ms apart (§Controls), because
what a fresh process pays is mostly the pages it touches.

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
| Git commit         | `c3f9f517` |
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

The benchmark alone does not separate the two trees, and neither does
anything finer on this machine. `kit startup` (kitsunium/platform#80) builds
both trees `-trimpath` and measures a third binary beside them — the working
tree's, copied byte for byte — so the noise floor is printed with the delta.
Three runs of `kit startup -base b02d9574 -rounds 8 -runs 200
./kit/testdata/cliprobe`:

| Run | deps | inits | init ms (base → tree) | wall p50 ms (base → tree) | delta | noise (control − tree) |
|---|---|---|---|---|---|---|
| 1 | 369 → 364 | 188 → 183 | 1.230 → 1.187 | 5.180 → 5.348 | +0.168 | +0.009 |
| 2 | 369 → 364 | 188 → 183 | 1.294 → 1.261 | 5.377 → 5.407 | +0.031 | −0.016 |
| 3 | 369 → 364 | 188 → 183 | 1.233 → 1.240 | 5.102 → 5.200 | +0.098 | +0.054 |

What ADR 0172 and the #276 change remove is countable — five packages,
five package initialisations, 88 KB, a hostname read, a regexp compile, a
build-information parse, a `runtime.FuncForPC` per declaration — and below
what a fresh process's wall time resolves here: the rounds of one binary
spread over 0.3–0.7 ms, and the delta moves between runs by more than it
measures.

## Controls

Identical bytes are not identical runs. The working tree's cliprobe built
`-trimpath` from two directories gives the same file byte for byte; exec'd
in eight alternated rounds of 300, the two copies measured 4.73 and 4.92 ms.
Without `-trimpath` the build directory is part of the binary, and the same
source built from two directories differs by as much again. A change
smaller than ~0.2 ms is judged by what it removes — packages initialised,
work done at the start — never by a wall time on this machine.
