<!-- generated from internal/kernel/concur/singleflight/singleflight_bench_test.go — run `cd internal/kernel && GOWORK=off go test -run='^$' -bench=. -benchmem -benchtime=1s ./concur/singleflight/` to refresh -->
# Benchmarks — `internal/kernel/concur/singleflight`

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
| Go toolchain       | go1.27.3 linux/amd64 |
| Git branch         | `wip/sdk-lot-6-cache` |
| Git commit         | `23531ae` (pre-commit) |
| Generated (UTC)    | 2026-11-02 |
| Bench wall-clock   | `-test.benchtime=1s`, single run |

## Results

```
BenchmarkDo_Uncontended-8              535807   2433 ns/op                        384 B/op   6 allocs/op
BenchmarkDo_SharedKeyParallel-8        421166   2857 ns/op   0.6512 fn-runs/op    289 B/op   5 allocs/op
BenchmarkDo_DistinctKeysParallel-8     486736   2227 ns/op                        391 B/op   6 allocs/op
BenchmarkDo_DistinctKeysParallel16-8   537745   2558 ns/op                        391 B/op   7 allocs/op
BenchmarkBaseline_DirectCall-8      1000000000      0.7575 ns/op                    0 B/op   0 allocs/op
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

## The group lock, taken off the leaders' path (#1035)

Measured 2026-11-02, go1.27.3, branch `wip/sdk-lot-6-cache`, 8-core AMD EPYC
7351P VM (shared), `-cpu=8 -benchtime=200ms -count=6`, benchstat. Three builds
on one box: the base (`ee1f48b`), step 1 alone (the map's critical section
holds the map and nothing else; built by setting the shard count to 1), and
step 2 (sixteen shards).

```
                            │ sf-base.txt │            sf-step1.txt             │             sf-new.txt              │
                            │   sec/op    │    sec/op     vs base               │    sec/op     vs base               │
Do_Uncontended-8              2.271µ ± 5%   2.259µ ± 11%        ~ (p=1.000 n=6)   2.236µ ±  3%        ~ (p=0.368 n=6)
Do_SharedKeyParallel-8        3.000µ ± 9%   3.174µ ±  7%        ~ (p=0.132 n=6)   3.208µ ± 10%        ~ (p=0.240 n=6)
Do_DistinctKeysParallel-8     3.537µ ± 8%   3.075µ ±  6%  -13.06% (p=0.002 n=6)   2.203µ ±  6%  -37.72% (p=0.002 n=6)
Do_DistinctKeysParallel16-8                 3.300µ ± 12%                          2.250µ ±  5%
geomean                       2.888µ        2.920µ         -2.93%                 2.442µ        -13.12%
```

- **Step 1** — the context derivation, the call record and the `go` statement
  out of the critical section — takes `Do_DistinctKeysParallel` from 3.54 µs to
  3.08 µs (−13 %): one lock, held for a map lookup or insert.
- **Step 2** — sixteen shards, each a map under its own mutex — takes it to
  2.20 µs (−38 %), now **no more than `Do_Uncontended`** (2.24 µs): distinct
  keys under eight goroutines no longer wait for one another.
  `Do_DistinctKeysParallel16` (sixteen goroutines per P, a miss storm's shape)
  reads the same.
- **`Do_Uncontended` keeps its 6 allocations**: the leader allocates exactly
  what it did, outside the lock instead of inside.
- **`Do_SharedKeyParallel`'s `fn-runs/op` is unchanged within noise**
  (p = 0.24). Its B/op and allocs/op rise by one allocation's worth: a leader
  that loses the insert to another (a race the old lock made impossible)
  built a candidate it then discards — the price of building it unlocked.

## How to read the rest

- **`BenchmarkDo_SharedKeyParallel`** — every `P` goroutine asks for the same
  key. The custom metric is the point: **0.43 to 0.65 executions of `fn` per
  call** across runs, so a third to a half of the calls were served by someone
  else's work. With a real (slow) `fn` that ratio is the saving; here it merely
  proves deduplication happens under contention. Allocations fall below the
  leader's 6/op for the same reason — a follower allocates nothing at all, so
  the average falls as the shared fraction rises (3 to 5/op: a leader that loses
  the insert to another also built a candidate it discards, see above). The
  ratio is workload-dependent: it rises with `fn`'s duration (a longer call
  collects more followers) and falls as `fn` approaches zero.
- **`BenchmarkDo_DistinctKeysParallel`** — nothing is ever shared, so every
  call pays the full leading price. Until #1035 it also contended on the one
  group mutex (3224 ns/op against 2007 uncontended in the 2026-09-09 run);
  since then a key locks one of sixteen shards, for a map lookup or insert
  only, and the row reads no more than `Do_Uncontended` (2227 against 2433 ns
  in this run). `Do_DistinctKeysParallel16` — sixteen goroutines per `P`, the
  shape of a cache's miss storm — stays there. It remains a shape a `Group`
  saves nothing on: no call is ever served by another, so every one pays the
  hand-off for no deduplication. That is the cache domain's miss path, and
  the reason its price is now the hand-off alone.

## What is deliberately not measured

- **Abandonment.** A caller leaving early is a correctness property (the call
  survives for the others), pinned by
  `TestAbandonedLeaderDoesNotCondemnTheFollowers`, not a throughput one.
- **Cross-process deduplication.** There is none — see `CLAUDE.md` §"One
  process, and only one".
