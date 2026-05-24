# Profiling the codec bench matrix

This guide explains how to drive the pprof + benchstat pipeline that
backs the codec-perf-micro loop. The Makefile already wires the
plumbing — the doc only covers usage and interpretation.

## Quick start

```bash
# Capture a baseline (cpu+mem+block+mutex profiles + bench.txt).
make profile WAVE=baseline

# Apply an optimisation, then capture the post wave.
make profile WAVE=post-cycle-1

# Compute deltas with mannwhitney p-values.
make benchstat-diff BEFORE=baseline AFTER=post-cycle-1
```

Output lives under `.bench/profiles/<WAVE>/`:

| File         | Reader command                                              |
| ------------ | ----------------------------------------------------------- |
| `cpu.out`    | `go tool pprof -http=: .bench/profiles/<wave>/cpu.out`      |
| `mem.out`    | `go tool pprof -alloc_objects .bench/profiles/<wave>/mem.out` |
| `block.out`  | `go tool pprof .bench/profiles/<wave>/block.out`            |
| `mutex.out`  | `go tool pprof .bench/profiles/<wave>/mutex.out`            |
| `bench.txt`  | benchstat input (raw `go test -bench` output)               |

The whole `.bench/` tree is gitignored — keep raw profiles per-machine,
cite `bench.txt` excerpts in commit bodies.

## Bench harness opt-ins

`pkg/v1/codec/main_test.go` only enables the block + mutex profiles when
the corresponding `-blockprofile` / `-mutexprofile` flag is set. Normal
`go test` invocations therefore stay at zero pprof overhead.

`pkg/v1/codec/codec_bench_test.go` calls `b.SetBytes(int64(...))` in
every throughput closure (Marshal, Append, Unmarshal, StreamEncode,
StreamDecode, plus parallel variants). Without `SetBytes`, benchstat
prints `ns/op` only; with it, you also get `MB/s` columns — the
throughput signal we steer Phase B optimisations on.

## Reading the profiles

### CPU profile

```
go tool pprof -http=: .bench/profiles/baseline/cpu.out
(pprof) top10 -cum
(pprof) web reflect
(pprof) list <hotFn>
```

Look for `reflect.Value.*`, `mallocgc`, `growslice` near the top of
`top10 -cum`. Each one is a Phase B lever.

### Allocation profile

Two views matter:

- `-alloc_objects` (count of allocations) → easier to read; aligned
  with `allocs/op`.
- `-alloc_space` (byte volume) → drives GC pressure; aligned with
  `B/op` and benchstat's MB/s deltas.

```
go tool pprof -alloc_objects .bench/profiles/baseline/mem.out
(pprof) top10 -cum
(pprof) list MarshalAppend
```

A drop in `alloc_objects` without a matching `alloc_space` drop usually
means you removed many small allocations; the opposite means you
removed a few large ones. Both ship as wins.

### Block / mutex profiles

Only meaningful when `-cpu=2,4,8` actually triggers contention. Cumulative
delay > 1 ms/sec of wall clock is the flag-this-cell threshold from the
plan's concurrency-contention lens.

## Diffing two waves

```
go tool pprof -base .bench/profiles/baseline/cpu.out \
              .bench/profiles/post-cycle-1/cpu.out
(pprof) top10 -cum
```

Negative numbers in the delta column = work removed. Positive numbers
in the delta column = work added (regression — investigate).

`make benchstat-diff` does the same job for the wall-clock + alloc
columns and reports a p-value per cell. The Phase B ship-gate is
p<0.05 on at least one cell per codec.

## Bench knobs

| Variable    | Default  | Effect                                              |
| ----------- | -------- | --------------------------------------------------- |
| `WAVE`      | `current`| Directory slug for `.bench/profiles/<WAVE>/`        |
| `COUNT`     | `10`     | `-count=<n>`; mannwhitney needs ≥10 samples         |
| `BENCHTIME` | `5s`     | `-benchtime=<dur>`; firms up nanos on small cells   |
| `BEFORE`    | `baseline`| Left-hand `benchstat-diff` operand                 |
| `AFTER`     | `$WAVE`  | Right-hand `benchstat-diff` operand                 |

## Canonical sources

- `pkg.go.dev/runtime/pprof` — profile API
- `pkg.go.dev/testing` — `b.Loop`, `b.SetBytes`, `-cpuprofile` flags
- `pkg.go.dev/golang.org/x/perf/cmd/benchstat` — flag surface + p-values
- `go.dev/blog/pprof` — interpretation primer
