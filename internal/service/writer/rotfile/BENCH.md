<!-- generated from internal/service/writer/rotfile/rotate_interval_bench_test.go — run `cd internal/service && GOWORK=off go test -run='^$' -bench=. -benchmem -benchtime=1s ./writer/rotfile/` to refresh -->
# Benchmarks — `internal/service/writer/rotfile`

Size-/time-capped rotating file sink (ADR 0014 + ADR 0015). The benchmark
isolates the **Write** path with interval rotation enabled (`RotateEvery: 1h`,
so the ticker daemon is running but never fires during the measured loop) and a
large `MaxBytes` (`1<<30`) so no rotation happens mid-loop. It measures the
steady-state append cost: one mutex acquisition, the stashed-tick-error check,
the size-threshold check, and the `*os.File.Write` syscall.

## Reproducibility envelope

> **Numbers vary across machines.** This report stamps the box that produced
> them so cross-machine deltas can be evaluated honestly.

| Dimension | Value |
|---|---|
| CPU                | 12th Gen Intel(R) Core(TM) i7-1255U |
| CPU cores          | 12 |
| RAM                | 15.3 GiB |
| OS / kernel        | Linux 6.12.107+deb13-amd64 (Debian 13 trixie) |
| Architecture       | amd64 |
| Go toolchain       | go1.27.1 linux/amd64 |
| Git branch         | fix/bench-127 |
| Git commit         | c08d730 |
| Generated (UTC)    | 2026-09-15 |
| Bench wall-clock   | `-benchtime=1s -count=5`, median of 5 runs |

> **What these numbers support.** `allocs/op` is exact: all 5 repeats agreed on
> every cell, in every package. `B/op` is exact too **except where a cell
> carries `*`**, which marks five values that were not identical and a median
> reported in their place. Both columns are **unchanged** from a go1.26.4 run
> of this same code on this same box (124 benchmarks compared SDK-wide, 44 of
> them allocating, zero counter moved). `ns/op` are medians and carry the
> `spread` shown, which is a **within-run** figure that understates run-to-run
> variance: re-running the identical binary on this box moved individual cells
> by up to 94 %. Read ns/op as an order of magnitude on this box, never as a
> cross-edition or cross-machine delta.

## Results

```
goos: linux
goarch: amd64
pkg: github.com/kitsunium/sdk/internal/service/writer/rotfile
cpu: 12th Gen Intel(R) Core(TM) i7-1255U

benchmark   median ns/op   spread   min–max         B/op   allocs/op
Write-12           674.5    11.1%   644.0 – 718.7      0           0
```

## How to read this

- **`0 allocs/op`, `0 B/op` — the Write path allocates nothing.** The sink
  appends a caller-owned `[]byte` straight to the active `*os.File` under its
  mutex; the threshold accounting is integer arithmetic and the
  stashed-tick-error check is a single nil compare. No buffer is allocated per
  record, even with the interval daemon running. This is the on-disk terminal
  of the zero-alloc logger hot path (ADR 0014), so it preserves the invariant
  rather than introducing a deferring cost (contrast `dbsink`, which batches).
- **~675 ns/op is dominated by the `write(2)` syscall**, not by SDK overhead —
  the same cost the plain `service/writer/file` sink pays. Interval rotation
  adds a *background* ticker that costs nothing on the Write path until it
  fires; rotation itself (close/rename/gzip/prune) is amortised across
  `RotateEvery` and happens off the measured loop.
- **CPU/RAM minimality** comes from doing the least possible per record (one
  syscall, no allocation) and pushing rotation + gzip + retention pruning to the
  rare interval boundary rather than the hot path.
