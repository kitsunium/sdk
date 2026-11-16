<!-- generated from internal/kernel/collections/ring/ring_bench_test.go — run `cd internal/kernel && GOWORK=off go test -run='^$' -bench=. -benchmem -benchtime=1s ./collections/ring/` to refresh -->
# Benchmarks — `internal/kernel/collections/ring`

Lock-free single-producer / single-consumer (SPSC) ring buffer built on
`sync/atomic` (ADR 0005 kernel/collections/ring block `0.1.3.*`). These benchmarks isolate
every exported surface of the package and exist to defend the performance claim
that justifies the package: a non-blocking, zero-allocation FIFO hand-off
between exactly one producer and one consumer.

The happy-path benches (`TryWrite_Happy`, `TryRead_Happy`) keep the ring from
saturating/emptying so each operation lands on the fast path (two atomic loads,
one slot store/load, one atomic store) and proves **0 allocs/op**. The sentinel
benches (`TryWrite_Full`, `TryRead_Empty`) saturate/empty the ring so every call
returns the **pre-allocated** `Full` / `Empty` sentinel — also 0 allocs, which is
the regression guard for PR #15 Wave 6 finding #13 (sentinels must not be
constructed on the hot path). `Capacity` and `Len` cover the cheap reads.
`BenchmarkSPSC_ProducerConsumer` measures end-to-end throughput with a dedicated
producer/consumer goroutine pair (NOT `b.RunParallel`, which would spawn N
producers + N consumers and violate the SPSC invariant) across ring sizes 4, 64,
1024, and 65536 so the throughput-vs-depth curve is visible.

## Reproducibility envelope

> **Numbers vary across machines.** This report stamps the box that produced
> them so cross-machine deltas can be evaluated honestly.

| Dimension | Value |
|---|---|
| CPU cores          | 12 (12th Gen Intel(R) Core(TM) i7-1255U) |
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

> **This edition changes the reference platform.** The previous edition was
> measured on **arm64** (8-core, Linux 6.12.72-linuxkit) under **go1.26.4**; this
> one is **amd64** on the box stamped above. The two editions are NOT
> comparable — a reader drawing a delta across them would be measuring the
> architecture, not the code. The whole table was re-measured here rather
> than half-updated, so the cells stay comparable with each other.

## Results

```
goos: linux
goarch: amd64
pkg: github.com/kitsunium/sdk/internal/kernel/collections/ring
cpu: 12th Gen Intel(R) Core(TM) i7-1255U

benchmark                            median ns/op   spread   min–max          B/op   allocs/op
New-12                                      1 646    46.0%   1 638 – 2 395   9 472           1
TryWrite_Happy-12                           30.83     4.2%   30.79 – 32.10       0           0
TryRead_Happy-12                            30.86     3.4%   30.77 – 31.83       0           0
TryWrite_Full-12                            8.771     3.6%   8.742 – 9.062       0           0
TryRead_Empty-12                            8.302    10.1%   7.858 – 8.695       0           0
Capacity-12                                 3.337     2.6%   3.312 – 3.399       0           0
Len-12                                      4.709     3.7%   4.622 – 4.797       0           0
SPSC_ProducerConsumer/size4-12              306.5    10.5%   291.0 – 323.3       0           0
SPSC_ProducerConsumer/size64-12             244.3    19.9%   226.1 – 274.7       0           0
SPSC_ProducerConsumer/size1024-12           192.5    53.2%   188.2 – 290.6       0           0
SPSC_ProducerConsumer/size65536-12          186.5     5.6%   177.0 – 187.4       0           0
```

## How to read this

- **`BenchmarkNew`** — construction allocates the `queueRing` struct plus its
  `cap+1` slots backing array (`1024+1` ints here): **1 alloc/op**, the only
  benchmark that allocates. Everything below is the steady-state hot path.
- **`BenchmarkTryWrite_Happy` / `BenchmarkTryRead_Happy`** — single producer /
  single consumer, ring kept un-saturated / un-empty, so each call stays on the
  fast path. **0 allocs/op** confirms the slot store/load reuses the backing
  array.
- **`BenchmarkTryWrite_Full` / `BenchmarkTryRead_Empty`** — saturated / empty
  ring, so every call returns the package-level `Full` / `Empty` sentinel.
  **0 allocs/op** is the guard that these sentinels stay pre-allocated and are
  never constructed per call.
- **`BenchmarkCapacity` / `BenchmarkLen`** — the cheap reads: an immutable field
  widen and a two-atomic-load snapshot subtract. Both 0 allocs, low-single-digit
  ns/op.
- **`BenchmarkSPSC_ProducerConsumer/sizeN`** — one dedicated producer goroutine
  and one dedicated consumer goroutine move `b.N` records end-to-end across the
  lock-free buffer at ring depths 4 / 64 / 1024 / 65536. ns/op is per-record
  wall time across the full producer→consumer hand-off (including spin-retry on
  `Full`/`Empty`), not a single `TryWrite`. Deeper rings broadly amortise the
  back-pressure spin; the exact size-to-size ordering is scheduler-sensitive and
  jitters run-to-run on a shared machine, so read the magnitude (tens of ns per
  record), not a strictly monotonic curve.

## Edition 2026-11-03 — padded cursors, cached far cursor, mask (#1034) and the MPSC sibling (#805)

Measured before and after on ONE box, interleaved — the table above is another
box and is not compared with this one: AMD EPYC 7351P (8 vCPU), Linux 6.12,
go1.27.3, `-benchmem -count 6` (`-count 5` for the producer rows), medians.
`GOMAXPROCS=4` unless the row says `-8`. Command:
`cd internal && go test -run '^$' -bench . -benchmem -count 6 ./kernel/collections/ring/`
(the producer rows with `-cpu 4,8`). Every row is 0 B/op and 0 allocs/op except `New`.

| Row | Before (base `42143bf47`) | After | |
|---|---|---|---|
| `SPSC_ProducerConsumer/size4` | 297.7 ns | 254.3 ns | −15 % |
| `SPSC_ProducerConsumer/size64` | 374.3 ns | 148.7 ns | −60 % |
| `SPSC_ProducerConsumer/size1024` | 313.2 ns | 120.5 ns | −62 % (#1034: ≤ 60 % of before — met) |
| `SPSC_ProducerConsumer/size65536` | 325.4 ns | 54.8 ns | −83 % |
| `Len` | 4.97 ns | 2.88 ns | −42 % |
| `TryWrite_Happy` | 20.7 ns | 19.8 ns | within noise |
| `TryRead_Happy` | 20.7 ns | 20.5 ns | within noise |
| `TryWrite_Full` | 11.9 ns | 13.7 ns | +16 %: the refusal now refreshes `cachedHead` before answering |
| `TryRead_Empty` | 12.3 ns | 12.1 ns | within noise |
| `New` | 1 826 ns, 9 472 B | 2 872 ns, 8 192 B | 1 024 slots instead of 1 025, plus the padded header |

- **The hand-off is where the layout pays.** With `head` and `tail` on one
  line, every write invalidated the line the consumer was reading and every
  read the producer's; apart, and each side re-reading the far cursor only
  when the ring looks full or empty, a record crossing between two cores costs
  a third to a sixth of what it did.
- **The happy rows did not move, and #1034's "≤ 50 %" for them is not met on
  this box.** Each iteration is a write and a read, so two sequentially
  consistent stores (`XCHG` on amd64) — the publication barriers the
  ORDERING INVARIANT comments name. Go has no release-only store; the
  division the mask replaced and the second load the cache saves are noise
  beside them.

### The MPSC sibling against the mutex it would replace

`MPSC_Producers/ProducersN` moves `b.N` records from N producers to one
consumer through `NewMulti[int](1024)`; `MutexSPSC_Producers/ProducersN` does
the same through the SPSC ring behind one mutex every side takes — the async
logger's shape. A producer that finds the ring full, and the consumer that
finds it empty, yield (`runtime.Gosched`) before trying again. ns per record.

| Producers | MPSC (`-4`) | Mutex + SPSC (`-4`) | MPSC (`-8`) | Mutex + SPSC (`-8`) |
|---|---|---|---|---|
| 1 | 26.6 | 84.0 | 24.3 | 85.7 |
| 4 | 417.0 | 235.4 | 416.6 | 246.4 |
| 8 | 452.7 | 518.4 | 714.5 | 384.6 |

`MPSC_TryWrite_Happy` (one goroutine, a claim then a read): 41.5 ns, 0 allocs.

- **One producer: three times faster than the mutex.** The claim is one CAS on
  an uncontended line.
- **Four and eight producers saturating the ring: slower than the mutex.**
  Every writer CASes the same `tail` and polls the cell the reader is about to
  free; Go's mutex parks the losers instead. #805's "four producers ≤ 2× the
  SPSC row" (`SPSC_ProducerConsumer/size1024-8`: 132 ns) is NOT met: 417 ns,
  3.2×. Padding each cell to a cache line (476–524 ns at four) and a writer's
  full check on the reader's cursor (477–551 ns) were measured and are slower
  still.
- **So the async logger keeps its mutex and its SPSC ring** (ADR 0216, #805):
  at four producers its hand-off would go from 235 to 417 ns a record. And
  its `DropOldest` would change what it guarantees: behind the mutex, a writer
  that finds the ring full reads the oldest record off and stores its own in
  one critical section, so the newest record is always admitted; a
  `MultiQueue` writer may not read, so the eviction would move to the
  reader, and a writer facing a full ring would wait for it or lose its own
  record. `MultiQueue` stays, with no adopter until a measurement of a
  different MPSC shape — or of an adopter that is not saturated — shows it
  winning.

## Edition 2026-11-11 — the floor of the happy rows (#1034)

Same box as the 2026-11-03 edition (AMD EPYC 7351P, Linux 6.12, go1.27.3,
`GOMAXPROCS=4`), `-benchmem -count 10`, benchstat medians:
`cd internal && go test -run '^$' -bench 'SCStorePair|TryWrite_Happy|TryRead_Happy' -benchmem -count 10 ./kernel/collections/ring/`.

| Row | ns/op | against `SCStorePair` |
|---|---|---|
| `SCStorePair` | 20.79 ± 0 % | — |
| `TryWrite_Happy` | 21.14 ± 1 % | +1.7 % |
| `TryRead_Happy` | 20.70 ± 5 % | −0.4 % |
| `SPSC_ProducerConsumer/size1024` (`-count 6`) | 135.7 ± 26 % | 43 % of the 313.2 before #1034 |

- **`SCStorePair` is two `atomic.Uint64.Store` on padded words and nothing
  else** — tail's publication of a slot and head's release of it, the two
  stores an iteration of either happy row (a write then a read) cannot do
  without. The ring costs that and no more: the loads, the mask, the slot
  copy and the interface call are inside the noise of two stores. One store
  alone measures 10.6 ns here, an `atomic.Add` 10.2 ns: on amd64 every
  `sync/atomic` write is a locked instruction (`XCHG` for a store) that drains
  the store buffer.
- **Why 50 % of the committed row cannot be reached.** Every `sync/atomic`
  operation is sequentially consistent (the Go memory model), and an SC store
  on x86 needs the `StoreLoad` fence `XCHG` carries. The ring only needs a
  release store, a plain `MOV` under x86's TSO: a scratch copy of the ring
  with plain stores, correct for one goroutine only, measured 5.7 ns for the
  pair. Go does not let a package reach it: `internal/runtime/atomic`'s
  `StoreRel` is not importable, a `go:linkname` to it is refused by the
  linker's `-checklinkname` and by the SDK, and a plain store is a data race
  that `-race` reports and the compiler may reorder past the slot's store.
  Batching the publication would make `TryWrite` return before its item is
  visible, or `TryWrite` answer `Full` with a slot free. On arm64 the same
  `Store` is an `STLR`, already a release store.
- **The bar this row can hold**: `TryWrite_Happy` and `TryRead_Happy` within
  10 % of `SCStorePair` on the same box; `SPSC_ProducerConsumer/size1024` at
  most 60 % of the pre-#1034 row, met (120.5 ns on 2026-11-03, 135.7 here).

## Edition 2026-11-15 — #1034 — seuil relevé selon la règle du propriétaire du 2026-11-14

**The original bound.** #1034's acceptance: "benchstat on the same box:
TryWrite_Happy and TryRead_Happy ns/op at most 50 % of the committed row;
SPSC_ProducerConsumer/size1024 at most 60 %" — the base `ee1f48ba2` measured
beside the head. #1316 held the hand-off on the VM (38–43 %, the editions
above); neither happy row moved.

**The designs tried.** The owner's decision of 2026-11-14: challenge the
algorithm without assembly — the cursors published once a group of operations,
writes and reads in batches, a sequence number a slot, any design that does
not pay two sequentially consistent stores an iteration. Each design below was
built outside the repository as a copy of this ring changed in that one
respect, run under `-race` where each check applies — a producer and a
consumer moving 200 000 items, each read once and in order (not the design
grouping both sides: its writer may leave its last items unpublished), and the
three checks of `TestQueue_ContractAcrossGoroutines` —, and measured through
`Queue[int]` as the rows are: a write then a read an iteration, 1024 slots, ns
per iteration. The development VM: AMD EPYC 7351P (Zen 1), Linux 6.12,
go1.27.4, one CPU pinned (`taskset`), six interleaved rounds of 200 ms a row,
medians (`benchstat`); the base's rows and this ring's from their own test
binaries, eight interleaved rounds of 300 ms — base `TryWrite_Happy` 21.09 ns
and `TryRead_Happy` 21.25, this ring 21.47 and 21.54, `SCStorePair` 20.94,
`PublishFloor` 10.95. Indicative: the VM is shared.

| Design | ns an iteration (VM) | Against the base | The contract it breaks |
|---|---|---|---|
| Base `ee1f48ba2` | 21.1–21.3 | 1 | — |
| This ring (#1316), kept | 21.3–21.5 | 1.00–1.02 | none: it is at `SCStorePair`, the floor of the contract |
| Each side's own cursor in a plain field | 21.6–22.1 | 1.01–1.04 | none, and nothing saved: an atomic load is a plain `MOV` on amd64 |
| The reader publishes `head` every 16 reads, and when it finds the ring empty | 11.7–11.9 | 0.55–0.56 | `Len` counts the items read and not yet released (5 after five writes and two reads, where the contract says 3); `TryWrite` answers `Full` with a slot read, so the async sink's `DropOldest` would lose the newest record |
| Both sides every 16 operations, the writer at once when the reader has caught up | 12.7 (`TryWrite_Happy`'s shape), 21.7 (`TryRead_Happy`'s) | 0.59, 1.02 | the same, and the items written: a writer that goes idle leaves its last ones unreadable (one of three read); where every read is of the item just written, both stores remain |
| A sequence number a slot (Vyukov's SPSC) | 21.4–21.6 | 1.01–1.02 | `Len`, which needs the cursors, and the capacity, rounded up to a power of two — for the same two stores, on the slot instead of the cursor |
| The same, the cursors stored too for `Len` | 42.5–42.7 | 2.0 | the capacity only, at four stores an iteration |
| A `TryWriteN` / `TryReadN` sibling, one store a call | 21.7 a batch of 1; an item 5.5, 2.6 and 2.4 in batches of 4, 16 and 64 | 1.02; an item 0.26, 0.12, 0.11 | none, but the rows are `TryWrite` and `TryRead`, which it leaves as they are, and no caller batches yet: not added |
| `PublishFloor`: one store a write, nothing on the reader's side | 10.95–11.7 | 0.52–0.55 | `Len` and `Full`: the floor of every design whose written item the next read sees |
| `SCStorePair`: two stores | 20.9 | 0.98–0.99 | none: the floor of every design that keeps the contract |
| Two interface calls and nothing else | 4.4 | 0.21 | — |

- **The contract needs two stores an iteration.** An item `TryWrite` returned
  on must be readable by the next `TryRead`, on another goroutine or after the
  caller told one; `Len` is a snapshot any goroutine may take, exact at every
  cursor; `Full` means every slot is taken. The first needs the writer to
  publish its item with an atomic store before it returns, the other two the
  reader to release its slot with one before it returns: a plain field the
  other side read would be a data race. Every `sync/atomic` store is
  sequentially consistent, and on amd64 an `XCHG`. So is
  `internal/runtime/atomic.StoreRel`: `atomic_amd64.s` jumps to `Store`, and
  the compiler makes it an intrinsic on PPC64 only, an alias of `Store`
  everywhere else. The 2026-11-11 edition is right that Go offers no
  release-only store, wrong to suggest the runtime keeps a cheaper one on
  amd64: there is none to reach. The floor of the contract is `SCStorePair`,
  0.98–0.99 of the base on the VM, and this ring measures it.
- **Dropping the reader's store** costs `Len` and `Full` as the table says, and
  leaves the writer's store and the two interface calls: `PublishFloor`, 0.52
  to 0.55 of the base on the VM. No design reaches 0.50 there.
- **The writer's store cannot be grouped**: `TryRead_Happy` reads the item just
  written, so a grouped publication either publishes every write anyway or
  answers `Empty` with an item written.
- **Batching inside one call** keeps the contract — a `TryWriteN` publishes its
  items before it returns, a `TryReadN` releases its slots — and passes 0.50
  an item from four items a batch; it does not move the rows. The async sink's
  drainer, which takes up to `batchMax` records under one lock, is where it
  would go.
- **The hand-off.** A sequence number a slot hands a record over in ~28 ns
  where this ring takes 89–144 (`SPSC_ProducerConsumer/size1024`'s shape, four
  CPUs, eight rounds, noisy): the item and its flag share a line. With its
  cursors stored too, for `Len`, it still takes about half this ring's time
  (45–91 ns against 93–108, four CPUs pinned), and doubles the happy rows (2.0
  × the base). No design tried holds the three bounds at once; this ring is
  the contract-keeping design best on the happy rows, which none reaches.
- **Step 0** (`go tool objdump`): the base's `TryWrite` and `TryRead` each
  carry a `DIVQ` and an `XCHGQ`, this ring's an `XCHGQ` alone; the rows call
  them through the interface, so their inlining (`-gcflags=-m=2`: `TryWrite`
  inlinable, `TryRead` over the budget, at both commits) does not reach the
  rows. `perf stat` is not available on the VM; the A/B lane records it on
  the runner.

**The runner.** [Run 43669032740](https://github.com/kitsunium/sdk/actions/runs/43669032740)
of the A/B lane (`bench-ab.yml`): AMD EPYC 7763, 4 vCPUs, 20 interleaved
rounds, this ring (main's, the one this edition keeps) against `ee1f48ba2`.

| Rule | Row | Median | 95 % interval |
|---|---|---|---|
| `1034-trywrite-happy` | `TryWrite_Happy` head / base | 0.944 | 0.920 – 0.952 |
| `1034-tryread-happy` | `TryRead_Happy` head / base | 0.941 | 0.937 – 0.945 |
| `1034-spsc-1024` | `SPSC_ProducerConsumer/size1024` head / base | 0.762 | 0.753 – 0.773 |
| `1034-floor-sc` | `SCStorePair` head / `TryWrite_Happy` base | 0.822 | 0.808 – 0.825 |
| `1034-trywrite-allocs`, `1034-tryread-allocs` | allocs/op | 0 | 0 – 0 |

In ns: base `TryWrite_Happy` 6.03 and `TryRead_Happy` 6.08, this ring 5.69 and
5.72, `SCStorePair` 4.96. On that box the two stores alone are 0.81–0.82 of
the base's row: no design that keeps the contract reaches 0.50 there either.

**The new bounds.** Each is the high bound of the 95 % interval above, rounded
up to the hundredth: `TryWrite_Happy` at most **0.96** of the base's row,
`TryRead_Happy` at most **0.95**, `SPSC_ProducerConsumer/size1024` at most
**0.78** (`scripts/ci/bench-ab.conf`, rules `1034-trywrite-happy`,
`1034-tryread-happy`, `1034-spsc-1024`); allocations still at most 0, and
`1034-floor-sc` now holds the contract's floor at 0.50 or more. The rules stay
`report`: the bounds sit less than 1 % above one runner family's interval.

« relevé selon la règle du propriétaire du 2026-11-14 » — no design that keeps
the contract reaches the original bound, so each is raised to the best
design's measure on the runner, this ring's, with this trail.
