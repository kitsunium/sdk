<!-- generated from internal/service/data/docstore/docstore_bench_test.go — run `cd internal/service && GOWORK=off go test -run='^$' -bench=. -benchmem -benchtime=1s ./data/docstore/` to refresh -->
# Benchmarks — `internal/service/data/docstore`

These numbers answer one question, which is the reason ADR 0110 exists:

> **Does a write cost more as the store grows?**

The whole-file store this package replaces rewrote every document on every
write. Here a write publishes one overlay entry, and a fold rewrites the
snapshot once per N writes.

## Reproducibility envelope

> **Numbers vary across machines.** The `BenchmarkPutDisk` figures are a
> property of this device: macOS makes `File.Sync` an `F_FULLFSYNC`, which
> waits for the drive's cache. The *shape* is what travels: a write is flat
> across store sizes, and the whole-file rewrite is linear.

| Dimension | Value |
|---|---|
| CPU cores          | 10 (Apple M1 Pro) |
| RAM                | 16 GiB |
| OS / kernel        | macOS 26.6.2 (Darwin 25.6.0) |
| Architecture       | arm64 |
| Go toolchain       | go1.27.1 darwin/arm64 |
| Git branch         | `feat/framework-wave-3` |
| Git commit         | `679aad1` (pre-commit) |
| Generated (UTC)    | 2026-09-26 |
| Bench wall-clock   | `-test.benchtime=1s`, single run, 142 s total |

## Results

```
BenchmarkPut/docs=100-10                          413346        2967 ns/op      1745 B/op       27 allocs/op
BenchmarkPut/docs=10000-10                        268666        4515 ns/op      3234 B/op       31 allocs/op
BenchmarkPut/docs=100000-10                       263868        5574 ns/op      2900 B/op       31 allocs/op
BenchmarkRewriteEverything/docs=100-10             21769       54852 ns/op     55090 B/op      108 allocs/op
BenchmarkRewriteEverything/docs=10000-10             162     7062381 ns/op   9539373 B/op    10027 allocs/op
BenchmarkRewriteEverything/docs=100000-10             16    70343427 ns/op  67652559 B/op   100018 allocs/op
BenchmarkPutMemory/docs=100-10                    914630        1274 ns/op       696 B/op       12 allocs/op
BenchmarkPutMemory/docs=10000-10                  728335        1543 ns/op       711 B/op       13 allocs/op
BenchmarkPutMemory/docs=100000-10                 500372        2281 ns/op       711 B/op       13 allocs/op
BenchmarkPutDisk/docs=100-10                         109    10984770 ns/op      3629 B/op       63 allocs/op
BenchmarkPutDisk/docs=10000-10                        94    11005307 ns/op      3703 B/op       64 allocs/op
BenchmarkGet-10                                  1327089         902.1 ns/op     336 B/op       11 allocs/op
BenchmarkGet_Parallel-10                         5249460         208.5 ns/op     258 B/op       10 allocs/op
BenchmarkLookup-10                               1113157        1077 ns/op       424 B/op       15 allocs/op
BenchmarkOpen/docs=100-10                           6571      186601 ns/op    161598 B/op     1511 allocs/op
BenchmarkOpen/docs=10000-10                           57    20873276 ns/op  18721115 B/op   140695 allocs/op
```

## What the numbers say

### A write no longer pays for the store

`BenchmarkPut` rewrites an existing document through the in-memory filesystem,
so the device is out of the number and only the store's own work is left: the
entry's encoding and publication, plus the folds the writes trigger, amortised.
`BenchmarkRewriteEverything` is the whole-file store's write, encoding every
document into one indented object and publishing it, over the same filesystem:

| documents | `Put` | whole-file rewrite | ratio |
|---|---|---|---|
| 100     | 2 967 ns | 54 852 ns      | **18×** |
| 10 000  | 4 515 ns | 7 062 381 ns   | **1 560×** |
| 100 000 | 5 574 ns | 70 343 427 ns  | **12 600×** |

A thousand times more documents cost the write **1.9×**: the maps grow, and
the fold, one full encoding per N writes, is spread over those N writes. The
same growth costs the rewrite **1 280×**, and 67 MB of allocation per write at
100 000 documents.

### On a disk, the device is the cost

| documents | `PutDisk` |
|---|---|
| 100    | 10.98 ms |
| 10 000 | 11.01 ms |

The two rows agree to 0.2 %. The cost is `WriteAtomic`'s two flushes (the
entry, then its directory), whatever the store holds. It is the same pair a
durable queue publish pays (see `internal/service/data/queue/BENCH.md`). A caller
who writes many documents at once pays one pair per document, because each
write is individually durable before it returns. That is the promise, not
overhead.

### Reads never touch a disk, and share their lock

`Get` is 0.9 µs: a map read under the readers' lock, plus the decode that makes
the result a copy. From ten goroutines at once it is 0.21 µs per read, because
the lock is shared and the decode runs outside it. `Lookup` adds the index
read: 1.1 µs.

### Opening is where the store is linear

`Open` reads the snapshot and decodes every document to rebuild the indexes:
0.19 ms for 100 documents and 20.9 ms for 10 000. That is the price of indexes
that are rebuilt rather than persisted (ADR 0110 §Alternatives), and it is paid
once per process.

These rows predate one change to `Open`: it now also calls `Key` once per
document, to check the key the document is stored under (ADR 0110 §D6). A rerun
on this machine allocated exactly as before (1 511 and 140 692 allocations per
open). Its timings are not quoted here because the machine was loaded (load
average 55) and they would measure the load.

### A version costs what it weighs (ADR 0143)

`BenchmarkPutVersioned` rewrites a document in a store keeping `versions`
former versions of each of its 100 documents, every document already holding
that many, over the in-memory filesystem.

| Dimension | Value |
|---|---|
| Machine | as above (10-core Apple M1 Pro, 16 GiB, macOS 26.6.2, go1.27.1 darwin/arm64) |
| Git branch | `feat/docstore-versions` |
| Git commit | `cdcbe78f` (pre-commit: the tree these rows ship with) |
| Generated (UTC) | 2026-09-28, load average 1.8 / 2.3 / 3.7 |
| Command | `cd internal/service && GOWORK=off go test -run='^$' -bench='PutVersioned\|BenchmarkPut$' -benchmem -benchtime=1s ./data/docstore/` |

```
BenchmarkPut/docs=100-10                    463015        2574 ns/op      1777 B/op       27 allocs/op
BenchmarkPutVersioned/versions=0-10         441181        2664 ns/op      1850 B/op       29 allocs/op
BenchmarkPutVersioned/versions=1-10         306878        3920 ns/op      2875 B/op       37 allocs/op
BenchmarkPutVersioned/versions=10-10        152655        8192 ns/op      7684 B/op       46 allocs/op
BenchmarkPutVersioned/versions=100-10        28453       41911 ns/op     50517 B/op      136 allocs/op
```

A store that keeps no versions writes what it wrote before: 2 664 ns, the
plain `Put`'s cost. A write that keeps them publishes the document AND its
versions in its one entry, because that is what makes the pair survive a crash
together, so its cost grows with what the versions weigh — about 0.39 µs a
kept version of this hundred-byte document — and not with the store's size.
On a disk the two flushes still dominate: 42 µs of encoding at a hundred
versions is 0.4 % of the 11 ms a durable write costs here.

### A page costs its documents, and a batch buys atomicity (#836, #1057)

`BenchmarkDocumentsPage` reads one page of 100 documents with `Documents`,
resuming a third of the way in; `BenchmarkDocumentsPass` pages through 10 000
documents by 100 from the last key read, against `Entries(0)`, which copies
them all at once; `BenchmarkBatch` writes 100 existing documents as one
`Batch`, against the same 100 `Put`s, in a store of 1 000, over the in-memory
filesystem and on disk.

| Dimension | Value |
|---|---|
| Machine | 4 of 8 vCPUs (`GOMAXPROCS=4`, AMD EPYC 7351P), 15 GiB, Linux 6.12.101 (Debian 13), ext4, go1.27.3 linux/amd64, shared with other jobs |
| Git branch | `wip/sdk-lot-6-cd` |
| Git commit | `65e0535` (pre-commit: the tree these rows ship with) |
| Generated (UTC) | 2026-11-03 |
| Command | `cd internal/service && GOWORK=off go test -run='^$' -bench='BenchmarkDocumentsPage\|BenchmarkDocumentsPass\|BenchmarkBatch/fs=mem' -benchmem -benchtime=1s ./data/docstore/`, then `-bench='BenchmarkBatch/fs=disk' -benchtime=10x` |

```
BenchmarkDocumentsPage/docs=1000-4         	   10000	    114008 ns/op	   25152 B/op	     202 allocs/op
BenchmarkDocumentsPage/docs=10000-4        	    2212	    475030 ns/op	   25152 B/op	     202 allocs/op
BenchmarkDocumentsPass/paged=100-4         	      27	  42640002 ns/op	 2038656 B/op	   10201 allocs/op
BenchmarkDocumentsPass/entries-4           	     296	   4131466 ns/op	 2347465 B/op	   10023 allocs/op
BenchmarkBatch/fs=mem/batch=100-4          	     987	   1241197 ns/op	  332356 B/op	    3143 allocs/op
BenchmarkBatch/fs=mem/puts=100-4           	    1165	   1033077 ns/op	  179242 B/op	    2878 allocs/op
BenchmarkBatch/fs=disk/batch=100-4         	      10	 330732182 ns/op	  554030 B/op	    7541 allocs/op
BenchmarkBatch/fs=disk/puts=100-4          	      10	 326873894 ns/op	  411212 B/op	    7206 allocs/op
```

A page allocates what it hands out — 202 allocations and 25 KiB at 1 000
documents and at 10 000 alike — and never a copy of the store's keys. Its
time is a pass over the keys under the read lock, so it grows with the
store: 114 µs at 1 000, 475 µs at 10 000. A whole pass by pages of 100
therefore costs about ten times one `Entries(0)` here (43 ms against 4 ms):
what paging buys is a bounded page in memory, and a reader that stops early,
not a cheaper full scan. A caller that reads everything at once and can hold
it should keep `List` or `Entries`.

A batch of 100 costs what 100 `Put`s cost, give or take its group entry:
20 % more in memory, 1 % more on this disk, where each key's entry still
pays its own flush. What it buys is that the 100 writes stand or fall
together, as ADR 0209's amendment says; paying the keys' flushes once per
batch is not this change.

## `GetInto` — a read into the caller's value (#813, 2026-11-11)

AMD EPYC 7351P VM (4 CPUs of the lane), Linux 6.12, go1.27.3, branch
`wip/sdk-lot-4-suite`, `-benchmem -count 6`, medians. `BenchmarkGet` now
formats its 10 000 keys once, before the loop, as `BenchmarkGetInto` does:
the row above counted `benchDoc`'s two `fmt.Sprintf` and its `Teams` slice
per read — 4 of its 11 allocations were the harness's, not the store's.

| Row | ns/op | B/op | allocs/op |
|---|---|---|---|
| `Get`, keys formatted in the loop (before) | 2 889 | 335 | 11 |
| `Get`, keys formatted once | 2 270 | 248 | 7 |
| `GetInto` | 2 049 | 88 | **5** |

The two allocations `GetInto` saves are the fresh `account` `Get` decodes
into — `json.Unmarshal` takes its address as an `any`, so it escapes — and
the `Teams` slice's growth from nil; the five left are the decoder's and the
document's own strings and slice, which differ from one read to the next.
The budget `allocs: 5` (`design/sdk.yaml`) holds that count in the alloc
lane. Command: `cd internal && go test -run '^$' -bench '^BenchmarkGet$|^BenchmarkGetInto$' -benchmem -count 6 ./service/data/docstore/`.
