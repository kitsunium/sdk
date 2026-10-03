<!-- generated from internal/service/net/sse/{sse,frame}_bench_test.go — run `cd internal/service && GOWORK=off go test -run='^$' -bench=. -benchmem -benchtime=1s -count=3 ./net/sse/` to refresh; every published number is the MEDIAN of the runs stated per section -->
# Benchmarks — `internal/service/net/sse`

An SSE server is defined by how many streams it holds **open**, not by its event
rate. So the first number here is not a ns/op: it is what one idle stream costs
while it is doing nothing, and it is the number that decides the box.

Before this file existed, `grep -rn 'AllocsPerRun|Benchmark' internal/service/net/sse/`
returned nothing at all, and three of the claims below were prose in a doc
comment with no executable statement anywhere in the repository.

## What an open stream costs while it does nothing

Medians of three, `benchStreams = 2000` opened at once, heap read after a
`runtime.GC()` so it is what is RETAINED rather than what has been allocated.

| | goroutines | heap | stack | total |
|---|---:|---:|---:|---:|
| default (watcher + keep-alive) | **2** | 2 427 B | 5 685 B | **8 112 B** |
| `WithoutKeepAlive()` | **1** | 1 713 B | 2 998 B | **4 711 B** |

**10 000 concurrent streams is 20 000 goroutines and 81.2 MB** — 24.3 MB of
heap and 56.9 MB of goroutine stacks. `WithoutKeepAlive()` halves it to 10 000
goroutines and 47.1 MB.

The stack column is reported separately because it cannot appear anywhere else.
A goroutine's stack is not heap, so it never shows in a `B/op` column; a
benchmark that reported only allocations would understate an SSE server's memory
by the LARGER of the two numbers. `StackInuse` is ~2 850 B per goroutine here,
not the 8 KiB a reader might assume.

Setting one up and tearing it down, medians of three:

| | ns/op | B/op | allocs/op |
|---|---:|---:|---:|
| `New` + `Close`, with keep-alive | 9 235 | 2 513 ¹ | **30** ¹ |
| `New` + `Close`, `WithoutKeepAlive()` | 4 988 | 1 904 ¹ | **21** |

¹ Re-measured 2026-10, when the keep-alive moved onto `worker.Every` and its
clock became injectable: one allocation and 32 B more per stream with
keep-alive — the tick is now a closure of its own beside the daemon's loop — and
16 B more without, because the option set carries the keep-alive's clock. The
deltas were measured on darwin/arm64 (Apple M1 Pro, go1.27.1), where the
previous code reproduces the published 2 482 / 1 889 B to the byte (29 / 21
allocations) and this code reads 2 514 / 1 905 B (30 / 21); the published rows
carry the same deltas. ns/op and the footprint figures below were not
re-measured.

Thirty allocations, not the fifteen a reading of the code suggests, and
2 481 B allocated against 2 427 B still live afterwards (before the change
above) — so essentially nothing
about opening a stream is transient. The ns/op is bounded below by two scheduler
round trips, because `Close` JOINS both goroutines; it is not a measurement of
this package's code so much as of the runtime's hand-off.

### The second goroutine, and why it is still there

The delta between the two rows is exactly one `worker.LoopDaemon`, its two
channels, its ticker and the tick closure: **9 allocations, about 610 B of heap
(8 and 594 B before the keep-alive ran on `worker.Every`), 2 687 B of stack and
4 247 ns**. Merging the watcher into the keep-alive would recover all of it
and take 10 000 streams from 81.2 MB to 47.1 MB.

It is **refused**, and the reason is a failure mode rather than a preference.
The keep-alive calls `Comment`, which takes `s.mu`. A merged goroutine that was
inside `Comment` waiting for a `Send` to finish would not be in its `select`,
and so would not observe the drain — for up to `WriteTimeout` (10 s by default),
against a peer that has stopped reading. That is precisely the "one open stream
burns the whole drain budget" that ADR 0043 exists to close, re-introduced in a
form that is timing-dependent and therefore invisible in CI. A `TryLock`-based
merge looks like it answers this and is a larger change than the memory is worth
in this pass; it is written down here so the next person starts from the
measured 34.1 MB rather than from an estimate.

## The benchmark harness everyone would have built measures the wrong path

`httptest.ResponseRecorder` implements `Flush` but not `SetWriteDeadline`. So
does this package's own `capture` test double. A `Stream` built on either sets
`deadlines = false` at construction and **never executes** the per-frame
deadline refresh — which is the path production takes on every single frame.

Both harnesses are therefore published side by side. `no_deadlines` is what a
recorder-based benchmark reports; `deadlines` is what a socket costs.

| | no deadlines | deadlines | the refresh costs |
|---|---:|---:|---:|
| `Send`, 64 B | 80.10 ns | **169.3 ns** | **+89.2 ns (2.11×)** |
| `Send`, 256 B | 99.12 ns | 189.1 ns | +90.0 ns |
| `Send`, 1 KiB | 173.1 ns | 255.1 ns | +82.0 ns |
| `Send`, 4 KiB | 472.7 ns | 553.4 ns | +80.7 ns |
| `Send`, 64 KiB | 6 550 ns | 6 683 ns | +133 ns |
| `Comment` (the keep-alive frame) | 55.19 ns | **138.8 ns** | **+83.6 ns (2.52×)** |

A recorder-based benchmark understates a small send by **2.11×** and the
keep-alive frame — the only work an idle stream ever does — by **2.52×**.

The same gap had a second consequence, found while building this harness and
larger than the benchmark one: **no test in the repository executed the deadline
path either** — neither the per-frame refresh nor the `s.deadlines = false`
degradation beneath it. `deadlineWriter`,
`TestWriteDeadlineIsRefreshedOnEveryFrame` and
`TestAFailingWriteDeadlineDegradesRatherThanFailingTheFrame` close that; all
three mutations are recorded in their doc comments.

The refresh is not the `ResponseController` walk. It is the clock. CPU profile
of `Send/deadlines/64B`, verbatim:

```
Showing nodes accounting for 3200ms, 88.15% of 3630ms total
      flat  flat%   sum%        cum   cum%
    1500ms 41.32% 41.32%     1500ms 41.32%  time.runtimeNow
     270ms  7.44% 48.76%     3540ms 97.52%  ...service/net/sse.(*Stream).Send
     260ms  7.16% 55.92%      260ms  7.16%  indexbytebody
     180ms  4.96% 60.88%      180ms  4.96%  ...core/net.validateSSELine
     170ms  4.68% 65.56%      170ms  4.68%  net/http.(*ResponseController).Flush
     130ms  3.58% 69.15%      710ms 19.56%  ...core/net.appendSSEData
     120ms  3.31% 72.45%      120ms  3.31%  runtime.memmove
     110ms  3.03% 75.48%      110ms  3.03%  internal/bytealg.IndexByteString
     100ms  2.75% 78.24%     1890ms 52.07%  ...service/net/sse.(*Stream).write
```

`time.Now()` is **41.32 %** of a 64-byte send. This host's clock source is
`kvm-clock`, so `time.Now()` is ~72 ns here against ~20 ns on a bare-metal TSC
box; the share is a property of the machine and the ratio table above is not.

Nothing was done about it, and the alternatives were considered and refused:
setting the deadline less often than per frame breaks the contract the deadline
exists for (a frame written a nanosecond before the previous deadline would get
no budget), and there is no coarse monotonic clock in the standard library. The
number is published so that a reader who is surprised by an SSE server's CPU on
a virtualised host has somewhere to look first.

## What the frame-encoder change was worth in situ

The terminator scan — in `internal/core/net` when this was measured, this
package's `appendData` since ADR 0160 moved the encoder here — was 91 % of
encoding a frame and is now two assembly passes (§The encoder, below).
Isolated, the encoder (`SSEEventValue.AppendTo` then, `AppendEvent` now) got
2.56×–4.62×. In situ, through `Send` — which adds a mutex, a terminal check, a
write and a flush, and on the production path a clock read — it is:

| `Send` | before | after | in situ | isolated |
|---|---:|---:|---:|---:|
| no deadlines, 64 B | 157.7 ns | **80.10 ns** | 1.97× | 2.56× |
| no deadlines, 256 B | 236.2 ns | **99.12 ns** | 2.38× | 3.34× |
| no deadlines, 1 KiB | 593.4 ns | **173.1 ns** | 3.43× | 3.66× |
| no deadlines, 4 KiB | 1 956 ns | **472.7 ns** | 4.14× | 4.51× |
| no deadlines, 64 KiB | 30 156 ns | **6 550 ns** | **4.60×** | 4.62× |
| deadlines, 64 B | 235.1 ns | **169.3 ns** | 1.39× | — |
| deadlines, 4 KiB | 2 069 ns | **553.4 ns** | 3.74× | — |
| deadlines, 64 KiB | 30 264 ns | **6 683 ns** | 4.53× | — |
| `Comment`, no deadlines | 71.55 ns | **55.19 ns** | 1.30× | 1.59× |
| `Comment`, deadlines | 149.7 ns | **138.8 ns** | 1.07× | — |

The in-situ number is the one that counts, and at small sizes it is markedly
lower than the isolated one — the stream's own fixed cost (≈ 40 ns without
deadlines, ≈ 129 ns with) does not shrink. The 64 KiB rows are where the two
converge, because there the encode is nearly the whole send.

The keep-alive at 1.07× on the production path is the honest form of a 1.59×
isolated win: the frame is ten bytes, and the clock read is most of what remains.

## The retention ceiling: what it prevents, and what it costs

A `Stream` keeps its encode buffer for its whole LIFE. With no ceiling, one
outsized event pinned its own size until the client disconnected — invisible in
every allocation profile, because the allocation happened once and legitimately.
Measured, on a stream that sent exactly one one-mebibyte event:

| after sending | buffer held |
|---|---:|
| 64 B | 512 B |
| 32 KiB | 40 960 B |
| 1 MiB, with no ceiling | **1 056 768 B, forever** |
| 1 MiB, then one 64 B frame | **512 B** |

1 056 768 B × 10 000 streams is **10.6 GB**, bought by an event that happened
once. `B/op` cannot see this: it counts bytes ALLOCATED, and the defect is bytes
still HELD. It needed its own measurement, and it has its own test —
`TestFrameBufferRetentionIsBounded` reads `cap(s.frame)` directly.

The first attempt at a ceiling was a plain size cap: release anything above
64 KiB. It is the obvious reading and it is **wrong for the one workload it
hits**, which the sweep found immediately because a row moved that had no
business moving:

| `Send`, 64 KiB, no deadlines | ns/op |
|---|---:|
| the policy that ships | 6 550 |
| release on size alone | **25 458** |

**3.89×**, paid by exactly the traffic the ceiling was never aimed at: a stream
that genuinely sends outsized frames re-grew its buffer on every send. What
ships releases only when the buffer is above the ceiling AND the frame just
written used less than half of it — so an outsized buffer is kept while the
frames still USE it, and given back on the first one that does not. Both halves
are gated, and both gates are mutation-checked in their doc comments.

The trade is then priced on both sides, at 256 KiB per frame, medians of seven
runs taken in isolation:

| | ns/op | B/op | allocs/op |
|---|---:|---:|---:|
| `repeated` — every frame outsized, buffer kept | **26 589** | 6 | **0** |
| `alternating` — outsized, then small, per PAIR | 104 281 | 270 849 | 2 |

(These two are the ISOLATED medians of seven. §Results prints the same two rows
measured inside the full sweep — 26 975 and 127 800 — which is not a
transcription error: `alternating` is GC-bound, so what the rest of the binary
allocated before it reaches it moves the number. The isolated figures are the
ones this section reasons from and the sweep figures are what the sweep
produced; neither is edited to agree with the other.)

The release-and-regrow costs ≈ **77.7 µs** on a 256 KiB buffer, or **3.92×** the
frame itself. That is the price of not pinning 256 KiB per stream forever, and
it is paid once per outsized burst rather than once per frame.

> `alternating` is the one unstable row in this file: 35 % spread across seven
> runs, because its cost is the garbage collector's rather than this package's.
> Six of the seven cluster inside 5 % (103.6–108.8 µs); the outlier is the first
> run of a fresh binary at 80.6 µs, before the pacer has settled. The median of
> seven is published rather than the mean, and the spread is stated rather than
> smoothed.

## Steady-state sends allocate nothing — and now something says so

`initialFrameCapacity`'s doc comment has claimed since the package was written
that "a steady-state send allocates nothing". It is TRUE, and it had no gate:

| | allocs/op |
|---|---:|
| `Send`, 64 B … 4 KiB, warm | **0** |
| `Comment` (keep-alive), warm | **0** |
| `Send`, outsized, warm, every frame outsized | **0** |

The claim holds only ABOVE the buffer's high-water mark — the first frame of a
given size legitimately grows it — and the doc comment now says so, beside the
ceiling that bounds how high the mark may go. The three rows are pinned by
malloc-total gates in `sse_alloc_internal_test.go`, which is `//go:build !race`
because the race detector allocates shadow state on every memory access; the
race-off alloc lane is its only gate, and `//internal/service/net/sse:sse_test`
is in `tools/alloc-lane-targets.txt` for that reason (SDK-wide rule 12).

## Draining: 1.8 ms, against a guard that would have accepted 3 seconds

This package's `CLAUDE.md` says a graceful shutdown "finishes in milliseconds";
ADR 0043 says "40 ms, clean". The only executable statement anywhere was
`pkg/v1/net/server`'s `TestHTTPAdapterDrainsOnShutdown`, which fails at **3 s** with
three streams open — three orders of magnitude looser than the claim. A
regression from 40 ms to 2.9 s would have passed every test in the repository.

Measured over **64** open streams on a real `http.Server`, medians of three:

| | `Shutdown` returns in |
|---|---:|
| the drain signal is published | **1.808 ms** (28 µs/stream) |
| no drain signal — the ADR 0043 defect | **300.4 ms** = the whole budget |

The claim is correct. `TestDrainWithOpenStreamsFinishesInMilliseconds` now
asserts under 100 ms and runs the control beside it, so the fast number never
stands alone. Per stream, the two halves are:

| | ns/op |
|---|---:|
| `close(draining)` → `<-stream.Done()` | 5 932 |
| `Close()` (end, then JOIN), with keep-alive | 5 062 |
| `Close()` (end, then JOIN), `WithoutKeepAlive()` | 2 501 |

Those are microseconds of goroutine hand-off, and they are why 64 streams cost
1.8 ms rather than 64 × anything interesting: the streams observe the signal
concurrently, and only the joins serialise.

## What was refused, and why

- **Merging the watcher and the keep-alive goroutines.** Worth 34.1 MB per
  10 000 streams; refused on the drain-observation failure mode above.
- **Buffering or batching frames to amortise the flush.** The flush IS the
  protocol: an unflushed frame sits in the transport buffer until the handler
  returns, which on an endless stream is never. A buffered stream passes every
  test that does not assert on timing and is broken.
- **Caching the resolved `SetWriteDeadline` target at construction** to skip
  `ResponseController`'s unwrap walk. The profile says that walk is not the
  cost — `time.Now()` is 41 % and `ResponseController.Flush` is 4.7 % — so the
  change would buy a few nanoseconds and give up net/http's own contract about
  when the chain is resolved.
- **A shorter deadline refresh cadence.** See §The benchmark harness.
- **Shrinking `initialFrameCapacity` below 512 B** to trim the 2 427 B a stream
  holds. It would move 512 B of retention into an allocation on the first send
  of every stream, which is the wrong direction for a domain whose cost is
  measured per open connection.

## Reproducibility envelope

> **Numbers vary across machines.** The allocation and goroutine columns are
> exact; the retention column is exact. What this report asserts is the
> **ratios** — deadlines-vs-not, before-vs-after, ceiling-vs-not — and the two
> absolute figures an operator sizes with: 8 112 B and 2 goroutines per stream.
>
> **Every published number is the median of three runs** at `-benchtime=1s`,
> except §The retention ceiling's `OutsizedFrame` rows (median of SEVEN, taken
> in isolation, for the reason stated there) and the drain latencies (median of
> three whole-test runs). Spreads: every row sits inside 7 % across its runs
> except `OutsizedFrame/alternating` (35 %, diagnosed above) and
> `NewClose/with_keepalive` (4.8 %).
>
> **The clock source is `kvm-clock`**, on a virtualised host. `time.Now()` costs
> ~72 ns here. On a bare-metal TSC host it is ~20 ns, which would take
> `Send/deadlines/64B` from 169 ns to roughly 117 ns and change the
> deadlines-vs-not ratio from 2.11× to about 1.45×. That row is the only one in
> this file that is strongly machine-dependent, and it is called out rather than
> published as if it were portable.
>
> **One row-set was thrown away.** The first `Send/no_deadlines/64 KiB` figure
> read 6 639 ns and the next read 25 458 ns on what looked like the same code.
> The cause was real and is now §The retention ceiling: the first ceiling
> released any buffer above 64 KiB, and a 64 KiB payload encodes to a frame just
> ABOVE 64 KiB, so that row — and only that row — re-allocated on every send.
> The discrepancy was found by cross-checking two rows against each other rather
> than by re-reading the code, and it changed the design.
>
> Machine load at measurement time was `load average: 0.28–1.40`, all of it this
> benchmark; no other job was running.

| Dimension | Value |
|---|---|
| CPU cores          | 8 |
| RAM                | 15 GiB |
| OS / kernel        | Linux 6.12.101+deb13-amd64 |
| Architecture       | amd64 |
| Clock source       | kvm-clock |
| Go toolchain       | go1.27.1 linux/amd64 |
| Git branch         | jaimerias-que-tu-te-connect |
| Git commit         | f2d263d |
| Generated (UTC)    | 2026-09-10 |
| Bench wall-clock   | `-test.benchtime=1s`, `-test.count=3`, medians |

## Results

`before` is `git show HEAD:internal/core/net/sse.go` restored into the tree —
i.e. this package unchanged, over the previous frame encoder — and re-measured
on the same binary. `after` is what ships.

```
goos: linux
goarch: amd64
pkg: github.com/kitsunium/sdk/internal/service/net/sse
cpu: AMD EPYC 7351P 16-Core Processor
                                          BEFORE         AFTER
BenchmarkNewClose/with_keepalive               —      9235 ns/op   2513 B/op   30 allocs/op   (B/op, allocs re-measured 2026-10, see ¹)
BenchmarkNewClose/without_keepalive            —      4988 ns/op   1904 B/op   21 allocs/op   (B/op re-measured 2026-10, see ¹)
BenchmarkSend/no_deadlines/0000064      157.7 ns/op   80.10 ns/op      0 B/op    0 allocs/op
BenchmarkSend/no_deadlines/0000256      236.2 ns/op   99.12 ns/op      0 B/op    0 allocs/op
BenchmarkSend/no_deadlines/0001024      593.4 ns/op   173.1 ns/op      0 B/op    0 allocs/op
BenchmarkSend/no_deadlines/0004096       1956 ns/op   472.7 ns/op      0 B/op    0 allocs/op
BenchmarkSend/no_deadlines/0065536      30156 ns/op    6550 ns/op      0 B/op    0 allocs/op
BenchmarkSend/deadlines/0000064         235.1 ns/op   169.3 ns/op      0 B/op    0 allocs/op
BenchmarkSend/deadlines/0000256         321.4 ns/op   189.1 ns/op      0 B/op    0 allocs/op
BenchmarkSend/deadlines/0001024         665.5 ns/op   255.1 ns/op      0 B/op    0 allocs/op
BenchmarkSend/deadlines/0004096          2069 ns/op   553.4 ns/op      0 B/op    0 allocs/op
BenchmarkSend/deadlines/0065536         30264 ns/op    6683 ns/op      0 B/op    0 allocs/op
BenchmarkComment/no_deadlines           71.55 ns/op   55.19 ns/op      0 B/op    0 allocs/op
BenchmarkComment/deadlines              149.7 ns/op   138.8 ns/op      0 B/op    0 allocs/op
BenchmarkOutsizedFrame/repeated        123691 ns/op   26975 ns/op      6 B/op    0 allocs/op
BenchmarkOutsizedFrame/alternating     168021 ns/op  127800 ns/op 270849 B/op    2 allocs/op
BenchmarkDrainToDone                           —      5932 ns/op    199 B/op    2 allocs/op
BenchmarkCloseJoin/with_keepalive              —      5062 ns/op    247 B/op    2 allocs/op
BenchmarkCloseJoin/without_keepalive           —      2501 ns/op      0 B/op    0 allocs/op

BenchmarkStreamFootprint/with_keepalive       2.000 goroutines/stream  2427 heapB/stream  5685 stackB/stream
BenchmarkStreamFootprint/without_keepalive    1.000 goroutines/stream  1713 heapB/stream  2998 stackB/stream
```

The `—` rows have no `before` because they measure this package's own
lifecycle, which this change did not touch; they are new numbers rather than
improved ones, and they are the ones an operator actually needs.

## The encoder — moved here from `internal/core/net` (ADR 0160 §4)

`AppendEvent` and `AppendComment` (`frame.go`) write an event's wire form; they
were `SSEEventValue.AppendTo` and `AppendSSEComment` in `internal/core/net`
until writing a wire format was recognised as a mechanism rather than the
domain's contract. Their benchmarks moved with them (`frame_bench_test.go`),
and so did the measurements below. The value and `Validate` stayed in the
core, with `Validate`'s one benchmark.

### SSE: 91 % of encoding a frame was one standard-library call

An event stream's per-byte work is finding the line terminators in `Data`,
because the SSE format has no escape: a terminator SPLITS the payload into
another `data:` line. `cutSSELine` asked `strings.IndexAny(s, "\n\r")` for the
first one, which is the obvious call and reads like the cheap one.

It is not. `IndexAny` has no `bytealg` path: for `len(s) > 8` it builds a
32-byte ASCII set **per call** and then walks the string one byte at a time
through `asciiSet.contains`; for `len(s) <= 8` it decodes a RUNE per byte and
calls `IndexRune` on each. `strings.IndexByte` is the assembly-backed
primitive — measured over 64 KiB it is ~30 GB/s per pass against `IndexAny`'s
2.4 — and the common SSE event, a single-line JSON payload with no terminator
anywhere in it, pays a full scan of the whole payload at the slow rate purely to
prove the absence.

The CPU profile of one 4 KiB single-line frame, before, verbatim — taken when
the encoder lived in `internal/core/net`, which is the package path the frames
below still name:

```
Showing nodes accounting for 3.57s, 100% of 3.57s total
      flat  flat%   sum%        cum   cum%
     2.77s 77.59% 77.59%      2.77s 77.59%  strings.(*asciiSet).contains (inline)
     0.51s 14.29% 91.88%      3.28s 91.88%  strings.IndexAny
     0.26s  7.28% 99.16%      0.26s  7.28%  runtime.memmove
     0.02s  0.56% 99.72%      0.28s  7.84%  github.com/kitsunium/sdk/internal/core/net.appendSSEField (inline)
     0.01s  0.28%   100%      3.57s   100%  github.com/kitsunium/sdk/internal/core/net.AppendEvent
         0     0%   100%      3.53s 98.88%  github.com/kitsunium/sdk/internal/core/net.appendSSEData
         0     0%   100%      3.25s 91.04%  github.com/kitsunium/sdk/internal/core/net.cutSSELine
```

`runtime.memmove` — copying the payload, which is the only work the function
actually has to do — is **7.28 %**. The scan is 91 %.

And after, on the same benchmark:

```
Showing nodes accounting for 3.61s, 99.72% of 3.62s total
      flat  flat%   sum%        cum   cum%
     2.36s 65.19% 65.19%      2.36s 65.19%  indexbytebody
     1.02s 28.18% 93.37%      1.02s 28.18%  runtime.memmove
     0.08s  2.21% 95.58%      3.54s 97.79%  github.com/kitsunium/sdk/internal/core/net.appendSSEData
     0.04s  1.10% 96.69%      0.04s  1.10%  internal/bytealg.IndexByteString
     0.03s  0.83% 97.51%      1.05s 29.01%  github.com/kitsunium/sdk/internal/core/net.appendSSEField (inline)
     0.02s  0.55% 98.07%      0.02s  0.55%  github.com/kitsunium/sdk/internal/core/net.validateSSELine
```

The scan is still the largest entry, and now it should be: two assembly passes
over the payload is the floor for proving two bytes are absent from it. The
copy went from 7 % to 28 % of a function that got 4.5× faster, which is the
same statement.

### Encoding a frame: 1.6× to 4.6×, and where each number comes from

Medians of three, `-benchtime=1s`. Zero allocations on every row, before and
after — this was never an allocation problem, which is exactly why nothing in
the tree had noticed it.

| `AppendEvent` | before | after | | after |
|---|---:|---:|---:|---:|
| single-line, 64 B | 104.5 ns | **40.80 ns** | **2.56×** | 1 569 MB/s |
| single-line, 256 B | 193.1 ns | **57.85 ns** | **3.34×** | 4 425 MB/s |
| single-line, 1 KiB | 538.8 ns | **147.1 ns** | **3.66×** | 6 961 MB/s |
| single-line, 4 KiB | 1 945 ns | **430.8 ns** | **4.51×** | 9 507 MB/s |
| single-line, 64 KiB | 30 397 ns | **6 582 ns** | **4.62×** | 9 956 MB/s |
| LF every 64 B, 4 KiB | 5 195 ns | **1 624 ns** | 3.20× | 2 522 MB/s |
| LF every 64 B, 64 KiB | 81 836 ns | **25 122 ns** | 3.26× | 2 609 MB/s |
| CRLF every 64 B, 4 KiB | 5 238 ns | **2 153 ns** | 2.43× | 1 903 MB/s |
| CRLF every 64 B, 64 KiB | 81 465 ns | **33 653 ns** | 2.42× | 1 947 MB/s |
| id + event + 256 B payload | 276.6 ns | **98.32 ns** | **2.81×** | — |
| `AppendComment` (keep-alive) | 31.85 ns | **19.99 ns** | 1.59× | — |

The single-line rows are the ones that matter: a payload with no terminator is
the overwhelmingly common event, and it is also the worst case for the scan,
which cannot stop early. A multi-line payload gains less because the CRLF rows
refresh both cursors on every line — see the next section.

The arithmetic cross-checks against the isolated scan rows below. At 64 KiB the
scan alone is 4 329 ns and the profile puts `memmove` at 28 % of 6 582, i.e.
≈ 1 840 ns; 4 329 + 1 840 = 6 169 against 6 582 measured, a 6 % gap that is the
field framing. At 4 KiB: 296.3 + ≈ 120 = 416 against 430.8, a 3 % gap.

`Validate` — which `AppendEvent` runs on every frame before it touches the
buffer — moved too, by 1.72×–1.73×, for a second, smaller reason; it is the
core's method, and `internal/core/net/BENCH.md` keeps that measurement.

### Two obvious scans are quadratic, in mirror-image halves

`IndexAny` finds the first of two bytes in one pass. `IndexByte` finds one byte,
so replacing it means two calls — and where those two calls go decides whether
the walk stays linear.

`cutSSELine` used to be called once per LINE, over the remaining payload. Two
unbounded `IndexByte` calls per line means the scan for a byte that is **not in
the payload at all** re-reads the whole tail on every line. Bounding the CR scan
by where the LF was found fixes the LF-terminated payload and leaves the exact
mirror broken, because now it is the LF scan that is unbounded when there is no
LF. Both were measured before either was believed:

| 64 KiB payload, whole-payload split | no terminator | LF every 64 B | CRLF every 64 B | CR every 64 B |
|---|---:|---:|---:|---:|
| `index_any` (the form replaced) | 27 737 ns | 67 534 ns | 67 210 ns | 69 034 ns |
| `two_index_byte` | 4 346 ns | **1 115 791 ns** | 20 920 ns | **1 109 329 ns** |
| `bounded_index_byte` | 4 347 ns | 18 601 ns | 28 181 ns | **1 114 228 ns** |
| `cursor` (shipped) | **4 329 ns** | **19 730 ns** | **27 311 ns** | **19 715 ns** |

The three bold blow-ups are 16× SLOWER than the code being replaced, on inputs
a caller supplies. `Data` is application data, so "a payload of LF-terminated
lines" is a log tail and "a payload of CR-terminated lines" is something a
stranger can send; neither is exotic and both are quadratic.

The shipped form keeps a cursor per terminator byte over the whole payload and
only ever moves each one FORWARD, re-scanning a cursor only when the cut just
made consumed or overtook it. Each byte is therefore examined at most once by
each of the two searches — two linear passes, whatever the shape:

| whole-payload split, `cursor` vs `index_any` | 64 B | 256 B | 1 KiB | 4 KiB | 64 KiB |
|---|---:|---:|---:|---:|---:|
| no terminator | 3.76× | 4.92× | 5.98× | 6.00× | **6.41×** |
| LF every 64 B | 2.68× | 3.22× | 3.20× | 3.35× | 3.42× |
| CRLF every 64 B | 2.14× | 2.31× | 2.33× | 2.46× | 2.46× |
| CR every 64 B | 2.64× | 3.18× | 3.35× | 3.38× | 3.50× |

It costs about **3 % more than the two naive forms on the common case** (18.67
against 18.08 ns at 64 B; at 64 KiB it is 4 329 against 4 346, i.e. inside the
noise), and that is the whole price of not having a quadratic corpus. CRLF is
the slowest shape because a CRLF cut consumes BOTH cursors and therefore
refreshes both — two `IndexByte` calls per line instead of one. It is still
linear; the row is there so nobody reads 2.46× as a defect.

Equivalence is not assumed. `TestSSELineSplitStrategiesAgreeExhaustively`
judges all four strategies against the `index_any` oracle over every string of
length 0 to 9 in `{'a', '\n', '\r'}` — 29 524 payloads, which is every
arrangement of LF, CR, CRLF, LFCR, a leading terminator, a trailing one and a
run of them — plus hand-written multi-byte cases, and the bound is 9 rather
than 8 because `IndexAny` itself changes strategy at `len(s) > 8`.
`TestAppendEventMatchesTheIndexAnyOracle` then re-encodes the same corpus from the
ORACLE's lines and requires byte-identical frames, because agreeing on a split
proves nothing if the encoder does not use that split.

### What was refused

- **A single pass that finds either byte.** There is no `bytealg` primitive for
  "first of two bytes", and a hand-written SWAR pass in pure Go would be
  competing with assembly that already runs at 15 GB/s. Two passes at that rate
  beat one pass at 2.2.
- **Skipping the scan when `Data` is known to be single-line.** Proving the
  absence IS the scan; there is nothing cheaper to check first.
- **Escaping a terminator instead of splitting on it.** The format has no
  escape (ADR 0029), and this is a benchmark file, not a licence to change the
  wire.

### Reproducibility envelope of the encoder rows

> **Numbers vary across machines**; the allocation column is exact, and what
> these rows assert is the **ratios**. Every published number is the median of
> three runs at `-benchtime=1s`. The encoder rows sit inside 6 % apart from
> `AppendComment` before (4.7 %) and `AppendEvent single_line/64 B` after
> (5.6 %), both of which are tens of nanoseconds where the timer's own
> resolution is a visible share.
>
> **One row-set was thrown away**, and the cause is worth recording because
> nothing about the numbers looked wrong: the "before" arm reported IDENTICAL
> medians to the "after" arm on all nineteen rows, 0.98×–1.02×. The backup the
> revert restored from had been taken AFTER the patch, so both arms ran the new
> code. It was caught by arithmetic and not by inspection — a 4.5× change had
> already been measured in a single-run pass, and a comparison that says 1.00×
> against a known 4.5× is reporting on the harness. The re-run restored the
> original from `git show HEAD:` instead of from a local copy.

| Dimension | Value |
|---|---|
| CPU cores          | 8 |
| RAM                | 15 GiB |
| OS / kernel        | Linux 6.12.101+deb13-amd64 |
| Architecture       | amd64 |
| Go toolchain       | go1.27.1 linux/amd64 |
| Git branch         | jaimerias-que-tu-te-connect |
| Git commit         | f2d263d |
| Generated (UTC)    | 2026-09-10 |
| Bench wall-clock   | `-test.benchtime=1s`, `-test.count=3`, medians |


### Results of the encoder benchmarks

Medians of three at `-benchtime=1s`. The `before` block is `git show
HEAD:internal/core/net/sse.go` restored into the tree and re-measured on the
same binary; the `after` block is what ships. Recorded on 2026-09-10 at
f2d263d, when the encoder lived in `internal/core/net` and the benchmarks were
`BenchmarkSSEAppendTo`, `BenchmarkSSEAppendToFullFrame` and
`BenchmarkAppendSSEComment`; they are `BenchmarkAppendEvent`,
`BenchmarkAppendEventFullFrame` and `BenchmarkAppendComment` in
`frame_bench_test.go` now, over the same code (ADR 0160 §4). The two
`BenchmarkSSEValidate` rows stayed with `Validate`, in
`internal/core/net/BENCH.md`.

```
goos: linux
goarch: amd64
pkg: github.com/kitsunium/sdk/internal/core/net
cpu: AMD EPYC 7351P 16-Core Processor
                                            BEFORE          AFTER
BenchmarkSSEAppendTo/single_line/0000064      104.5 ns/op    40.80 ns/op   0 B/op  0 allocs/op
BenchmarkSSEAppendTo/single_line/0000256      193.1 ns/op    57.85 ns/op   0 B/op  0 allocs/op
BenchmarkSSEAppendTo/single_line/0001024      538.8 ns/op    147.1 ns/op   0 B/op  0 allocs/op
BenchmarkSSEAppendTo/single_line/0004096       1945 ns/op    430.8 ns/op   0 B/op  0 allocs/op
BenchmarkSSEAppendTo/single_line/0065536      30397 ns/op     6582 ns/op   0 B/op  0 allocs/op
BenchmarkSSEAppendTo/multi_line_lf/0000064    137.6 ns/op    57.99 ns/op   0 B/op  0 allocs/op
BenchmarkSSEAppendTo/multi_line_lf/0000256    378.2 ns/op    151.4 ns/op   0 B/op  0 allocs/op
BenchmarkSSEAppendTo/multi_line_lf/0001024     1357 ns/op    441.7 ns/op   0 B/op  0 allocs/op
BenchmarkSSEAppendTo/multi_line_lf/0004096     5195 ns/op     1624 ns/op   0 B/op  0 allocs/op
BenchmarkSSEAppendTo/multi_line_lf/0065536    81836 ns/op    25122 ns/op   0 B/op  0 allocs/op
BenchmarkSSEAppendTo/multi_line_crlf/0000064  136.9 ns/op    66.08 ns/op   0 B/op  0 allocs/op
BenchmarkSSEAppendTo/multi_line_crlf/0000256  360.7 ns/op    190.6 ns/op   0 B/op  0 allocs/op
BenchmarkSSEAppendTo/multi_line_crlf/0001024   1336 ns/op    589.7 ns/op   0 B/op  0 allocs/op
BenchmarkSSEAppendTo/multi_line_crlf/0004096   5238 ns/op     2153 ns/op   0 B/op  0 allocs/op
BenchmarkSSEAppendTo/multi_line_crlf/0065536  81465 ns/op    33653 ns/op   0 B/op  0 allocs/op
BenchmarkSSEAppendToFullFrame                 276.6 ns/op    98.32 ns/op   0 B/op  0 allocs/op
BenchmarkAppendSSEComment                     31.85 ns/op    19.99 ns/op   0 B/op  0 allocs/op
```

The whole-payload line-split sweep — four strategies × four corpus shapes ×
five sizes, 80 rows, all zero-allocation — is summarised in §Two obvious scans
are quadratic. The 64 KiB column is reproduced there in full; the smaller sizes
scale linearly for every strategy except the three diagnosed blow-ups, which
scale with the SQUARE of the payload and are the reason the sweep exists.
