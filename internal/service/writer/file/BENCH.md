<!-- generated from internal/service/writer/file/file_linux_bench_test.go — run `SDK_BENCH_DISK_DIR=<dir on a block device> go test -run='^$' -bench='BenchmarkWrite$|BenchmarkFlush|BenchmarkWriteThenFlush|BenchmarkWriteParallel|BenchmarkOpen|BenchmarkEmit' -benchmem -benchtime=200ms -count=5 ./internal/service/writer/file/` plus `-bench=BenchmarkWriteSize -benchtime=500ms -count=5`, and take medians -->
# Benchmarks — `internal/service/writer/file`

The file writer is the other writer ADR 0015 leaves **on by default**. The trap
in measuring it is that a file benchmark measures the FILESYSTEM unless it is
stopped from doing so, and the difference between the two is three orders of
magnitude on the one call that matters.

Every row here therefore names the filesystem it ran on, **detected by
`statfs(2)` rather than assumed from the path**:

| label | what it is | why it is in the report |
|---|---|---|
| `devnull(syscall floor)` | a character device; the kernel drops the bytes | the only place this SDK's own cost is resolvable |
| `tmpfs` | `/dev/shm` — RAM | a write that reaches no device, and an `fsync` with nothing to push |
| `ext4` | a real block device, named by `SDK_BENCH_DISK_DIR` | the only row that can price durability |
| `tmpdir(<fs>)` | whatever `b.TempDir()` resolves to, labelled | see §3 — it is the point |

**A row here that carries no filesystem label is not measuring a file.**

> **Every figure below is the median of five runs.** The main sweep ran at
> `-benchtime=200ms` — an iteration cap rather than a time cap, because a 1 s
> row at 800 ns/op writes 200 MB and this VM balloons to 8 GiB. Spreads were
> under 10 % on every published row except `WriteParallel/tmpfs/raw_syscall`
> (10.6 %), which is device jitter and which agrees with its sink arm anyway.
> The allocation columns are exact. Load average at the start of the sweep:
> 1.36.

## 1. Nothing on the write path allocates, at any destination

| | ns/op | B/op | allocs |
|---|---:|---:|---:|
| `devnull` — the syscall floor | 318.5 | 0 | 0 |
| `tmpfs` | 882.3 | 0 | 0 |
| `ext4` | 1 007 | 0 | 0 |
| `tmpdir(ext4)` — an independent directory on the same device | 1 019 | 0 | 0 |

The two ext4 rows are separate files in separate directories on the same device
and agree to 1.2 %, which is this harness cross-checking itself.

## 2. What this package adds is ~21 ns, and it is per CALL

The filesystem rows cannot resolve it: the sink-minus-raw difference on ext4 is
66 ns against arms whose own spreads are 5.0 % and 9.5 %. So the pair is measured
again at `/dev/null`, where the kernel accepts the length and copies nothing.

| identical descriptor (`O_APPEND\|O_CREATE\|O_WRONLY\|O_NOFOLLOW`, 0600) | ns/op |
|---|---:|
| raw `*os.File.Write` | 290.0 |
| the sink `writer.Open("file", …)` returns | **318.5** |

And it does not grow with the payload:

| payload | sink | raw `*os.File.Write` | delta |
|---|---:|---:|---:|
| 96 B | 309.8 | 288.7 | +21.1 |
| 1 KiB | 312.1 | 290.7 | +21.4 |
| 4 KiB | 311.0 | 282.5 | +28.5 |

**The cost is per call, not per byte.** The sink row is flat to 0.7 % across a
42× size range and the raw row to 2.9 %; what varies is which of them the
machine favours on a given sweep, which is why the delta is quoted as **+21 ns
(median of the three) and 21–29 ns across all four floor pairs** rather than as
one figure. It is one interface call, one `ctx.Err()` check and one mutex pair,
none of which look at the bytes.

Two independent confirmations:

- `BenchmarkWrite/devnull` and `BenchmarkWriteSize/96B` are different benchmark
  functions over different sinks and agree on the sink arm to 2.8 % (318.5 vs
  309.8) and on the raw arm to 0.4 % (290.0 vs 288.7).
- `../console/BENCH.md` §1 measures a completely different sink — a different
  package, a different struct — over `/dev/null` at **302.2 ns**, against
  309.8–318.5 ns here. The two default writers cost the same because the thing
  they cost is the same mutex, priced at 19.63 ns in `../console/BENCH.md` §2.

At the default `MinLevel` there is no `levelgate` in the path at all: `New`
returns the sink unwrapped (`../levelgate/BENCH.md` §4), so none of those 21 ns
is the severity floor.

## 3. `b.TempDir()` is one environment variable away from being RAM

On the machine that produced this report `TMPDIR=/home/dev/tmp`, so `b.TempDir()`
lands on **ext4** and the `tmpdir(ext4)` rows above are real device rows.

Unset `TMPDIR` and `os.TempDir()` returns `/tmp`, which on this same machine is
**tmpfs**. The label follows, measured:

```
env -u TMPDIR go test -bench=BenchmarkFlush …
BenchmarkFlush/tmpdir(tmpfs)-8      219.0 ns/op
BenchmarkFlush/tmpfs-8              211.0 ns/op
```

**219 ns against 55 556 ns** on this machine's ext4 — a factor of **254**, and a
factor of **6 327** once the `fsync` has an append behind it (§4). A test suite
that asserts `Flush` returns nil would pass identically against a filesystem
where `fsync` pushes nothing to any device, and nothing in the assertion could
tell. That is why the row name carries the filesystem and why
`SDK_BENCH_DISK_DIR` is a named variable rather than a guessed path: this file
refuses to print a durability number for RAM.

## 4. Durability costs 1 376× a write, and the cadence is the lever

**A benchmark that only calls `Flush` measures almost nothing.** With no write
between them, consecutive `fsync`s find nothing dirty:

| `Flush` alone (nothing written between calls) | ns/op |
|---|---:|
| `tmpfs` | 208.7 |
| `ext4` | 55 556 |

The number a consumer actually pays is `fsync` after an append, because that
forces a journal commit as well as the data:

| write + flush every N records, per RECORD | `ext4` | vs a bare write | `tmpfs` |
|---|---:|---:|---:|
| every record | **1 385 658 ns** | **1 376×** | 1 115 ns |
| every 16 | 88 346 ns | 87.7× | 800.8 ns |
| every 256 | 7 524 ns | 7.5× | 780.2 ns |

The three cadences agree on what one `fsync` costs, which is the check that the
row-set is not noise: `(1 385 658 − 1 007)` = 1.385 ms,
`(88 346 − 1 007) × 16` = 1.397 ms, `(7 524 − 1 007) × 256` = 1.668 ms. The mild
upward drift is real — more dirty data accumulates before each sync — and the
three land within 20 % of each other over a 256× range of batch sizes.

**1 376× is the same order as the 1 865× ADR 0054 measured** for the queue
domain's durable round trip, on the same class of hardware and for the same
reason: the cost is the two device round trips and not the payload. Two domains,
two independent harnesses, one physical fact.

**And a flush with nothing dirty is 25× cheaper than one with an append behind
it** (55 556 against 1 385 658). Any report that prices `fsync` by calling it in
a loop is off by that factor. This one says so because its own first row-set did
exactly that.

On tmpfs the same cadence sweep spans 1 115 → 780 ns, a 1.43× range, because
there is no device: `fsync` on RAM is a syscall and a return.

## 5. End to end: the writer adds NOTHING to "one alloc per emit"

The root `CLAUDE.md` sells the logger on one allocation per emit and
`pkg/v1/logger` pins it with `TestV116BuildSendAllocatesOnePerEmit`. That test
ends at the handler. This is the file half of what a WRITER adds underneath it.

| a full emit — text encoder + `genericHandler` + transport | ns/op | B/op | allocs |
|---|---:|---:|---:|
| no transport at all | 494.1 | 128 | **1** |
| file → `tmpfs` | 1 473 | 128 | **1** |
| file → `ext4` | 1 603 | 128 | **1** |
| file → `tmpdir(ext4)` | 1 613 | 128 | **1** |

**The allocation count does not move.** The allocation profile says why:

```
Type: alloc_objects
Showing nodes accounting for 1464575, 100% of 1464869 total
Showing top 5 nodes out of 15
      flat  flat%   sum%        cum   cum%
   1392810 95.08% 95.08%    1392810 95.08%  slices.Clone[…logger.AttrValue,…] (inline)
     38997  2.66% 97.74%      38997  2.66%  runtime.mallocgc
     32768  2.24%   100%      32768  2.24%  …/logger/encoder.NewText
         0     0%   100%    1392810 95.08%  …/service/logger.(*genericHandler).Handle
         0     0%   100%    1392810 95.08%  …/service/logger.mergeAttrs
```

95.08 % of the objects are the handler's attrs clone, which
`pkg/v1/logger/BENCH.md` already attributes correctly; the remaining 5 % is
construction. `fileSink.Write` contributes **zero objects**.

The CPU profile of the same run:

```
Duration: 1.49s, Total samples = 1570ms (105.15%)
      flat  flat%   sum%        cum   cum%
     530ms 33.76% 33.76%      530ms 33.76%  internal/runtime/syscall/linux.Syscall6
     150ms  9.55% 43.31%      150ms  9.55%  time.runtimeNow
     100ms  6.37% 49.68%      100ms  6.37%  …/logger/encoder.appendSanitizedMessage (inline)
      70ms  4.46% 54.14%      430ms 27.39%  …/logger/encoder.(*textEncoder).Append
      70ms  4.46% 58.60%       70ms  4.46%  runtime.memmove
      50ms  3.18% 61.78%     1480ms 94.27%  …/service/logger.(*genericHandler).Handle
      50ms  3.18% 64.97%       50ms  3.18%  …/logger/encoder.appendPad2 (inline)
      30ms  1.91% 66.88%       60ms  3.82%  …/kernel/recycler.(*CappedPool[…]).Put
      30ms  1.91% 68.79%      240ms 15.29%  …/logger/encoder.appendHeader
      20ms  1.27%     —       680ms 43.31%  …/logger/sink/file.(*fileSink).Write
      10ms  0.64%     —       620ms 39.49%  internal/poll.(*FD).Write
```

`fileSink.Write` is 43.31 % cumulative, of which `internal/poll.(*FD).Write` is
39.49 %. **This SDK's file code is 3.82 % of an emit**; the rest of that share is
the kernel.

`TestFileWriterAddsNoAllocationToAnEmit` now pins the delta — the writer's
contribution, not a constant — plus `Write` and `Flush` at zero, and is
mutation-checked in its own doc comment.

## 6. Eight goroutines get the throughput of one, and the sink's mutex is free

| 8 goroutines, one file | sink | raw `*os.File` behind a plain mutex |
|---|---:|---:|
| `tmpfs` | 987.2 ns/op | 962.1 ns/op |
| `ext4` | 1 186 ns/op | 1 095 ns/op |
| `tmpdir(ext4)` | 1 126 ns/op | 1 179 ns/op |

Two findings, and neither is what a reader would guess from the code.

**The sink's mutex costs nothing.** It is measured against a raw descriptor
taking a `sync.Mutex` of its own, and the two arms are within 8 % everywhere,
with the raw arm slower in one of the three roots — i.e. inside the rows' own
spreads.   `internal/poll.FD.Write` already takes a
per-descriptor write lock, so the sink's mutex is the second lock in a path that
was going to serialise regardless.

**Adding goroutines adds no throughput.** `RunParallel` divides wall time by
total operations, so the ext4 row at 1 186 ns/op against 1 007 ns serially means
eight goroutines produced the throughput of one, minus 18 % spent contending.
A `Sink` is a serialisation point; that is what makes a log line atomic, and it
is what an operator sizing a high-volume service has to know.

## 7. Construction: the symlink refusal costs 2.2 µs, once

| `writer.Open("file", …)` + `Close`, ext4 | ns/op | B/op | allocs |
|---|---:|---:|---:|
| the factory | 6 678 | 512 | 7 |
| raw `os.OpenFile` + `Close` with identical flags | 4 507 | 184 | 3 |
| **the hardening** | **+2 171 (+48 %)** | **+328** | **+4** |

The extra work is the `Lstat` that refuses a pre-planted symlink (CWE-59) and
the objects around it. It is consistent across all three roots — +2 171, +2 045,
+2 201 ns — and it runs **once per writer**, at start-up, against a per-record
cost of ~21 ns. Two microseconds to close a symlink-swap attack is not a
trade-off that needs arguing.

Both arms time the `Close` too, deliberately. A first version stopped the timer
around it, and `b.StopTimer`/`b.StartTimer` cost more than the syscall pair they
were excluding — every row read ~8.6 µs. Timing open+close inflates nothing.

## 8. Rows thrown away

- **The payload sweep on real filesystems.** Run on tmpfs and ext4 at an
  iteration budget this VM can afford (10 000 — a 4 KiB row at 100 000 writes
  410 MB), five runs produced spreads of 35 %, 58 %, 86 %, 92 %, 101 % and
  111 %, with one ext4 outlier at 11 197 ns against a 3 255 ns median. At 4 KiB
  the row is page allocation and writeback, not code. The sweep was re-pointed
  at `/dev/null`, where the same question — does this package's cost scale with
  the payload — is answerable to 0.4 % (§2). The filesystem's own per-byte cost
  is the filesystem's; `../console/BENCH.md` §3 prices a destination that
  genuinely copies.
- **The first `Flush` row-set**, which called `fsync` in a loop with no write
  between calls and reported 55 µs as though it were the cost of durability. It
  is 25× too small (§4). The row is kept, relabelled as what it actually
  measures, and `WriteThenFlush` was added beside it.
- **An early `BenchmarkWrite` sweep**, taken at load average 1.28, read 994.7 ns
  for the tmpfs sink against 785.9 ns for the same code at load 0.37 — and
  disagreed with `WriteSize`'s own tmpfs row by 29 %. Discarded and re-run.
- **An early `BenchmarkOpen` sweep**, inflated by `b.StopTimer` (§7).
- **Every number in this file, once.** The first complete sweep predated the pass
  that brought these benchmarks past `ktn-linter` — the row closures now take
  their parameters instead of reading a range variable, and every `Write` and
  `Flush` error is checked instead of discarded. Publishing it would have
  described a program not in the tree, so everything here, including both pprof
  quotes, was re-measured against the committed source.

## Reproducibility envelope

> **Numbers vary across machines, and this file's headline number varies more
> than most.** 1 376× is this device: an ext4 volume with `discard`, on a
> virtualised host. A device that lies about write-through would report a much
> smaller ratio and would also lose the data — no code here can tell the
> difference, which is the same disclosure ADR 0056 makes about `fsync`. What
> this report asserts is the SHAPE: durability is three orders of magnitude
> above a write, batching moves it and buffering does not, and the package's own
> cost is a flat ~21 ns that no filesystem changes.
>
> The allocation columns are exact everywhere.

| | |
|---|---|
| CPU | AMD EPYC 7351P 16-Core Processor |
| CPU cores | 8 |
| RAM | 15 GiB |
| OS / kernel | Debian GNU/Linux 13 (trixie), Linux 6.12.101+deb13-amd64 |
| Architecture | amd64 |
| Block device | `/dev/sdb`, ext4, `rw,relatime,discard` |
| `TMPDIR` at run time | `/home/dev/tmp` (ext4) |
| Go toolchain | go1.27.1 linux/amd64 |
| Git branch | jaimerias-que-tu-te-connect |
| Git commit | 1be196a |
| Generated | 2026-09-10 |
| Bench wall-clock | `-benchtime=200ms` (main) / `500ms` (size), `-count=5`, medians |
| Load average at start | 1.36 |
