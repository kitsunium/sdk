<!-- generated from internal/service/data/queue/queue_bench_test.go and siblings_bench_test.go — run `cd internal && GOWORK=off go test -run=NONE -bench=. -benchmem -benchtime=1s ./service/data/queue/` to refresh (BenchmarkAckMany with -benchtime=20x: each iteration leases a hundred messages off the clock) -->
# Benchmarks — `internal/service/data/queue`

Two brokers, every verb, three payload sizes and three backlog depths — and
the SQL broker's own allocations per verb over the fake engine. These
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
| Go toolchain       | go1.27.3 linux/amd64, `GOMAXPROCS=4` (the `-4` suffix) |
| Git branch         | `wip/sdk-lot-6-queue` |
| Git commit         | `acf366a83` on `ee1f48ba2` (issue #1048) |
| Generated (UTC)    | 2026-11-02 |
| Bench wall-clock   | `-test.benchtime=1s`, single run each; about 15 min in all |

## Results

The verbs ADR 0054 shipped with:

```
BenchmarkPublish/mem/64-4 1534201 732.0 ns/op 87.43 MB/s 397 B/op 4 allocs/op
BenchmarkPublish/mem/4096-4 553578 9184 ns/op 445.98 MB/s 4425 B/op 4 allocs/op
BenchmarkPublish/mem/65536-4 106510 117756 ns/op 556.54 MB/s 65866 B/op 4 allocs/op
BenchmarkPublish/file/64-4 379 3136863 ns/op 0.02 MB/s 2128 B/op 42 allocs/op
BenchmarkPublish/file/4096-4 390 3173393 ns/op 1.29 MB/s 2128 B/op 42 allocs/op
BenchmarkPublish/file/65536-4 367 3314376 ns/op 19.77 MB/s 2144 B/op 42 allocs/op
BenchmarkReceiveBatch/mem/batch1-4 701341 1922 ns/op 577 B/op 5 allocs/op
BenchmarkReceiveBatch/mem/batch16-4 54006 19748 ns/op 10563 B/op 69 allocs/op
BenchmarkReceiveBatch/file/batch1-4 1479 875000 ns/op 121918 B/op 1128 allocs/op
BenchmarkReceiveBatch/file/batch16-4 510 2493772 ns/op 205361 B/op 2332 allocs/op
BenchmarkAck/mem-4 2738967 511.3 ns/op 0 B/op 0 allocs/op
BenchmarkAck/file-4 33962 45539 ns/op 960 B/op 15 allocs/op
BenchmarkRoundTrip/mem-4 618925 1941 ns/op 786 B/op 9 allocs/op
BenchmarkRoundTrip/file-4 344 3353523 ns/op 7226 B/op 118 allocs/op
BenchmarkNack/mem-4 1801660 795.2 ns/op 147 B/op 1 allocs/op
BenchmarkNack/file-4 23797 55334 ns/op 2288 B/op 37 allocs/op
BenchmarkReceiveBacklog/mem/depth10-4 520246 2267 ns/op 542 B/op 5 allocs/op
BenchmarkReceiveBacklog/mem/depth100-4 700647 2396 ns/op 576 B/op 5 allocs/op
BenchmarkReceiveBacklog/mem/depth1000-4 660906 2217 ns/op 546 B/op 5 allocs/op
BenchmarkReceiveBacklog/file/depth10-4 6027 209829 ns/op 10660 B/op 140 allocs/op
BenchmarkReceiveBacklog/file/depth100-4 2808 470870 ns/op 52145 B/op 503 allocs/op
BenchmarkReceiveBacklog/file/depth1000-4 363 3966697 ns/op 457758 B/op 4106 allocs/op
```

Since issue #1048 — `Consume` round trips, the lease verbs since ADR 0151 and
kitsunium/sdk#474, ADR 0183's publications, the listings, and the siblings of
the 2026-11-02 amendment — on the memory broker:

```
BenchmarkConsume/mem/plain-4 107527 13500 ns/op 2009 B/op 28 allocs/op
BenchmarkConsume/mem/autoextend-4 87909 15011 ns/op 2250 B/op 31 allocs/op
BenchmarkConsume/mem/drain-4 84817 14023 ns/op 2010 B/op 28 allocs/op
BenchmarkLeaseVerbs/mem/extend-4 991210 1074 ns/op 238 B/op 2 allocs/op
BenchmarkLeaseVerbs/mem/release-4 1746222 812.5 ns/op 151 B/op 1 allocs/op
BenchmarkLeaseVerbs/mem/reject-4 807831 2183 ns/op 987 B/op 1 allocs/op
BenchmarkPublishLater/mem/at-4 1634308 739.2 ns/op 395 B/op 4 allocs/op
BenchmarkPublishLater/mem/once-fresh-4 718324 1608 ns/op 779 B/op 5 allocs/op
BenchmarkPublishLater/mem/once-duplicate-4 7421754 160.3 ns/op 0 B/op 0 allocs/op
BenchmarkListings/mem/messages-page100-4 26551 44486 ns/op 55968 B/op 116 allocs/op
BenchmarkListings/mem/allmessages-first100-4 13602 75773 ns/op 98832 B/op 277 allocs/op
BenchmarkListings/mem/deadletters-page100-4 79650 15737 ns/op 22784 B/op 101 allocs/op
BenchmarkListings/mem/stats-depth1000-4 249751 5601 ns/op 0 B/op 0 allocs/op
BenchmarkPublishMany/mem/1-4 1410032 803.9 ns/op 803.9 ns/msg 455 B/op 5 allocs/op
BenchmarkPublishMany/mem/16-4 124022 8658 ns/op 541.2 ns/msg 5815 B/op 50 allocs/op
BenchmarkPublishMany/mem/256-4 9249 137129 ns/op 535.7 ns/msg 91932 B/op 770 allocs/op
BenchmarkAckMany/mem/100-4 55996 20934 ns/op 209.3 ns/msg 112 B/op 1 allocs/op
```

and on the file broker:

```
BenchmarkConsume/file/plain-4 358 3239922 ns/op 10609 B/op 170 allocs/op
BenchmarkConsume/file/autoextend-4 372 3366043 ns/op 10859 B/op 173 allocs/op
BenchmarkConsume/file/drain-4 352 3395685 ns/op 10610 B/op 170 allocs/op
BenchmarkLeaseVerbs/file/extend-4 27010 44229 ns/op 1808 B/op 29 allocs/op
BenchmarkLeaseVerbs/file/release-4 22144 58819 ns/op 2288 B/op 37 allocs/op
BenchmarkLeaseVerbs/file/reject-4 380 3228067 ns/op 5512 B/op 79 allocs/op
BenchmarkPublishLater/file/at-4 368 3135548 ns/op 2164 B/op 42 allocs/op
BenchmarkPublishLater/file/once-fresh-4 366 3271518 ns/op 4079 B/op 79 allocs/op
BenchmarkPublishLater/file/once-duplicate-4 84588 13064 ns/op 1485 B/op 14 allocs/op
BenchmarkListings/file/messages-page100-4 258 4834515 ns/op 933156 B/op 9142 allocs/op
BenchmarkListings/file/allmessages-first100-4 243 4827802 ns/op 901062 B/op 9137 allocs/op
BenchmarkListings/file/deadletters-page100-4 486 2499191 ns/op 366762 B/op 3323 allocs/op
BenchmarkListings/file/stats-depth1000-4 302 3636996 ns/op 516118 B/op 6038 allocs/op
BenchmarkPublishMany/file/1-4 363 3062187 ns/op 3062180 ns/msg 2212 B/op 43 allocs/op
BenchmarkPublishMany/file/16-4 22 49321358 ns/op 3082577 ns/msg 34170 B/op 662 allocs/op
BenchmarkPublishMany/file/256-4 2 1001783012 ns/op 3913210 ns/msg 553836 B/op 10669 allocs/op
BenchmarkAckMany/file/100-4 20 3798912 ns/op 37988 ns/msg 96112 B/op 1501 allocs/op
```

The SQL broker on PostgreSQL's statements over the fake engine (issue #1048):
the allocations and the statement count are the broker's; the nanoseconds are
the fake's, which copies its table per statement — every case empties it off
the clock. The real engines' figures belong in `e2e/integration/sql`, which this
machine cannot run.

```
BenchmarkSQL/postgres/publish-4 102226 11375 ns/op 1409 B/op 21 allocs/op
BenchmarkSQL/postgres/roundtrip-4 36290 32776 ns/op 6627 B/op 95 allocs/op
BenchmarkSQL/postgres/publishmany16-4 17556 69854 ns/op 31802 B/op 559 allocs/op
BenchmarkSQL/postgres/stats-4 204385 5985 ns/op 508 B/op 8 allocs/op
```

## The `Consume` round trip is a design budget (issue #1048)

`BenchmarkConsume/mem/plain`'s 28 allocations are a contract since: the budget
`allocs: 28` on `queue.Consume` in `design/sdk.yaml` (ADR 0165), held race off
by `TestPerfAllocsConsumeRoundTrip` — the `gen_perf_test.go` kit writes over
`perf_fixtures_test.go` — as a total over 30 000 round trips after as many
warm-up ones: 840 000 on 2026-11-03, five runs alike. A change that adds one
allocation to the engine's happy path fails there, in the alloc lane, before
it reaches this table. The real engines' rows for the SQL broker are in
`e2e/integration/sql/BENCH.md`.

## Since issue #811: a listing reused while its directory is quiet

The file broker keeps each state directory's last listing and reuses it while
the directory's mtime has not moved and was already older than the listing by
three seconds — git's "racily clean" rule —, ten seconds at most
(`file_listing.go`); a listing is a `Readdirnames` and a sort, no `DirEntry`
per entry, and a name is split and its count checked without allocating.
Measured 2026-11-03 on the same box, `-benchtime=300ms -count=4`, before
(`3cd16492a`) → after, benchstat p = 0.029 on every row with a delta:

| Benchmark | before | after |
|---|---|---|
| `IdlePoll/file/depth0` | 15.3 µs, 481 B, 12 allocs | 4.0 µs (−73 %), 464 B, 6 allocs |
| `IdlePoll/file/depth1000` | 2 938 µs, 452 011 B, 4 026 allocs | **4.7 µs (−99.8 %)**, 465 B, 6 allocs |
| `IdlePoll/mem/depth0` / `depth1000` | — | 125 ns, 0 allocs (the control) |
| `Listings/file/stats-depth1000-quiet` | 3 454 µs, 504 KiB, 6 038 allocs | **453 µs (−87 %)**, 842 B, 10 allocs |
| `Listings/file/stats-depth1000-listed` | 3 498 µs, 504 KiB, 6 038 allocs | 1 336 µs (−62 %), 105 KiB, 1 050 allocs |
| `ReceiveBacklog/file/depth10` | 148 µs, 140 allocs | 140 µs (~, p = 0.057), 112 allocs |
| `ReceiveBacklog/file/depth100` | 398 µs, 503 allocs | 241 µs (−39 %), 205 allocs |
| `ReceiveBacklog/file/depth1000` | 3 115 µs, 446.7 KiB, 4 106 allocs | 1 011 µs (−68 %), 103 KiB, 1 108 allocs |
| `ReceiveBatch/file/batch1` | 892 µs, 1 128 allocs | 365 µs (−59 %), 362 allocs |
| `RoundTrip/file` | 3.29 ms, 126 allocs | 3.26 ms (~), 127 allocs |

- **An idle poll no longer scales with the backlog.** A queue holding a
  thousand messages an hour from visible, untouched for longer than the
  margin, costs one stat per state directory a poll — the same 4–5 µs as an
  empty one — where it read and sorted the thousand names on every poll.
  That is the poll every idle worker of a `Consume` makes each
  `PollInterval`.
- **`Stats` on a quiet queue reads no directory** (issue #833's reserve):
  10 allocations, the payloads never read; the 453 µs left is parsing the
  names. On a queue being written to it still lists the three directories
  (`-listed`, each touched off the clock before every call).
- **A busy queue is still read on every `Receive`**, and that is the
  decision rather than a gap: any change moves the directory's mtime — this
  consumer's own lease and nack included —, so `ReceiveBacklog`, which
  leases and hands back a message per iteration, relists every time. What it
  gained (−68 % at depth 1 000) is the cheaper listing — names alone, no
  `DirEntry` — and the allocation-free name grammar. depth 1 000 costs 7× depth
  10 rather than 21×; issue #811's "at most 2×" would need the cache reused
  after the consumer's own rename, which the rule forbids: a rename by another
  process in the same tick would then be missed.
- `RoundTrip/file` is the publication's two flushes, unchanged.

## Since issue #827: a batch shares the directory flush

The file broker's `PublishMany` hands its batch to vfs's
`AtomicBatchWriter.WriteAtomicAll`: each message still its own file flush
before its rename, `ready/` flushed once after the last. Measured 2026-11-03,
`-benchtime=1s -count=3`, 64-byte payloads:

| Benchmark | per message, before | per message, after |
|---|---|---|
| `Publish/file/64` | 3.14 ms | 3.13–3.51 ms (unchanged: one message, two flushes) |
| `PublishMany/file/1` | 3.06 ms | 3.07–3.55 ms |
| `PublishMany/file/16` | 3.08 ms | **1.88–1.96 ms** (≈ 59 % of `Publish`) |
| `PublishMany/file/256` | 3.91 ms | **1.79–1.87 ms** |

Issue #827 asked for at most 55 % of `Publish` at sixteen; this is 59–62 %
on this VM. What is left is the sixteen file flushes, which each message
needs before its rename; sharing them would mean flushing the temporaries
concurrently, which vfs does not do (ADR 0056, amended 2026-11-03).
`Publish/file` reads 50 allocations where the table above reads 42: the
eight of #803's commit group.

## What the rows added for issue #1048 say

- **`Consume` costs ~14 µs and ~28 allocations per message over the memory
  broker's ~2 µs `RoundTrip`**: the handler's goroutine hand-off, the watch
  (kitsunium/sdk#474) — a goroutine, a timer and two derived contexts per
  message — and the wake. `AutoExtend` adds the renewal ticker (+3 allocs);
  `DrainTimeout` costs nothing until a stop. On the file broker the 3.2 ms
  is the publication's flush, as `RoundTrip`'s.
- **`Reject` on file is a publication** (3.2 ms): the dead-letter record is
  written atomically and durably, as a message is. `Extend` and `Release` are
  one rename (44–59 µs).
- **A duplicate `PublishOnce` is cheap on both** (160 ns, 13 µs): the key's
  reservation is read and nothing is written. A fresh one on file is a
  publication plus an exclusive link.
- **`Stats` reads no payload**: 0 B on the memory broker; on file it lists
  the three directories, so it allocated per entry (`fs.ReadDir`), as
  `Receive` did — issue #811 removed that for a quiet queue (above).
- **`PublishMany` amortises on memory and SQL** — per message, a batch of 16
  costs 541 ns against `Publish`'s 732 ns on memory, and one INSERT on SQL —
  **and on file since issue #827** (below): the batch shares `ready/`'s
  directory flush through vfs's `WriteAtomicAll`.
- **`AckMany` of 100** costs 209 ns a message on memory (one lock) and 38 µs
  on file, an unlink each.
- **On SQL a `Publish` is 21 allocations and a `PublishMany` of 16 is 559**
  (35 a message: the arguments of a multi-row INSERT), a full round trip 95.

## What the numbers say

### Durability costs 1 728× on a round trip, and that IS the domain

The headline, and the only number a caller sizing a system needs to start from:

| verb | `mem` | `file` | ratio |
|---|---|---|---|
| `Publish` (64 B) | 732 ns | 3 136 863 ns | **4 285×** |
| `Ack`            | 511 ns |    45 539 ns |    **89×** |
| `Nack`           | 795 ns |    55 334 ns |    **70×** |
| **`RoundTrip`**  | **1 941 ns** | **3 353 523 ns** | **1 728×** |

(Measured on 2026-11-02; the report of 2026-09-10 read 1 865× on the same box,
a spread the device's flush latency accounts for.)

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
| 64 B   | 3 136 863 ns | — |
| 4 KiB  | 3 173 393 ns | +1.2 % |
| 64 KiB | 3 314 376 ns | +5.7 % |

**A 1 024× larger payload costs under 6 % more** — and the report of
2026-09-10 had 4 KiB costing MORE than 64 KiB, which is the giveaway: the
spread across three sizes is run-to-run variance, not a size effect. The cost is the flush of the file and the flush of the
directory, and neither shrinks because the message did.

This independently reproduces `internal/service/data/vfs/BENCH.md`'s finding on the
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
| 10    |   209 829 ns | — | 2 267 ns |
| 100   |   470 870 ns | **2.2×** for 10× the depth | 2 396 ns |
| 1 000 | 3 966 697 ns | **8.4×** for a further 10× | 2 217 ns |

A `Receive` reads and sorts the whole queued directory, so a deep backlog costs
more per lease than a shallow one — roughly linearly once the directory stops
fitting comfortably in the cache. Since issue #811 a directory nobody changed
is not read again, so an idle poll is flat; a backlog being consumed changes
its directory at every lease and is read every time (the section above). **The memory broker is flat** (2 267 / 2 396 / 2 217 ns across
a 100× range, which is noise), because its ready list is ordered
at insertion and its lease deadlines live in a `kernel/collections/heap` min-heap.

Read plainly: **this is a durable queue for a backlog of hundreds to low
thousands.** At 1 000 queued messages a lease already costs as much as a
publish. A caller with a persistent backlog of a million wants a broker over a
system built for that, behind this same `core/data/queue.Broker` port.

### Batching amortises the scan, and by roughly as much as you would expect

| batch | `Receive` (file, backlog 256) | per message |
|---|---|---|
| 1  |   875 000 ns | 875 000 ns |
| 16 | 2 493 772 ns | **155 861 ns** |

**5.6× cheaper per message at a batch of 16**, because one directory read and
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

The fix is `internal/kernel/collections/heap` — the primitive was already in the SDK —
keyed on the lease deadline, with lazy deletion so an `Ack` stays O(1) and a
stale entry is discarded when it is popped. **105 µs → 1.83 µs**, and the
backlog column above is flat as a result.

**3. The heap is indexed since #1036.** Lazy deletion left one entry behind per
`Extend` and per acknowledgement until its deadline surfaced, so a lease
extended N times held N + 1 entries. The broker now keeps its expiries in
`heap.IndexedHeap`: each record holds the `Handle` of its lease's entry,
`Extend` re-keys it in place with `Update`, and `Ack`, `Nack`, `Reject`,
`Release`, `AckMany` and `Purge` remove it, so the heap holds exactly the
leases in flight. Measured 2026-11-02 on the 4-CPU lane of the 8-core AMD EPYC
7351P VM, go1.27.3, `-benchtime=200ms -count=4`, before → after (benchstat,
p = 0.029 where a delta is given):

| Benchmark | before | after |
|---|---|---|
| `RoundTrip/mem` | 2.217 µs, 771 B, 9 allocs | 1.654 µs (−25 %), 579 B, 8 allocs |
| `ReceiveBacklog/mem/depth1000` | 2.059 µs, 580 B, 5 allocs | 1.588 µs (−23 %), 338 B, 5 allocs |
| `LeaseVerbs/mem/extend` | 1.141 µs, 243 B, 2 allocs | 1.377 µs (~, p = 0.11), 28 B, 2 allocs |
| `LeaseVerbs/mem/release` | 624 ns, 153 B, 1 alloc | 754 ns (+21 %), 171 B, 1 alloc |
| `Ack/mem` | 392 ns, 0 B, 0 allocs | 419 ns (~, p = 0.11), 22 B, 0 allocs |

An acknowledgement pays an O(log n) removal where it paid nothing, and the
`Release` row shows it; every `Receive` stops paying for the entries nobody
removed, which is the larger number. `Ack/mem`'s 22 B are the amortised growth
of the heap's position table, primed here with b.N leases at once.

### Allocations, including the ones deliberately left alone

- **`Ack/mem` is 0 allocs.** A map delete and the lease's removal from the
  expiry heap (#1036), whose position table grows amortised.
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
- **`Receive/file` allocated ~450 B per queued message scanned** (456 311 B at a
  backlog of 1 000): `fs.ReadDir` building a `DirEntry` and a name for every
  entry in the directory, plus the sort. Since issue #811 it reads the names
  alone — ~100 B an entry (105 668 B at 1 000) — and none on a quiet directory.
  It is the same fact as the scaling limit above, in bytes rather than in
  nanoseconds, and the answer to a deep backlog that keeps moving is a
  different broker rather than a cleverer scan.
- **`Publish/mem` at 64 KiB is a GC benchmark, not a queue benchmark.** At
  `-benchtime=1s` it runs 116 k iterations and allocates ~7.6 GB, so the
  reported 118 µs is dominated by collection pressure — the same case measured
  25 µs at `-benchtime=300ms`. That instability is itself the finding: **an
  in-memory queue holding 64 KiB messages is a garbage-collection problem**,
  which is one more reason `NewMemory` is documented as a test double rather
  than as a lightweight production option.
