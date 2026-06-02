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
| RAM                | 15 GiB |
| OS / kernel        | Linux 6.12.90+deb13-amd64 |
| Architecture       | amd64 |
| Go toolchain       | go1.26.3 linux/amd64 |
| Git branch         | feat/logger-perfection |
| Git commit         | (current HEAD, pre-commit) |
| Generated (UTC)    | 2026-06-02 |
| Bench wall-clock   | `-test.benchtime=1s`, single run |

## Results

```
BenchmarkWrite-12    	 2301624	       524.2 ns/op	       0 B/op	       0 allocs/op
```

## How to read this

- **`0 allocs/op`, `0 B/op` — the Write path allocates nothing.** The sink
  appends a caller-owned `[]byte` straight to the active `*os.File` under its
  mutex; the threshold accounting is integer arithmetic and the
  stashed-tick-error check is a single nil compare. No buffer is allocated per
  record, even with the interval daemon running. This is the on-disk terminal
  of the zero-alloc logger hot path (ADR 0014), so it preserves the invariant
  rather than introducing a deferring cost (contrast `dbsink`, which batches).
- **~524 ns/op is dominated by the `write(2)` syscall**, not by SDK overhead —
  the same cost the plain `service/writer/file` sink pays. Interval rotation
  adds a *background* ticker that costs nothing on the Write path until it
  fires; rotation itself (close/rename/gzip/prune) is amortised across
  `RotateEvery` and happens off the measured loop.
- **CPU/RAM minimality** comes from doing the least possible per record (one
  syscall, no allocation) and pushing rotation + gzip + retention pruning to the
  rare interval boundary rather than the hot path.
