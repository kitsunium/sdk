<!-- generated from internal/service/writer/console/console_bench_test.go — run `go test -run='^$' -bench=. -benchmem -benchtime=1s -count=5 ./internal/service/writer/console/` and take medians -->
# Benchmarks — `internal/service/writer/console`

The console writer is one of the two writers ADR 0015 leaves **on by default**,
so every consumer who never configures logging is running this path. Nothing in
this repository had ever measured it.

A console writer writes to a terminal in production and to something else in
every test that captures it, and those are not the same cost. Three destinations
are measured and each is labelled with the deployment it stands for. **No row
here writes to a terminal**: a terminal's cost is the terminal emulator's, is
not reproducible, and is not this SDK's.

| row | what it is | who has it |
|---|---|---|
| `io.Discard` | no syscall at all | nobody — it is the package floor |
| `/dev/null` | one real `write(2)` the kernel drops | `app 2>/dev/null` |
| `pipe` | one real `write(2)` with a live consumer | a container's stderr, `\| tee`, a log collector |

**Believe the `pipe` row.** A containerised service's stderr is a pipe with a
collector on the far end, and that row is 6.2× the `/dev/null` one and 70× the
`io.Discard` one. `io.Discard` is in the report to separate this SDK's work from
the kernel's, not to describe a deployment.

> **Every figure below is the median of five runs** at `-benchtime=1s`. Spreads
> were under 4 % on the `io.Discard` and `/dev/null` rows and 18–27 % on the
> `pipe` rows, which is inherent: a pipe write wakes a reader, and when that
> happens is the scheduler's decision. The allocation columns are exact and did
> not vary. Load average at the start of the sweep: 1.85 — the highest of the
> three writer sweeps, and the reason the pipe rows carry their spread. Despite
> it, the two independently-measured pipe rows (§1 and §3) agree to 1.5 %.

## 1. One record, three destinations

| `Sink.Write`, 83-byte line | ns/op | B/op | allocs |
|---|---:|---:|---:|
| `io.Discard` — package floor | 26.63 | 0 | 0 |
| `/dev/null` — one real `write(2)` | 302.20 | 0 | 0 |
| `pipe` — one real `write(2)`, consumer reading | **1 868** | 0 | 0 |

Nothing allocates anywhere, at any destination.

The rows are the shipped composition, not a reassembly of it: they call
`writer.Open("console", cfg)` with `os.Stderr` pointed at the destination for
the duration of the construction and restored immediately — the same swap this
package's own external test already uses to capture output. The one exception is
`io.Discard`, which the factory cannot produce and which is labelled as a floor
rather than a deployment.

**The double is not a different code path.** `consoleSink.Write` calls
`s.w.Write(p)` and nothing else — no `ReadFrom`, no `WriteString`, no
`SetWriteDeadline` — so the interfaces `*os.File` implements and `io.Discard`
does not are never reached. That was checked in the source before the rows were
trusted, because an SSE benchmark elsewhere in this campaign measured a path
production never takes for exactly the opposite reason.

## 2. 74 % of the package floor is one mutex, and work inside it is free

The `io.Discard` row is 26.63 ns for a call that does nothing. Where it goes:

| | ns/op |
|---|---:|
| `sync.Mutex` Lock+Unlock, uncontended, one integer increment between | **19.63** |
| one interface call to `io.Discard.Write` | 2.84 |
| both — the write INSIDE the pair | **19.97** |

`19.63 + 2.84 = 22.47` predicted; **19.97 measured**. The interface write costs
0.34 ns when it sits inside the critical section instead of 2.84 ns on its own —
88 % of it disappears. This is a Zen 1 part, where a LOCK-prefixed instruction is
expensive and a tight loop of them stalls; unrelated work between two of them
lets the first retire.

(The first row holds one integer increment rather than nothing, because an empty
critical section is a linted defect and cannot be committed. It measured 19.57 ns
empty against 19.63 ns with the increment, which is the same finding applied to
itself.)

That is not a curiosity. It is the explanation for a row-set this report
**refuses to publish as a ranking**:

| `Sink.Write` into `io.Discard`, by context | ns/op |
|---|---:|
| `context.Background()` | 26.48 |
| `context.WithCancel(…)` | 23.60 |
| `context.WithValue` over a cancellable parent | 23.57 |
| `nil` — the check short-circuits, no `Err()` call at all | 25.50 |

The `nil` row does strictly less work than the other three and measures 1.9 ns
SLOWER, reproducibly, in isolation (each row re-run alone reproduces its
position), with per-row spreads of 0.6–3.6 %. The guard being measured —
`if ctx != nil && ctx.Err() != nil` — costs less than 0.4 ns when priced where it
actually sits, so a 2.9 ns spread across these four rows is the instruction
stream's interaction with the LOCK and not the cost of `Err()`. **This benchmark cannot resolve the context check**, and the honest
report of that is this paragraph rather than a table implying `Background` is
worse than a request-scoped context.

One thing the rows do settle, and it is worth settling because the opposite is
widely believed: **`cancelCtx.Err()` does not take a mutex.** It has not since
the stdlib replaced the lock with `c.err.Load()`, whose own comment says "an
atomic load is ~5x faster than a mutex, which can matter in tight loops"
(`$GOROOT/src/context/context.go`, go1.27.1). A logger on a request-scoped
context is not paying for a lock it did not know about.

## 3. What scales with the bytes, and what does not

| payload | `/dev/null` | `pipe` |
|---|---:|---:|
| 96 B | 308.4 ns | 1 841 ns |
| 1 KiB | 303.2 ns | 2 592 ns |
| 8 KiB | 305.5 ns | 5 187 ns |

**`/dev/null` is flat: 1.7 % across an 85× size range.** The kernel accepts the
length and copies nothing, so that row is a `write(2)` and nothing else — which
is why it is the right floor for isolating this SDK's per-call cost
(`../file/BENCH.md` §2 uses it for exactly that).

**A pipe genuinely copies.** The 8 KiB row costs 3 346 ns more than the 96 B one
for 8 096 extra bytes — 0.41 ns/byte, about 2.4 GB/s, which is a pipe copy plus
the reader's wake-up. A consumer emitting large structured lines to a container
log collector pays for every byte; one emitting to `2>/dev/null` does not.

## 4. Construction

| `writer.Open("console", cfg)` | ns/op | B/op | allocs |
|---|---:|---:|---:|
| zero config — stderr, inherit | 73.43 | 26 | 2 |
| `MinLevel: Error` | 100.50 | 50 | 3 |

The difference is **24 B and exactly one allocation** — the `levelgate` wrapper,
which `../levelgate/BENCH.md` §4 measures independently at 24 B / 1 alloc. Two
paths, same number.

The default's two allocations are the `consoleSink` itself and the boxing of the
2-byte `ConsoleConfig` into the `writer.Config` interface. This happens once per
writer, at start-up.

## 5. End to end: the writer adds NOTHING to "one alloc per emit"

The root `CLAUDE.md` sells the logger on one allocation per emit and
`pkg/v1/logger` pins it with `TestV116BuildSendAllocatesOnePerEmit`. That test
ends at the handler. This is the console half of what a WRITER adds underneath
it.

| a full emit — text encoder + `genericHandler` + transport | ns/op | B/op | allocs |
|---|---:|---:|---:|
| no transport at all | 485.6 | 128 | **1** |
| console → `io.Discard` | 488.7 | 128 | **1** |
| console → `/dev/null` | 965.7 | 128 | **1** |
| console → pipe | 2 729 | 128 | **1** |

**The allocation count does not move.** The allocation profile of that sweep
says why, and it is unambiguous:

```
Type: alloc_objects
Showing nodes accounting for 9815214, 98.91% of 9923438 total
Showing top 4 nodes out of 6
      flat  flat%   sum%        cum   cum%
   9815214 98.91% 98.91%    9815214 98.91%  slices.Clone[…logger.AttrValue,…] (inline)
         0     0% 98.91%    9815214 98.91%  …/service/logger.(*genericHandler).Handle
         0     0% 98.91%    9815214 98.91%  …/service/logger.mergeAttrs
         0     0% 98.91%    9815214 98.91%  …/writer/console_test.emitRow.func1
```

98.91 % of the objects — and 99.48 % of the bytes, 1.17 GB — are the handler's
attrs clone, which `pkg/v1/logger/BENCH.md` already attributes correctly. The
transport contributes **zero objects**, and so does everything else in the
profile: the whole list is six nodes.

The CPU profile of the same run splits the time honestly:

```
Duration: 6.72s, Total samples = 8260ms (122.86%)
      flat  flat%   sum%        cum   cum%
    1910ms 23.12% 23.12%     1910ms 23.12%  internal/runtime/syscall/linux.Syscall6
     660ms  7.99% 31.11%      660ms  7.99%  …/logger/encoder.appendSanitizedMessage (inline)
     630ms  7.63% 38.74%      630ms  7.63%  time.runtimeNow
     290ms  3.51% 42.25%      290ms  3.51%  …/logger/encoder.appendPad2 (inline)
     250ms  3.03% 45.28%     2560ms 30.99%  …/logger/encoder.(*textEncoder).Append
     230ms  2.78% 48.06%     6760ms 81.84%  …/service/logger.(*genericHandler).Handle
      70ms  0.85% 30.63%     1800ms 21.79%  …/logger/sink/console.(*consoleSink).Write
      20ms  0.24% 31.60%     1640ms 19.85%  internal/poll.(*FD).Write
```

`consoleSink.Write` is 21.79 % cumulative, of which `internal/poll.(*FD).Write`
is 19.85 %. **This SDK's console code is 1.94 % of an emit**; the rest of that
fifth is the kernel. The encoder is 30.99 % and the wall clock 7.63 %.

`TestConsoleWriterAddsNoAllocationToAnEmit` now pins the delta — the writer's
contribution, not a constant — and is mutation-checked in its own doc comment.

## 6. Eight goroutines get the throughput of one

| 8 goroutines, one Sink | ns/op | vs the same sink serially |
|---|---:|---:|
| `io.Discard` | 86.17 | 3.24× **worse** |
| `/dev/null` | 479.80 | 1.59× worse |
| `pipe` | 2 076 | 1.11× worse |

`RunParallel` reports wall time divided by total operations, so a value equal to
the serial one means perfect serialisation and a larger one means serialisation
plus contention. Every row here is at best the first and at worst the second:
**adding goroutines to a console writer adds no logging throughput.**

That is by design — the sink holds a mutex so concurrent goroutines emit whole
lines rather than interleaved fragments — but it is a property an operator sizing
a high-volume service needs stated. It also means the mutex of §2, which is
74 % of the package floor when uncontended, is the thing that decides the
ceiling: `io.Discard` at eight-way contention costs 3.24× its uncontended self
for work that is otherwise free.

Note that on `*os.File` destinations the sink's mutex is the SECOND lock in the
path — `internal/poll.FD.Write` takes a per-descriptor write lock of its own —
so the `/dev/null` and `pipe` rows would serialise even without it.
`../file/BENCH.md` §4 measures that pair directly and finds the sink's mutex
costs nothing on top.

## 7. Rows thrown away

- **The `pipe` rows at `-benchtime=20ms` read 854 ns in one benchmark function
  and 2 251 ns in another for the same payload and the same sink shape** — a
  2.6× contradiction between rows that must agree. At `-benchtime=1s` they
  converge to 1 868 and 1 841 ns (1.5 % apart). Twenty milliseconds is not long
  enough to amortise a drained pipe's start-up, and the smoke numbers were
  discarded rather than reconciled.
- **A whole sweep, twice.** The first was taken before the benchmark bodies were
  brought past `ktn-linter` — the row closures now take their parameters instead
  of reading a range variable, every `Write` error is checked instead of
  discarded, and the `mutex_pair` row gained an integer increment because an
  empty critical section is a linted defect. Publishing it would have described a
  program not in the tree, so every number here, and both pprof quotes, were
  re-measured against the committed source.
- **The four context rows of §2** are measured, reproducible and NOT published as
  a ranking, for the reason given there.

## Reproducibility envelope

> **Numbers vary across machines.** The allocation columns are exact. What this
> report asserts is the RATIOS — destination against destination, emit with the
> writer against emit without it — and two absolute figures: a console write to
> a real descriptor costs ~302 ns, and it allocates nothing.
>
> §2's decomposition is the most machine-dependent part of this file. A Zen 1
> LOCK is expensive; on a part with cheaper atomics the mutex would be a smaller
> share of the 26.63 ns floor and the `nil`-versus-cancellable inversion might
> not appear at all. It is published because it is the reason a row-set was
> refused, not because 19.63 ns is portable.

| | |
|---|---|
| CPU | AMD EPYC 7351P 16-Core Processor |
| CPU cores | 8 |
| RAM | 15 GiB |
| OS / kernel | Debian GNU/Linux 13 (trixie), Linux 6.12.101+deb13-amd64 |
| Architecture | amd64 |
| Go toolchain | go1.27.1 linux/amd64 |
| Git branch | jaimerias-que-tu-te-connect |
| Git commit | 35edd8c |
| Generated | 2026-09-10 |
| Bench wall-clock | `-benchtime=1s`, `-count=5`, medians |
| Load average at start | 1.85 |
