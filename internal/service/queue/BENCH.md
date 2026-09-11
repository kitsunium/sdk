<!-- generated from internal/service/queue/queue_bench_test.go — run `cd internal/service && GOWORK=off go test -run=NONE -bench=. -benchmem -benchtime=1s ./queue/` to refresh -->
# Benchmarks — `internal/service/queue`

Two brokers, five verbs, three payload sizes and three backlog depths. These
benchmarks exist to answer one question honestly, because a caller who guesses
its answer sizes their system wrong by three orders of magnitude:

> **What does durability actually cost, and what is it that costs?**

## Reproducibility envelope

> **Numbers vary across machines.** This report stamps the box that produced
> them so cross-machine deltas can be evaluated honestly. The `file` figures in
> particular are a property of *this* device — a VM on overlayfs — and a
> machine with a battery-backed write cache will report a different `Publish`
> entirely. The *shape* of the result is what travels, not the milliseconds.

| Dimension | Value |
|---|---|
| CPU cores          | 8 (AMD EPYC 7351P 16-Core Processor) |
| RAM                | 15 GiB |
| OS / kernel        | Linux 6.12.101+deb13-amd64 |
| Architecture       | amd64 |
| Go toolchain       | go1.27.1 linux/amd64 |
| Git branch         | `agent-ae0099f209a4afcb2` |
| Git commit         | `7ef449f` (pre-commit) |
| Generated (UTC)    | 2026-09-10 |
| Bench wall-clock   | `-test.benchtime=1s`, single run, 391 s total |

## Results

```
BenchmarkPublish/mem/64-8                   2066310       561.0 ns/op    114.08 MB/s      291 B/op     5 allocs/op
BenchmarkPublish/mem/4096-8                  514070      6199   ns/op    660.75 MB/s     4323 B/op     5 allocs/op
BenchmarkPublish/mem/65536-8                 116396    118009   ns/op    555.35 MB/s    65755 B/op     4 allocs/op
BenchmarkPublish/file/64-8                      350   3379989   ns/op      0.02 MB/s     1984 B/op    41 allocs/op
BenchmarkPublish/file/4096-8                    363   3595272   ns/op      1.14 MB/s     2000 B/op    41 allocs/op
BenchmarkPublish/file/65536-8                   301   3549209   ns/op     18.46 MB/s     2000 B/op    41 allocs/op

BenchmarkReceiveBatch/mem/batch1-8           755736      1826   ns/op                     447 B/op     4 allocs/op
BenchmarkReceiveBatch/mem/batch16-8           63326     17773   ns/op                    8986 B/op    53 allocs/op
BenchmarkReceiveBatch/file/batch1-8            1399    873113   ns/op                  120822 B/op  1107 allocs/op
BenchmarkReceiveBatch/file/batch16-8            534   2161703   ns/op                  189262 B/op  2026 allocs/op

BenchmarkAck/mem-8                          3222615       494.9 ns/op                       0 B/op     0 allocs/op
BenchmarkAck/file-8                           34621     33579   ns/op                     960 B/op    14 allocs/op

BenchmarkNack/mem-8                         2534286       515.9 ns/op                      41 B/op     0 allocs/op
BenchmarkNack/file-8                          31611     40133   ns/op                    1712 B/op    27 allocs/op

BenchmarkRoundTrip/mem-8                     690416      1766   ns/op                     660 B/op    10 allocs/op
BenchmarkRoundTrip/file-8                       313   3293139   ns/op                    6619 B/op   107 allocs/op

BenchmarkReceiveBacklog/mem/depth10-8        618663      1827   ns/op                     442 B/op     4 allocs/op
BenchmarkReceiveBacklog/mem/depth100-8       802382      1848   ns/op                     432 B/op     4 allocs/op
BenchmarkReceiveBacklog/mem/depth1000-8      919480      1682   ns/op                     461 B/op     4 allocs/op
BenchmarkReceiveBacklog/file/depth10-8        10000    121734   ns/op                    9571 B/op   119 allocs/op
BenchmarkReceiveBacklog/file/depth100-8        2978    391974   ns/op                   51024 B/op   482 allocs/op
BenchmarkReceiveBacklog/file/depth1000-8        405   3029735   ns/op                  456311 B/op  4085 allocs/op
```

## What the numbers say

### Durability costs 1 865× on a round trip, and that IS the domain

The headline, and the only number a caller sizing a system needs to start from:

| verb | `mem` | `file` | ratio |
|---|---|---|---|
| `Publish` (64 B) | 561 ns | 3 379 989 ns | **6 025×** |
| `Ack`            | 495 ns |    33 579 ns |    **68×** |
| `Nack`           | 516 ns |    40 133 ns |    **78×** |
| **`RoundTrip`**  | **1 766 ns** | **3 293 139 ns** | **1 865×** |

Publish is where almost all of it lives: **3.3 ms of a 3.3 ms round trip is the
enqueue**, and the enqueue is two `fsync(2)` calls. That is the guarantee being
paid for, not overhead around it — the promise `Publish` makes is that a power
cut one instruction after it returns does not lose the message, and there is no
way to make that promise without going to the device.

The practical consequence, stated because it is what people get wrong: **on
this device one durable producer tops out at roughly 300 messages per second,
and no amount of code changes that.** More throughput comes from more producers
(the flushes are independent), or from a broker over a system that batches its
`fsync` across publishers — which is a connector, not this package.

### The cost of a durable publish is not the payload

| payload | `Publish` (file) | vs 64 B |
|---|---|---|
| 64 B   | 3 379 989 ns | — |
| 4 KiB  | 3 595 272 ns | +6.4 % |
| 64 KiB | 3 549 209 ns | +5.0 % |

**A 1 024× larger payload costs 5 % more, and 4 KiB costs MORE than 64 KiB** —
which is the giveaway: the spread across three sizes is run-to-run variance,
not a size effect. The cost is the flush of the file and the flush of the
directory, and neither shrinks because the message did.

This independently reproduces `internal/service/vfs/BENCH.md`'s finding on the
same device, which is the expected result — this package's `Publish` IS
`vfs.WriteAtomic` plus a name.

So: **batching helps and shrinking does not.** A producer with a hundred small
messages pays a hundred round trips to the device, and the only thing that
changes that is asking whether all hundred genuinely need to be individually
crash-consistent.

### The durable broker's cost scales with the BACKLOG, and that is its limit

This is the number that decides whether the file broker is the right tool:

| depth | `Receive` (file) | vs the row above | `Receive` (mem) |
|---|---|---|---|
| 10    |   121 734 ns | — | 1 827 ns |
| 100   |   391 974 ns | **3.2×** for 10× the depth | 1 848 ns |
| 1 000 | 3 029 735 ns | **7.7×** for a further 10× | 1 682 ns |

A `Receive` reads and sorts the whole queued directory, so a deep backlog costs
more per lease than a shallow one — roughly linearly once the directory stops
fitting comfortably in the cache. **The memory broker is flat** (1 827 / 1 848 /
1 682 ns across a 100× range, which is noise), because its ready list is ordered
at insertion and its lease deadlines live in a `kernel/heap` min-heap.

Read plainly: **this is a durable queue for a backlog of hundreds to low
thousands.** At 1 000 queued messages a lease already costs as much as a
publish. A caller with a persistent backlog of a million wants a broker over a
system built for that, behind this same `core/queue.Broker` port.

### Batching amortises the scan, and by roughly as much as you would expect

| batch | `Receive` (file, backlog 256) | per message |
|---|---|---|
| 1  |   873 113 ns | 873 113 ns |
| 16 | 2 161 703 ns | **135 106 ns** |

**6.5× cheaper per message at a batch of 16**, because one directory read and
one sort serve sixteen leases instead of one. On the memory broker the same
change is 1 826 → 1 111 ns per message (1.6×), which is just the per-call
overhead being shared — there is no scan to amortise.

Both figures INCLUDE the `Nack` that returns each message to the backlog, which
is inside the timed region deliberately: `b.StopTimer`/`StartTimer` cost more
per iteration than a whole in-memory `Receive`, so excluding it would have made
the memory column a measurement of the instrumentation. Subtract `BenchmarkNack`
(516 ns / 40.1 µs) to read the lease alone.

The trade-off `ConsumerConfig.BatchSize` documents is the other half: a batch is
leased all at once and processed one at a time, so the last message of a batch
of 16 has already spent fifteen handlers' worth of its visibility timeout.

### Two things were measured, found wanting, and fixed

Both were found by reading these numbers, and both are recorded here rather
than quietly corrected, because the numbers before and after are the argument.

**1. The memory broker's `Publish` allocated the payload twice.** It cloned into
its own record — which it must, so a producer reusing a buffer cannot rewrite an
accepted message — and then cloned AGAIN for the `MessageValue` it returned.
`go tool pprof -sample_index=alloc_space` was unambiguous: `slices.Clone`
accounted for **99.78 %** of the bytes on the path. The returned payload is now
the caller's own slice, which the caller already holds:

| payload | before | after |
|---|---|---|
| 64 B   |     358 B/op, 6 allocs |     291 B/op, 5 allocs |
| 4 KiB  |   8 405 B/op, 5 allocs |   4 323 B/op, 5 allocs |
| 64 KiB | 131 284 B/op, 5 allocs |  65 755 B/op, 4 allocs |

At 64 KiB that is **half the allocation and 23 % less time** (69.4 µs → 53.3 µs
at the benchtime the two were compared at).

**2. The memory broker's `Receive` was O(in-flight).** `reclaimExpired` ranged
over the whole in-flight MAP looking for lapsed leases — and a map has no order,
so there was nothing to stop early on. Against a growing in-flight set a lease
measured **105 µs**, for a verb that is a few hundred nanoseconds of actual
work. The file broker never had the defect, because its in-flight directory
sorts by deadline and its scan breaks at the first lease still held.

The fix is `internal/kernel/heap` — the primitive was already in the SDK —
keyed on the lease deadline, with lazy deletion so an `Ack` stays O(1) and a
stale entry is discarded when it is popped. **105 µs → 1.83 µs**, and the
backlog column above is flat as a result.

### Allocations, including the ones deliberately left alone

- **`Ack/mem` is 0 B and 0 allocs.** A map delete and nothing else.
- **`Nack/mem` reports 41 B and 0 allocs**, which is the amortised growth of the
  ready slice when a retried message is inserted back into it.
- **`Publish/file` is ~2 000 B and 41 allocs, at every payload size.** The count
  is flat because it is all name-building and syscall plumbing — entropy, hex,
  `path.Join`, the temporary's name, the directory handle for the final flush —
  and none of it touches the payload. **This is deliberately not optimised.**
  Two kilobytes of garbage sits next to 3 milliseconds of `fsync`; removing every
  allocation on the path would improve the verb by under one part in a thousand,
  and each one removed would be a step away from the straightforward code that
  makes the failure paths auditable.
- **`Receive/file` allocates ~450 B per queued message scanned** (456 311 B at a
  backlog of 1 000). That is `fs.ReadDir` building a `DirEntry` and a name for
  every entry in the directory, plus the sort. It is the same fact as the
  scaling limit above, in bytes rather than in nanoseconds, and the answer to it
  is a different broker rather than a cleverer scan.
- **`Publish/mem` at 64 KiB is a GC benchmark, not a queue benchmark.** At
  `-benchtime=1s` it runs 116 k iterations and allocates ~7.6 GB, so the
  reported 118 µs is dominated by collection pressure — the same case measured
  25 µs at `-benchtime=300ms`. That instability is itself the finding: **an
  in-memory queue holding 64 KiB messages is a garbage-collection problem**,
  which is one more reason `NewMemory` is documented as a test double rather
  than as a lightweight production option.
