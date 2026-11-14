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
| Git branch         | `wip/sdk-lot-4-kernel` |
| Git commit         | `e28fa41` + #813 (pre-commit) |
| Generated (UTC)    | 2026-11-07 |
| Bench wall-clock   | `-test.benchtime=200ms`, `GOMAXPROCS=4`, 15 runs interleaved with the build before #813; medians |

## Results

| Benchmark | ns/op | B/op | allocs/op | |
|---|---|---|---|---|
| `Do_Uncontended-4` | 2 259 ± 3 % | 384 | 5 | |
| `Do_SharedKeyParallel-4` | 2 343 ± 8 % | 283 | 4 | 0.635 fn-runs/op |
| `Do_DistinctKeysParallel-4` | 1 798 ± 7 % | 390 | 5 | |
| `Do_DistinctKeysParallel16-4` | 1 925 ± 18 % | 389 | 6 | |
| `Baseline_DirectCall-4` | 0.731 ± 7 % | 0 | 0 | |

## The number that decides whether to use this

**A leading call costs ≈ 2 µs.** That is `BenchmarkDo_Uncontended`: one caller,
one key, an `fn` that returns immediately. Almost none of it is allocation
(5 allocs since #813 — the call record, its done channel,
`context.WithCancel`'s context and its cancel func, the `go` statement's
closure); it is a goroutine **park/unpark round trip**. `fn` runs on a
goroutine of its own (see `CLAUDE.md` §"Why a goroutine"), so the caller
hands off, blocks on a channel, and is woken by the scheduler.

So the rule, measured rather than asserted:

> A `Group` pays for itself when `fn` costs materially more than ~2 µs **and**
> callers actually collide.

A network round trip (10³–10⁵ µs), a disk read, an L2 cache lookup, a
cryptographic key derivation — all far above the line. A map lookup, an
arithmetic result, a small in-process computation — all far below it, and
wrapping one in a `Group` makes it three orders of magnitude slower for a
saving that does not exist.

`BenchmarkBaseline_DirectCall` (0.4 to 0.8 ns/op, one or two cycles, by
where the linker places its loop — see #813 below) is the floor and is in the
table for exactly this reason: without it, 2 µs reads as "fast".

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
- **`Do_Uncontended` kept its 6 allocations** (5 since #813, below): the
  leader allocates exactly what it did, outside the lock instead of inside.
- **`Do_SharedKeyParallel`'s `fn-runs/op` is unchanged within noise**
  (p = 0.24). Its B/op and allocs/op rise by one allocation's worth: a leader
  that loses the insert to another (a race the old lock made impossible)
  built a candidate it then discards — the price of building it unlocked.

## One acquisition, a publication off the lock (#1035, reopened)

The first pass at #1035 (above) left step 1 alone at 87 % of the base, for a
bound of 80 %: a leading call took the group's lock three times — a first
look, the insert, the publication. The owner kept the bound (2026-11-13), and
this pass takes the lock once:

- **One blocking acquisition a call.** A caller builds its candidate, the call
  record and its context, with the lock released; `insertOrJoin` inserts it or
  joins the call in flight, in one acquisition.
- **A publication that never waits for the lock.** `retired` is an atomic a
  newcomer reads as the key's absence; the map entry goes at once under
  `TryLock`, or is queued for the holder, which removes it before releasing.
- **A hot key's followers build nothing.** A shard keeps the hash of the
  latest key whose call gained a follower; a caller of that key looks first
  and joins with no allocation.

Two rows are new. **`Do_DistinctKeysParallel_OneShard`** (an internal
benchmark, `shard_bench_internal_test.go`) drives ONE shard through `join` and
`await`, with the hash `Do` computes: step 1 alone, judged against the base's
`Do_DistinctKeysParallel`. **`Do_DistinctKeysParallelOwnCtx`** gives each
goroutine its own context: every other row shares one, whose `Done` channel
each waiting call queues on and leaves under that channel's lock — a second
lock all goroutines contend on, worth 35 to 40 % of the row once the shards
are in place. It judges nothing.

**Indications only**, measured 2026-11-14 on the shared 8-vCPU AMD EPYC 7351P
VM, four CPUs pinned by `taskset`, `-cpu=4`, 200 000 calls a row, ten rounds
alternating the base (`ee1f48b`), the first pass (`6492b44`) and this one,
benchstat. The VM measures its scheduler as much as the code; the verdict is
the A/B lane's on the runner (`bench-ab.yml`), whose figures replace these.

```
                                   │   base.txt   │               main.txt               │              final.txt              │
                                   │    sec/op    │    sec/op     vs base                │   sec/op     vs base                │
Do_Uncontended-4                     2.137µ ±  3%   2.157µ ±  6%        ~ (p=0.853 n=10)   2.251µ ± 2%   +5.33% (p=0.000 n=10)
Do_SharedKeyParallel-4               2.321µ ±  8%   2.302µ ±  8%        ~ (p=0.739 n=10)   2.494µ ± 6%        ~ (p=0.052 n=10)
Do_DistinctKeysParallel-4            2.730µ ± 16%   1.957µ ± 17%  -28.32% (p=0.000 n=10)   2.002µ ± 7%  -26.68% (p=0.000 n=10)
Do_DistinctKeysParallelOwnCtx-4      2.637µ ±  7%   1.204µ ± 21%  -54.36% (p=0.000 n=10)   1.247µ ± 7%  -52.71% (p=0.000 n=10)
Do_DistinctKeysParallel_OneShard-4                                                         2.245µ ± 7%

                                   │  base.txt   │              main.txt               │              final.txt              │
                                   │  allocs/op  │  allocs/op   vs base                │  allocs/op   vs base                │
Do_Uncontended-4                     6.000 ±  0%   5.000 ±  0%  -16.67% (p=0.000 n=10)   5.000 ±  0%  -16.67% (p=0.000 n=10)
Do_SharedKeyParallel-4               5.000 ± 20%   4.000 ± 25%  -20.00% (p=0.002 n=10)   4.000 ± 25%  -20.00% (p=0.000 n=10)
Do_DistinctKeysParallel-4            7.000 ±  0%   5.000 ±  0%  -28.57% (p=0.000 n=10)   5.000 ±  0%  -28.57% (p=0.000 n=10)

                       │   base.txt   │            main.txt            │              final.txt              │
                       │  fn-runs/op  │  fn-runs/op   vs base          │  fn-runs/op   vs base               │
Do_SharedKeyParallel-4   777.4m ± 23%   725.1m ± 21%  ~ (p=0.353 n=10)   701.8m ± 12%  -9.72% (p=0.035 n=10)
```

- **Step 1 alone**, `Do_DistinctKeysParallel_OneShard` over the base's
  `Do_DistinctKeysParallel`, read 0.70 and 0.82 in the two runs of this
  design that day (0.82 in the one above): the bound is 0.80, so the VM
  cannot say; the runner will.
- **Step 2**, `Do_DistinctKeysParallel` 2.00 µs against `Do_Uncontended`'s
  2.25 µs, holds here as it did.
- **`Do_Uncontended`** keeps its 5 allocations (the budget's), and reads
  +5 % in every run that day, about 100 ns of a 2.2 µs hand-off that a CPU
  profile of the row, dominated by the scheduler, does not attribute: to
  confirm on the runner.
- **`fn-runs/op` without the hot-key hint rose 7 to 14 %** in every run
  (0.76 against 0.67 to 0.70): each follower built and discarded a candidate
  before reaching the lock, and arrived after the call it would have joined.
  With the hint it is the base's within noise.

## The shared context's root in the call record (#813)

Measured 2026-11-07, go1.27.3, branch `wip/sdk-lot-4-kernel`, 8-vCPU AMD EPYC
7351P VM (shared), `GOMAXPROCS=4`: two test binaries — the code before #813
and after — run alternately fifteen times, `-test.benchtime=200ms`,
benchstat.

```
                            │  before.txt   │               after.txt               │
                            │    sec/op     │    sec/op      vs base                │
Do_Uncontended-4               2.279µ ±  3%    2.259µ ±  3%        ~ (p=0.116 n=15)
Do_SharedKeyParallel-4         2.303µ ±  9%    2.343µ ±  8%        ~ (p=0.783 n=15)
Do_DistinctKeysParallel-4      1.752µ ±  4%    1.798µ ±  7%        ~ (p=0.177 n=15)
Baseline_DirectCall-4         0.3722n ± 10%   0.7310n ±  7%  +96.40% (p=0.000 n=15)
Do_DistinctKeysParallel16-4    1.809µ ±  7%    1.925µ ± 18%        ~ (p=0.340 n=15)

                            │  before.txt  │              after.txt              │
                            │     B/op     │    B/op     vs base                 │
Do_Uncontended-4              384.0 ± 0%     384.0 ± 0%       ~ (p=1.000 n=15)
Do_SharedKeyParallel-4        283.0 ± 1%     283.0 ± 4%       ~ (p=0.292 n=15)
Do_DistinctKeysParallel-4     389.0 ± 0%     390.0 ± 0%       ~ (p=0.128 n=15)
Do_DistinctKeysParallel16-4   389.0 ± 0%     389.0 ± 0%       ~ (p=1.000 n=15)

                            │  before.txt  │              after.txt               │
                            │  allocs/op   │ allocs/op   vs base                  │
Do_Uncontended-4              6.000 ± 0%     5.000 ± 0%  -16.67% (p=0.000 n=15)
Do_SharedKeyParallel-4        5.000 ± 0%     4.000 ± 0%  -20.00% (p=0.000 n=15)
Do_DistinctKeysParallel-4     6.000 ± 0%     5.000 ± 0%  -16.67% (p=0.000 n=15)
Do_DistinctKeysParallel16-4   7.000 ± 0%     6.000 ± 0%  -14.29% (p=0.000 n=15)

                       │ before.txt  │              after.txt               │
                       │ fn-runs/op  │ fn-runs/op   vs base               │
Do_SharedKeyParallel-4   629.4m ± 1%   634.7m ± 5%  +0.84% (p=0.024 n=15)
```

- **One allocation fewer on every leading call.** The shared context was
  `context.WithCancel(context.WithoutCancel(leader))`: two contexts, and
  `WithoutCancel`'s 16-byte wrapper was an allocation of its own. Its root
  now lives in the call record — the leader's values, no deadline, no
  cancellation, the same name — and `WithCancel` is built over it. A memory
  profile of `Do_Uncontended` names what is left: three objects in `join`
  (the record, its done channel, the `go` statement's closure) and two in
  `context.WithCancel` (its context, its cancel func); `WithoutCancel`'s
  line is gone. The budget in `design/sdk.yaml` holds the five
  (`TestPerfAllocsDoLeading`, race off); the code before #813 fails it with
  180 000 allocations for its 30 000 calls.
- **B/op does not move.** The record grows from 80 to 96 bytes — the next
  size class — by the 16 the wrapper took.
- **Time does not move**, within noise on every row: a 16-byte allocation is
  a few nanoseconds of a 2 µs hand-off.
- **`Baseline_DirectCall` +96 % is the loop's placement, not the change.** It
  calls nothing in this package; in the after binary its nine-byte loop
  straddles a 64-byte line (`0x552ebc`–`0x552ec4`), in the before binary it
  does not (`0x552d1c`–`0x552d24`): two cycles an iteration against one. The
  2026-11-02 run, before #813, read 0.76 ns.
- **`fn-runs/op` +0.8 %** is the scheduler's share of collisions: the path a
  follower takes (`joinExisting`) did not change.
- **The output is the same.** `identity_internal_test.go` compares what fn's
  context and its children show — values, deadline, `Done`, `Err`, `Cause`,
  name, an `AfterFunc` — with the construction before #813 at every point:
  over eight shapes of leader context, a lone caller cancelled or past its
  deadline, the first and then the last of two callers leaving. It checks
  Do's values, shared flags, errors and panics too, and passes on the code
  before #813 as after.

## How to read the rest

- **`BenchmarkDo_SharedKeyParallel`** — every `P` goroutine asks for the same
  key. The custom metric is the point: **0.43 to 0.65 executions of `fn` per
  call** across runs, so a third to a half of the calls were served by someone
  else's work. With a real (slow) `fn` that ratio is the saving; here it merely
  proves deduplication happens under contention. Allocations fall below the
  leader's 5/op for the same reason — a follower of a hot key allocates
  nothing at all, so the average falls as the shared fraction rises (3 to
  4/op since #813: a caller that loses the insert to another also built a
  candidate it discards, see above). The
  ratio is workload-dependent: it rises with `fn`'s duration (a longer call
  collects more followers) and falls as `fn` approaches zero.
- **`BenchmarkDo_DistinctKeysParallel`** — nothing is ever shared, so every
  call pays the full leading price. Until #1035 it also contended on the one
  group mutex (3224 ns/op against 2007 uncontended in the 2026-09-09 run);
  since then a key locks one of sixteen shards, for a map lookup or insert
  only, and the row reads no more than `Do_Uncontended` (2227 against 2433 ns
  on 2026-11-02, 1798 against 2259 in the #813 run). `Do_DistinctKeysParallel16` — sixteen goroutines per `P`, the
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
