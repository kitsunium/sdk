<!-- generated from internal/service/vfs/vfs_bench_test.go — run `cd internal/service && GOWORK=off go test -run='^$' -bench=. -benchmem -benchtime=1s ./vfs/` to refresh -->
# Benchmarks — `internal/service/vfs`

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
- **`WriteAtomic` on disk allocates 24 times and 1 248 B.** Roughly three times
  `WriteFile`'s 8, from the temporary's name (entropy, hex encoding, `path.Join`),
  the extra open, and the directory handle for the final flush. **This is
  deliberately not optimised.** 1 248 B of garbage sits next to 3 milliseconds
  of `fsync`: removing every allocation on the path would improve the verb by
  well under a tenth of a percent, and each one removed would be a step away
  from the straightforward code that makes the failure paths auditable. The
  allocation count is published here so the trade is visible, not because it is
  a defect.
- **`ValidatePath` and `ValidatePerm` contribute zero.** See
  `internal/core/vfs/BENCH.md`: the accepting paths allocate nothing, so none of
  the counts above are guard overhead.

### Throughput is a red herring for the small sizes

The `MB/s` column is reported because `b.SetBytes` makes it free, but at 256 B
it is measuring the fixed cost, not the device: 0.08 MB/s for `WriteAtomic/os`
does not describe a slow disk, it describes 3 ms of flush divided by 256 bytes.
Read the `ns/op` column for anything under a few KiB.
