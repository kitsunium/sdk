<!-- generated from internal/service/net/sse/sse_bench_test.go — run `cd internal/service && GOWORK=off go test -run='^$' -bench=. -benchmem -benchtime=1s -count=3 ./net/sse/` to refresh; every published number is the MEDIAN of the runs stated per section -->
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
| `New` + `Close`, with keep-alive | 9 235 | 2 481 | **29** |
| `New` + `Close`, `WithoutKeepAlive()` | 4 988 | 1 888 | **21** |

Twenty-nine allocations, not the fifteen a reading of the code suggests, and
2 481 B allocated against 2 427 B still live afterwards — so essentially nothing
about opening a stream is transient. The ns/op is bounded below by two scheduler
round trips, because `Close` JOINS both goroutines; it is not a measurement of
this package's code so much as of the runtime's hand-off.

### The second goroutine, and why it is still there

The delta between the two rows is exactly one `worker.LoopDaemon`, its two
channels and a `time.Ticker`: **8 allocations, 594 B of heap, 2 687 B of stack
and 4 247 ns**. Merging the watcher into the keep-alive would recover all of it
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

`internal/core/net`'s terminator scan was 91 % of encoding a frame and is now
two assembly passes (see that package's `BENCH.md`). Isolated, `AppendTo` got
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
`AllocsPerRun` gates in `sse_alloc_internal_test.go`, which is `//go:build !race`
because the race detector allocates shadow state on every memory access; the
race-off alloc lane is its only gate, and `//internal/service/net/sse:sse_test`
is in `tools/alloc-lane-targets.txt` for that reason (SDK-wide rule 12).

## Draining: 1.8 ms, against a guard that would have accepted 3 seconds

This package's `CLAUDE.md` says a graceful shutdown "finishes in milliseconds";
ADR 0043 says "40 ms, clean". The only executable statement anywhere was
`pkg/v1/server`'s `TestHTTPAdapterDrainsOnShutdown`, which fails at **3 s** with
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
| Git commit         | a486bb3 |
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
BenchmarkNewClose/with_keepalive               —      9235 ns/op   2481 B/op   29 allocs/op
BenchmarkNewClose/without_keepalive            —      4988 ns/op   1888 B/op   21 allocs/op
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
