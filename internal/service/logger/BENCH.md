<!-- generated from internal/service/logger/builder_bench_test.go — run `go test -bench=. -benchmem -benchtime=10s ./logger/` to refresh -->
# Benchmarks — `internal/service/logger`

## Reproducibility envelope

> **Numbers vary across machines.** This report stamps the box that produced
> them so cross-machine deltas can be evaluated honestly.

| Dimension | Value |
|---|---|
| CPU                | 12th Gen Intel(R) Core(TM) i7-1255U |
| CPU cores          | 12 |
| RAM                | 15 GiB |
| OS / kernel        | Linux 6.12.88+deb13-amd64 |
| Architecture       | amd64 |
| Go toolchain       | go1.26.3 linux/amd64 |
| Git branch         | refactor/kernel-recycler |
| Git commit         | (current HEAD) |
| Generated (UTC)    | 2026-05-25 |
| Bench wall-clock   | `-test.benchtime=2s`, single run |

## Results

`BenchmarkBuild_ZeroAlloc` exercises the chainable `Build(lg, lv).Str().Int().Send()`
hot path once the `recordPool` recycler is warm. The single alloc/op is the
console sink's formatted line; the builder + attr accumulation are pool-backed.

```
BenchmarkBuild_ZeroAlloc-12   	3,583,167	781.4 ns/op	277 B/op	1 allocs/op
```
