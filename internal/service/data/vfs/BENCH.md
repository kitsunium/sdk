<!-- generated from internal/service/data/vfs/vfs_bench_test.go — run `cd internal/service && GOWORK=off go test -run='^$' -bench=. -benchmem -benchtime=1s ./data/vfs/` to refresh -->
# Benchmarks — `internal/service/data/vfs`

Two filesystems, four verbs, three payload sizes. These benchmarks exist to
answer one question honestly, because the whole domain is built on the answer:
**what does atomic publication actually cost, and what is it that costs?**

## Reproducibility envelope

> **Numbers vary across machines.** This report stamps the box that produced
> them so cross-machine deltas can be evaluated honestly. The disk figures in
> particular are a property of *this* device — a VM on overlayfs — and a
> machine with a battery-backed cache will report a different `WriteAtomic`
> entirely. The *shape* of the result is what travels, not the milliseconds.

| Dimension | Value |
|---|---|
| CPU cores          | 8 (AMD EPYC 7351P 16-Core Processor) |
| RAM                | 15 GiB |
| OS / kernel        | Linux 6.12.101+deb13-amd64 |
| Architecture       | amd64 |
| Go toolchain       | go1.27.1 linux/amd64 |
| Git branch         | `agent-a3061313ad370948a` |
| Git commit         | `e01714c` (pre-commit) |
| Generated (UTC)    | 2026-09-10 |
| Bench wall-clock   | `-test.benchtime=1s`, single run |

## Results

```
BenchmarkReadFile/mem/256-8          5994242     199.4 ns/op   1283.64 MB/s     256 B/op    1 allocs/op
BenchmarkReadFile/mem/4096-8          667056    1726   ns/op   2372.66 MB/s    4096 B/op    1 allocs/op
BenchmarkReadFile/mem/65536-8          78668   15425   ns/op   4248.59 MB/s   65536 B/op    1 allocs/op
BenchmarkReadFile/os/256-8            155294    7387   ns/op     34.65 MB/s     920 B/op    7 allocs/op
BenchmarkReadFile/os/4096-8           135936    8652   ns/op    473.44 MB/s    5272 B/op    7 allocs/op
BenchmarkReadFile/os/65536-8           40172   30056   ns/op   2180.48 MB/s   74136 B/op    7 allocs/op

BenchmarkWriteFile/mem/256-8         2799255     429.2 ns/op    596.49 MB/s     304 B/op    3 allocs/op
BenchmarkWriteFile/mem/4096-8         638139    1911   ns/op   2142.98 MB/s    4144 B/op    3 allocs/op
BenchmarkWriteFile/mem/65536-8         67384   18124   ns/op   3615.93 MB/s   65584 B/op    3 allocs/op
BenchmarkWriteFile/os/256-8             5941  195008   ns/op      1.31 MB/s     440 B/op    8 allocs/op
BenchmarkWriteFile/os/4096-8            6037  189462   ns/op     21.62 MB/s     440 B/op    8 allocs/op
BenchmarkWriteFile/os/65536-8           3378  345666   ns/op    189.59 MB/s     440 B/op    8 allocs/op

BenchmarkWriteAtomic/mem/256-8       2739458     441.2 ns/op    580.23 MB/s     304 B/op    3 allocs/op
BenchmarkWriteAtomic/mem/4096-8       628591    1817   ns/op   2254.06 MB/s    4144 B/op    3 allocs/op
BenchmarkWriteAtomic/mem/65536-8       70479   16646   ns/op   3937.07 MB/s   65584 B/op    3 allocs/op
BenchmarkWriteAtomic/os/256-8            387 3062407   ns/op      0.08 MB/s    1248 B/op   24 allocs/op
BenchmarkWriteAtomic/os/4096-8           392 3069464   ns/op      1.33 MB/s    1248 B/op   24 allocs/op
BenchmarkWriteAtomic/os/65536-8          376 3205589   ns/op     20.44 MB/s    1264 B/op   24 allocs/op

BenchmarkStat/mem-8                  8163771     130.6 ns/op                     32 B/op    1 allocs/op
BenchmarkStat/os-8                    654974    1768   ns/op                    240 B/op    3 allocs/op
```

## What the numbers say

### Atomic publication costs two device round trips, and nothing else

This is the headline, and it is the one measurement that should change how a
caller uses the domain:

| payload | `WriteAtomic` (os) | vs 256 B |
|---|---|---|
| 256 B  | 3 062 407 ns | — |
| 4 KiB  | 3 069 464 ns | +0.2 % |
| 64 KiB | 3 205 589 ns | +4.7 % |

**A 256× larger payload costs 4.7 % more.** The cost of publishing a file is
therefore not the file: it is the two `fsync(2)` calls — one on the temporary
before the rename, one on the parent directory after it — and those are device
round trips that do not shrink because the payload did. Everything else in the
sequence (the 16 bytes of entropy, `path.Join`, `O_EXCL` create, `rename(2)`)
is lost in the noise of the flushes.

The practical consequence, stated because it is the thing people get wrong:
**batching helps and buffering does not.** Publishing 100 small files costs
100 × 3 ms because each one pays its own two flushes; there is no payload size
at which the fixed cost amortises, because it is not per-byte. A caller
publishing many files should ask whether all of them need to be individually
crash-consistent, not whether they can be made smaller.

### What atomicity costs against writing in place

| payload | `WriteFile` (os) | `WriteAtomic` (os) | ratio |
|---|---|---|---|
| 256 B  | 195 008 ns | 3 062 407 ns | **15.7×** |
| 4 KiB  | 189 462 ns | 3 069 464 ns | **16.2×** |
| 64 KiB | 345 666 ns | 3 205 589 ns | **9.3×** |

That is the real price, and it is not small. It buys the guarantee measured by
`TestOnlyTheAtomicWriterSurvivesAConcurrentReader`, which runs both verbs under
an identical reader/writer race over a 64 KiB file and counts what the reader
saw:

```
torn reads out of 600:  WriteFile = 540–597   WriteAtomic = 0
                        (with -race:  203)              (0)
```

**Between 90 % and 99.5 % of concurrent reads of an in-place write observed a
file that was neither the old version nor the new one.** Not a rare race — the
common case. `WriteAtomic` was zero on every run, which is not luck: `rename(2)`
leaves no scheduling in which a partial file is observable.

So the 16× is the answer to "why not just write the file", and the number to
weigh it against is 90 %+, not "occasionally".

### The memory filesystem pays nothing for atomicity, and that is honest

`WriteAtomic/mem` (441 ns) and `WriteFile/mem` (429 ns) are the same number
within noise, at every size. There is no temporary, no rename and no flush,
because there is no device: publication is one map assignment under the write
lock the reader also takes. The memory filesystem is therefore a faithful
double for the *semantics* of `WriteAtomic` and tells you **nothing** about its
cost — anyone sizing a publisher must benchmark against `NewOS`.

### Allocations, including the ones that are larger than they need to be

- **Reads allocate the file, once, on the memory filesystem** (1 alloc, exactly
  the payload size). That single allocation is the defensive copy: a handle
  never shares a slice with the tree, which is what makes the double usable
  under `-race`.
- **The disk read path allocates 7 times and 920 B for a 256 B file.** That is
  `os.Root.ReadFile` — open, stat, and a buffer grown from the stat size — and
  it is stdlib code this package delegates to rather than reimplements. It is
  reported rather than hidden; it is not worth a private read loop to shave,
  since the same call is already 7.4 µs of syscall.
- **`WriteAtomic` on disk allocated 24 times and 1 248 B** when this table was
  taken (32 after #803's shared flush, 20 since #813 — see the last section).
  Roughly three times `WriteFile`'s 8, from the temporary's name (entropy, hex
  encoding, `path.Join`), the extra open, and the directory handle for the final
  flush. **What is left is not optimised further**: `os.Root`'s confinement is
  18 of the 20, and it stays (ADR 0056, ADR 0083). 1 248 B of garbage sits next to 3 milliseconds
  of `fsync`: removing every allocation on the path would improve the verb by
  well under a tenth of a percent, and each one removed would be a step away
  from the straightforward code that makes the failure paths auditable. The
  allocation count is published here so the trade is visible, not because it is
  a defect.
- **`ValidatePath` and `ValidatePerm` contribute zero.** See
  `internal/core/data/vfs/BENCH.md`: the accepting paths allocate nothing, so none of
  the counts above are guard overhead.

### Throughput is a red herring for the small sizes

The `MB/s` column is reported because `b.SetBytes` makes it free, but at 256 B
it is measuring the fixed cost, not the device: 0.08 MB/s for `WriteAtomic/os`
does not describe a slow disk, it describes 3 ms of flush divided by 256 bytes.
Read the `ns/op` column for anything under a few KiB.

## Step 5 shared per directory (#803, 2026-11-02)

Measured 2026-11-02 on the 4-CPU lane of the same 8-core AMD EPYC 7351P VM,
go1.27.3, branch `wip/sdk-lot-6-int`, `-benchtime=2s -count=4`, before → after
the directory flush went through a `kernel/concur/commit` group per directory
(benchstat, p = 0.029 where a delta is given). `WriteAtomic_Parallel` is 16
goroutines (`SetParallelism(4)` × 4), each replacing its own file in ONE
directory:

| Benchmark | before | after |
|---|---|---|
| `WriteAtomic/os/256` | 3.110 ms, 1 280 B, 24 allocs | 3.019 ms (~, p = 0.49), 1 812 B, 32 allocs |
| `WriteAtomic/os/65536` | 3.545 ms, 1 280 B, 24 allocs | 3.414 ms (~, p = 0.06), 1 809 B, 32 allocs |
| `WriteAtomic_Parallel` | 563 µs, 1 282 B, 24 allocs | 450 µs (−20 %), 1 172 B, 20 allocs |

- **One publisher pays the group's floor and nothing else**: eight allocations
  (the batch, its channels, the flush's goroutine — `concur/commit`'s
  `Do_Uncontended`, ~2 µs) beside a 3 ms flush, inside the noise of the time
  column.
- **Concurrent publishers into one directory share flushes**: the ones that
  reach step 5 while a flush runs share the next. The gain is 20 % here, not
  the 16× a count of flushes would suggest: ext4 already merges concurrent
  fsyncs of one directory at the journal, the caveat #803 named, so what is
  saved is the syscalls and the waits that the device did not merge.

## A batch under one directory flush (#827, 2026-11-03)

`WriteAtomicAll` publishes a batch file by file — each its own temporary,
file flush and rename — and flushes each directory once, after its last
rename. Measured 2026-11-03 on the same VM, go1.27.3, branch
`wip/sdk-lot-6-cq`, `-benchtime=1s -count=3`, sixteen 256-byte files into one
directory per call (`BenchmarkWriteAtomicAll`, `ns/file` its time divided by
the files):

| Benchmark | per file | allocations |
|---|---|---|
| `WriteAtomic/os/256` | 2.88–3.10 ms | 1 794 B, 32 |
| `WriteAtomicAll/os/1` | 2.77–2.97 ms | 1 809 B, 32 |
| `WriteAtomicAll/os/16` | **1.74–1.83 ms** (≈ 61 % of `WriteAtomic`) | 18 050 B, 317 a batch |
| `WriteAtomicAll/mem/16` | 340–370 ns | 4 864 B, 48 a batch |

The directory flush is shared; the sixteen file flushes are not, and they are
what is left: each must reach the device before its own rename. A batch of one
costs what `WriteAtomic` costs.

## The name in one allocation, the idle directory flushed in place (#813, 2026-11-12)

Measured 2026-11-12 on the same 8-core AMD EPYC 7351P VM (Linux
6.12.101+deb13-amd64), 4 CPUs (`GOMAXPROCS=4`), go1.27.4 linux/amd64, branch
`wip/sdk-lot-4-x13` against its base `26a4ae8a5`: two test binaries run in
turn, three rounds of `-test.bench '^Benchmark(WriteAtomic|WriteAtomic_Parallel)$'
-test.benchmem -test.count=2 -test.benchtime=1s`, benchstat over the six
samples of each:

| Benchmark | before | after |
|---|---|---|
| `WriteAtomic/os/256` | 2.848 ms, 1 848 B, **32 allocs** | 2.787 ms (~, p = 0.49), 1 105 B, **20 allocs** |
| `WriteAtomic/os/4096` | 2.836 ms, 1 841 B, 32 allocs | 2.825 ms (~, p = 0.94), 1 104 B, 20 allocs |
| `WriteAtomic/os/65536` | 3.130 ms, 1 843 B, 32 allocs | 3.100 ms (~, p = 0.49), 1 104 B, 20 allocs |
| `WriteAtomic_Parallel` | 425 µs, 1 190 B, 20 allocs | 435 µs (~, p = 0.94), 983 B, 16 allocs |

Where the twenty go (`-memprofile -memprofilerate=1`, 2 000 publications):
`os.Root` — the guard's `Lstat` 3, the temporary's `O_EXCL` create 5, the
rename 6, the directory's open for its fsync 4 —, the temporary's name 1, and
one tiny allocation the profile does not see. The twelve removed:

- **the temporary's name, 5 → 1.** It was sixteen random bytes, their hex and
  `path.Join`; it is the process's seed, drawn once, and a counter, written on
  the stack. A name found taken — only a planter that read the directory can
  guess the next one — falls back to a random name, at most three tries.
- **the directory's commit group, 8 → 0 when nobody shares it.** A publication
  that finds its directory idle flushes it on its own goroutine through
  `concur/commit`'s idle path (no batch, no channel, no goroutine), and the
  directory's entry is kept for the next one rather than rebuilt. Concurrent
  publications still share the next flush (#803): `WriteAtomic_Parallel` keeps
  its time.

`TestAnUncontendedPublicationStaysWithinItsAllocationBudget`
(`alloc_external_test.go`, the race-off lane and `test-386`) holds the
publication to the criterion, 22 on the caller's stack: nineteen counted here,
22 on CI's linux/386 runner, whose `os.Root` makes three more.
