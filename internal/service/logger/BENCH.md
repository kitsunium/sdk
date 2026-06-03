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
| Git branch         | feat/logger-perfection |
| Git commit         | (current HEAD) |
| Generated (UTC)    | 2026-06-03 |
| Bench wall-clock   | `-test.benchtime=2s`, single run |

## Results

`BenchmarkBuild_ZeroAlloc` exercises the chainable `Build(lg, lv).Str().Int().Send()`
hot path once the `recordPool` recycler is warm. It is **not** zero-alloc: the
single alloc/op is the unconditional defensive copy of `r.Attrs` in
`genericHandler.Handle` — `mergeAttrs` → `slices.Clone` (an `alloc_objects`
memprofile attributes ~96% of allocations to `slices.Clone[…AttrValue]` reached
via `(*genericHandler).Handle`). The builder + attr accumulation are pool-backed,
and the console sink writes into the recycled `kernel/buffer` and allocates
nothing per call. Making the path truly zero-alloc requires a conditional clone
and is tracked as a separate perf ticket (V115).

```
BenchmarkBuild_ZeroAlloc-12   	4,401,567	562.1 ns/op	249 B/op	1 allocs/op
```
