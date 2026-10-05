<!-- generated from internal/kernel/semver/semver_bench_test.go — run `cd internal/kernel && GOWORK=off go test -run='^$' -bench=. -benchmem -count=10 -benchtime=200ms ./semver/` to refresh -->
# Benchmarks — `internal/kernel/semver`

A SemVer 2.0.0 comparison and Go pseudo-version reading, written to replace
`golang.org/x/mod/semver` and the three pseudo-version functions of
`golang.org/x/mod/module`. The benchmark that matters is therefore the one
against x/mod itself: a replacement that is slower than what it replaces needs
saying, and one that is faster needs proving.

## Reproducibility envelope

> **Numbers vary across machines, and these were taken on a busy one.** The
> load average was between 29 and 60 on 10 cores while they ran — several other
> builds shared the machine — so a single run swung by ±100 %. Every figure
> below is a median over 10 or 20 runs with its spread, or the minimum over
> those runs, which is the better estimate of intrinsic cost under contention.
> Ratios between the two implementations were measured in the same process,
> interleaved, and are the trustworthy part.

| Dimension | Value |
|---|---|
| CPU cores          | 10 (Apple M1 Pro) |
| RAM                | 16 GiB |
| OS / kernel        | macOS 26.6.2 (Darwin 25.6.0) |
| Architecture       | arm64 |
| Go toolchain       | go1.27.1 darwin/arm64 |
| Git branch         | `refactor/sdk-tree-reorg--p3-semver` |
| Git commit         | `390aa80f` (pre-commit) |
| Generated (UTC)    | 2026-10-03 |
| Bench wall-clock   | `-benchtime=200ms -count=10` (own); `-benchtime=50ms -count=20` (against x/mod) |
| Load average       | 29–60 on 10 cores |

## Results — this package

```
                      sec/op (median ± spread, n=10)   min      B/op   allocs/op
Compare                 56.70n ± 26%                  52.79n     0       0
CompareReleases         24.58n ± 25%                  23.93n     0       0
ComparePrereleases      70.18n ±  6%                  68.68n     0       0
IsValid                 33.52n ± 120%                 30.83n     0       0
Prerelease              29.88n ±  4%                  29.22n     0       0
IsPseudoVersion         63.53n ± 91%                  58.49n     0       0
PseudoVersionTime       146.1n ± 21%                  135.0n     0       0
```

Nothing allocates. That is a property, not an observation: each function's
design budget (`allocs: 0`, ADR 0165) is the `perf_gen_test.go` kit gen writes,
run in the race-off alloc lane, and `perf_fixtures_test.go`'s doc comment
records the mutation it catches — `strings.Split` in place of the
in-place identifier walk passes every functional test and costs 1 000
allocations over 500 comparisons.

## Against `golang.org/x/mod` — the number that justifies the package

The suite must not import x/mod, so the comparison runs out of tree: a
throwaway module requiring `golang.org/x/mod v0.41.0` and this package through a
`replace`, each benchmark split into `impl=kernel` and `impl=xmod`
sub-benchmarks over the same inputs, `go test -bench . -count=20
-benchtime=50ms`, compared with `benchstat -col /impl`. `Compare` uses x/mod's
own `BenchmarkCompare` input (`v1.0.0+metadata-dash` against
`v1.0.0+metadata-dash1`).

| Benchmark | kernel (median) | x/mod (median) | verdict | kernel (min) | x/mod (min) |
|---|---|---|---|---|---|
| `Compare` (x/mod's input) | 59.23 ns ± 5 % | 63.03 ns ± 23 % | no difference (p = 0.34) | 53.75 ns | 50.75 ns |
| `Compare`, two releases | 22.59 ns ± 2 % | 24.43 ns ± 7 % | kernel 8 % faster | 21.89 ns | 22.98 ns |
| `Compare`, two pre-releases | 68.49 ns ± 3 % | 67.75 ns ± 4 % | no difference (p = 0.27) | 67.11 ns | 66.24 ns |
| `IsValid` | 29.48 ns ± 8 % | 35.70 ns ± 30 % | parity: the medians part under x/mod's ± 30 %, the minimums do not | 28.61 ns | 28.20 ns |
| `Prerelease` | 30.20 ns ± 10 % | 26.14 ns ± 4 % | x/mod 13 % faster | 29.23 ns | 25.66 ns |
| `IsPseudoVersion` | 54.71 ns ± 2 % | 881.05 ns ± 2 % | **kernel 16× faster** | 53.83 ns | 867.7 ns |
| `PseudoVersionRev` | 55.21 ns ± 1 % | 943.85 ns ± 2 % | **kernel 17× faster** | 54.12 ns | 927.5 ns |
| `PseudoVersionTime` | 136.1 ns ± 3 % | 1 055 ns ± 5 % | **kernel 7.8× faster** | 133.2 ns | 1 028 ns |

All sixteen rows allocate nothing.

**The core readings are at parity** — within 13 % either way, most of it inside
the noise. Two rewrites were needed to get there, and the first draft is worth
recording because the mistake is the obvious one: walking identifiers with
`strings.Cut` spent 45 % of `IsValid` in `IndexByteString` call overhead on
strings of a few bytes, and ran 2–3× slower than x/mod. The parser now
classifies each byte once, in one pass, with one comparison per class
(`isDigit` and `isLetter` are single unsigned comparisons, checked over all 256
bytes by `TestByteClassesMatchTheirDefinitions`).

**The pseudo-version readings are an order of magnitude faster**, for a
structural reason: x/mod recognises a pseudo-version with a regular expression
(after a full SemVer parse), this package by WHERE the stamp sits in a
pre-release it has already parsed — the last identifier, alone in a `vX.0.0`
or right after an identifier `0`. `PseudoVersionTime` is dominated by
`time.Parse` in both.

## What is deliberately not measured

- **Long versions.** Every cost here is linear in the version's length and
  versions are short; a 10 kB pre-release would measure the loop, not a
  decision.
- **Sorting.** `slices.SortFunc(list, semver.Compare)` is n log n comparisons
  at the figures above; a sort benchmark would measure `slices`.
