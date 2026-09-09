<!-- generated from internal/kernel/singleflight/singleflight_bench_test.go — run `cd internal/kernel && GOWORK=off go test -run='^$' -bench=. -benchmem -benchtime=1s ./singleflight/` to refresh -->
# Benchmarks — `internal/kernel/singleflight`

Call deduplication (ADR 0049). These benchmarks answer one question and it is
not "how fast is it": **when is a `Group` worth using at all?** The primitive
saves executions of `fn` and pays for that saving in scheduler latency, so the
only meaningful comparison is against calling `fn` directly.

## Reproducibility envelope

> **Numbers vary across machines.** This report stamps the box that produced
> them so cross-machine deltas can be evaluated honestly.

| Dimension | Value |
|---|---|
| CPU cores          | 8 (AMD EPYC 7351P) |
| RAM                | 15 GiB |
| OS / kernel        | Linux 6.12.101+deb13-amd64 |
| Architecture       | amd64 |
| Go toolchain       | go1.27.1 linux/amd64 |
| Git branch         | `jaimerias-que-tu-te-connect` |
| Git commit         | `8b576c6` (pre-commit) |
| Generated (UTC)    | 2026-09-09 |
| Bench wall-clock   | `-test.benchtime=1s`, single run |

## Results

```
BenchmarkDo_Uncontended-8            589851   2007 ns/op                        368 B/op   6 allocs/op
BenchmarkDo_SharedKeyParallel-8      440044   2879 ns/op   0.4299 fn-runs/op    174 B/op   3 allocs/op
BenchmarkDo_DistinctKeysParallel-8   336994   3224 ns/op                        375 B/op   7 allocs/op
BenchmarkBaseline_DirectCall-8    1000000000      0.7002 ns/op                    0 B/op   0 allocs/op
```

## The number that decides whether to use this

**A leading call costs ≈ 2 µs.** That is `BenchmarkDo_Uncontended`: one caller,
one key, an `fn` that returns immediately. Almost none of it is allocation
(6 allocs — the channel, the call record, the two context wrappers, the map
entry); it is a goroutine **park/unpark round trip**. `fn` runs on a goroutine
of its own (see `CLAUDE.md` §"Why a goroutine"), so the caller hands off,
blocks on a channel, and is woken by the scheduler.

So the rule, measured rather than asserted:

> A `Group` pays for itself when `fn` costs materially more than ~2 µs **and**
> callers actually collide.

A network round trip (10³–10⁵ µs), a disk read, an L2 cache lookup, a
cryptographic key derivation — all far above the line. A map lookup, an
arithmetic result, a small in-process computation — all far below it, and
wrapping one in a `Group` makes it three orders of magnitude slower for a
saving that does not exist.

`BenchmarkBaseline_DirectCall` (0.70 ns/op) is the floor and is in the table
for exactly this reason: without it, 2 µs reads as "fast".

## How to read the rest

- **`BenchmarkDo_SharedKeyParallel`** — every `P` goroutine asks for the same
  key. The custom metric is the point: **0.43 executions of `fn` per call**, so
  57 % of calls were served by someone else's work. With a real (slow) `fn`
  that ratio is the saving; here it merely proves deduplication happens under
  contention. Allocations drop to 3/op for the same reason — a follower
  allocates nothing at all, so the average falls as the shared fraction rises.
  The ratio is workload-dependent: it rises with `fn`'s duration (a longer call
  collects more followers) and falls as `fn` approaches zero.
- **`BenchmarkDo_DistinctKeysParallel`** — nothing is ever shared, so every
  call pays the full leading price *and* contends on the group mutex
  (3224 ns/op vs 2007 uncontended). This is the shape a `Group` should **not**
  be used for, and the number is here to say so out loud rather than leave it
  to be discovered in production.

## What is deliberately not measured

- **Abandonment.** A caller leaving early is a correctness property (the call
  survives for the others), pinned by
  `TestAbandonedLeaderDoesNotCondemnTheFollowers`, not a throughput one.
- **Cross-process deduplication.** There is none — see `CLAUDE.md` §"One
  process, and only one".
