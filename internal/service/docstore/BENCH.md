<!-- generated from internal/service/docstore/docstore_bench_test.go — run `cd internal/service && GOWORK=off go test -run='^$' -bench=. -benchmem -benchtime=1s ./docstore/` to refresh -->
# Benchmarks — `internal/service/docstore`

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
durable queue publish pays (see `internal/service/queue/BENCH.md`). A caller
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
