<!-- updated: 2026-10-03T12:00:00Z -->
# internal/service/net/sse/

The reference half of this file moved, unchanged and under the same section titles, to
`internal/service/net/sse/NOTES.md` (ADR 0253): read the section a change touches.
It holds:
§On a loop group (#858); §Contents; §Why-this-shape; §The loop's stream; §Last-Event-ID.

## Purpose

The server side of Server-Sent Events (ADR 0029): an HTTP response held open and
written one `text/event-stream` frame at a time, flushed after each.

Public façade: `pkg/v1/net/sse`.

It is written against `net/http`'s own interfaces — `http.ResponseWriter`,
`*http.Request`, `http.ResponseController` — not against the SDK's listener
engine, so it works inside any `http.Handler`. Mounted on the engine
(`Group.HandleHTTP`) it additionally observes the drain signal the adapter
publishes, which is the only reason a graceful shutdown of a streaming server
finishes in milliseconds instead of burning its whole budget — **1.808 ms for
64 open streams, against 300 ms of budget spent in full when the signal is not
published**, measured in `BENCH.md` and gated by
`TestDrainWithOpenStreamsFinishesInMilliseconds`, which runs both.

## Concurrency

`Stream` is safe for concurrent use, and that is a requirement rather than a
courtesy: the keep-alive writes from its own goroutine while the handler writes
from another, and a handler fanning events in from several producers is the
normal shape. `mu` serialises whole frames — two interleaved frames are not two
events, they are one corrupt one, and
`TestConcurrentSendsStayWholeFrames` is the guard.

Two goroutines per stream, both owned by the `Stream` and both joined by
`Close`:

- the **watcher**, which turns `r.Context().Done()` and the drain signal into
  `done`;
- the **keep-alive**, which is not started at all when it is disabled. It is
  a `worker.Every` loop: it ticks on the stream's clock (`clock.System`; only
  `Test_Stream_keepAliveOnInjectedClock` hands it a `ManualClock`, through the
  unexported option field — there is no public clock option) and ends on
  `done` through `worker.WithDone`, so it stops at the stream's own end.

`end()` (close `done` once) is separate from `Close()` (end, then join) on
purpose: the keep-alive goroutine calls `end` when its own write fails, and
calling `Close` there would make it join itself.

**The two goroutines are not merged, and the reason is measured on both sides.**
Merging them would save one `worker.LoopDaemon`, its channels, its ticker and
the tick closure — 9 allocations, about 610 B of heap and 2 687 B of stack per
stream, or 34.1 MB per 10 000 streams, which is 42 % of what an idle stream costs. It is
refused because the keep-alive calls `Comment`, which takes `mu`: a merged
goroutine parked on that mutex behind a slow `Send` is not in its `select`, so
it does not observe the drain — for up to `WriteTimeout`, ten seconds by
default. That is ADR 0043's "one open stream burns the whole budget",
re-introduced in a timing-dependent form no CI run would catch. `BENCH.md`
records the number so the next attempt starts from it rather than from an
estimate.

## Cost

Full numbers, both benchmark harnesses and the rejected optimisations are in
`BENCH.md`. The four facts that decide how this package is used:

| | |
|---|---|
| one open stream | **2 goroutines, 8 112 B** (2 427 heap + 5 685 stack) |
| `WithoutKeepAlive()` | 1 goroutine, 4 711 B |
| `Send`, 256 B, on a socket | 189.1 ns, **0 allocs** |
| `Comment` (keep-alive), on a socket | 138.8 ns, **0 allocs** |
| drain → `Shutdown` returns, 64 streams | **1.808 ms** |

- **An SSE server is sized by open streams, not by event rate.** 10 000
  concurrent streams is **20 000 goroutines and 81.2 MB** — 24.3 MB of heap and
  56.9 MB of goroutine stacks. The stack half never appears in a `B/op` column,
  so a benchmark reporting only allocations understates it by the larger of the
  two numbers. `WithoutKeepAlive()` halves both.
- **A benchmark built on `httptest.ResponseRecorder` measures the wrong path.**
  The recorder — and this package's own `capture` double — implements `Flush`
  but not `SetWriteDeadline`, so `deadlines` is false and the per-frame deadline
  refresh never runs. It understates a 64-byte send by **2.11×** and the
  keep-alive comment by **2.52×**. `BENCH.md` publishes both harnesses' rows.
- **The deadline refresh is the CLOCK, not the `ResponseController` walk.**
  `time.Now()` is **41.32 %** of a 64-byte send on this host, whose clock source
  is `kvm-clock`. Nothing was done about it: refreshing less often than per
  frame breaks the contract the deadline exists for, and there is no coarse
  monotonic clock in the standard library.
- **An SSE newline SPLITS rather than escapes.** The format has no escape
  mechanism, so a terminator inside `Data` becomes another `data:` line and the
  client rejoins them with `"\n"` — that is what makes a multi-line payload
  expressible. CRLF, CR and LF are all recognised, and all three normalise to
  LF on reassembly: the VALUE round-trips, its byte spelling does not. A
  terminator inside a comment is refused, as one inside an id or a name is by
  `Validate`, because a comment is a single line.
- **The terminator scan keeps a cursor per byte and never rescans.** Finding
  the terminators in `Data` is the only per-byte work an event stream does —
  and `strings.IndexAny`, the obvious call, has no `bytealg` path; a profile
  put it at **91 %** of encoding a 4 KiB single-line frame. `strings.IndexByte`
  is the assembly-backed primitive, but it finds ONE byte, and the two obvious
  ways to call it twice are each quadratic on half the possible payloads —
  both measured at **16× SLOWER** than the code they replaced. `appendData`
  finds both terminators once over the whole payload and moves each cursor
  only FORWARD: **2.1×–6.4×** on the split, and `BENCH.md` §The encoder prints
  the profile, all four strategies and the corpus that exposes each blow-up.
- **A frame is 1.97×–4.60× cheaper than it was**, in situ, from the terminator
  scan (which was 91 % of encoding a frame, then in `internal/core/net`).
  The isolated encoder win is 2.56×–4.62×; the in-situ number is lower at small
  sizes because the stream's own fixed cost — mutex, terminal check, write,
  flush, and on a socket the clock read — does not shrink.

## Error range

None of its own. Every failure is an `internal/core/net` sentinel
(`0.2.11.23` – `0.2.11.26`), wrapped with the offending option or field. Per
ADR 0029 the service layer declares **no** codes.

## Do NOT

- Cancel the request context to signal a drain. It would tell every handler to
  abandon the response it is halfway through, which is the opposite of what a
  drain is for. The signal is a context VALUE (`corenet.DrainSignal`) precisely
  so it is additive.
- Call `ResponseController.Flush` to test whether flushing is supported — it
  commits the response.
- Escape a newline in `Data`. The format has no escape; a terminator SPLITS the
  value into another `data:` line, which is what makes a multi-line payload
  expressible at all.
- Rewrite `appendData`'s two cursors as an `IndexByte` pair per line. It reads
  as the same thing and is quadratic — in one of two mirror-image halves
  depending on which scan is left unbounded, both measured at 16× slower than
  the `IndexAny` form they would replace. The forward-only refresh IS the
  algorithm, not an optimisation layered on it.
- Delete `splitIndexAny` from `frame_bench_test.go`, or "simplify" it to call
  `appendData`. It is the terminator scan shipped before the campaign,
  transcribed, and the oracle both equivalence tests are judged against — over
  every string of length 0 to 9 in `{'a', '\n', '\r'}`; the bound is 9 because
  `strings.IndexAny` itself changes strategy at `len(s) > 8`.
- Add a replay buffer. See §Last-Event-ID.
- Write anything to stdout (ADR 0030). The only output is the response.
- Buffer or batch frames to amortise the flush. The flush IS the protocol: an
  unflushed frame sits in the transport buffer until the handler returns, which
  on an endless stream is never. A buffered stream passes every test that does
  not assert on timing, and is broken.
- Merge the watcher and the keep-alive goroutines without answering what happens
  when the merged goroutine is parked on `mu` inside `Comment`. See §Concurrency.
- Turn `retain`'s ceiling into a plain size cap. Measured at 3.89× on a stream
  whose every frame is outsized; both clauses of the condition are gated and
  mutation-checked.
- Benchmark this package on `httptest.ResponseRecorder` alone. It cannot set a
  write deadline, so it silently skips the path production takes on every frame.

## Verification

```
# Primary (Bazel)
bazel test --config=race //internal/service/net/sse:sse_test

# The allocation gates, which the race suite CANNOT run: sse_alloc_internal_test.go
# is //go:build !race, because the race detector allocates shadow state on every
# memory access and the allocation total would be measuring the detector. Its only lane is
# the race-off one, and //internal/service/net/sse:sse_test is listed in
# tools/alloc-lane-targets.txt for exactly that reason (SDK-wide rule 12).
make test-alloc

# Fallback (go test — quick local iteration)
cd internal/service && GOWORK=off go test -race -cover ./net/sse/...

# The benchmarks behind BENCH.md and §Cost
cd internal/service && GOWORK=off go test -run='^$' -bench=. -benchmem -benchtime=1s -count=3 ./net/sse/
```

## Reference

- ADR 0029 — `docs/adr/0029-sdk-net-domain.md`
- ADR 0030 (stdout is a protocol channel), ADR 0031 (zero values are never inert)
- Contract layer — `internal/core/net/CLAUDE.md`
- The drain signal's publisher — `internal/service/net/server/CLAUDE.md`
