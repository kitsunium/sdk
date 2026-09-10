<!-- updated: 2026-09-09T00:00:00Z -->
# internal/service/net/sse/

## Purpose

The server side of Server-Sent Events (ADR 0029): an HTTP response held open and
written one `text/event-stream` frame at a time, flushed after each.

Public façade: `pkg/v1/server/sse`.

It is written against `net/http`'s own interfaces — `http.ResponseWriter`,
`*http.Request`, `http.ResponseController` — not against the SDK's listener
engine, so it works inside any `http.Handler`. Mounted on the engine
(`Group.HandleHTTP`) it additionally observes the drain signal the adapter
publishes, which is the only reason a graceful shutdown of a streaming server
finishes in milliseconds instead of burning its whole budget — **1.808 ms for
64 open streams, against 300 ms of budget spent in full when the signal is not
published**, measured in `BENCH.md` and gated by
`TestDrainWithOpenStreamsFinishesInMilliseconds`, which runs both.

## Contents

| File | Surface |
|---|---|
| `sse.go` | `Stream` — `New`, `Send`, `Comment`, `Done`, `LastEventID`, `Close`; the keep-alive and watcher goroutines; the flush probe; `retain` and the two constants that bound the encode buffer |
| `options.go` | `Option` — `KeepAlive`, `WithoutKeepAlive`, `WriteTimeout`, `Retry`; `resolve` and the ADR 0031 clamp/refuse split |
| `sse_alloc_internal_test.go` | the malloc-total gates under the "a steady-state send allocates nothing" claim (deliberately NOT `testing.AllocsPerRun` — its integer division reports 0.0 for anything allocating less than once per call) — `//go:build !race`, so the race-off alloc lane is its ONLY lane (see §Verification) |
| `BENCH.md` | what a stream costs open, per frame, and to drain — see §Cost |

The frame itself — `corenet.SSEEventValue`, its validation and its wire form —
lives in `internal/core/net/sse.go`. This package owns the *stream*; the core
owns the *format*.

## Why-this-shape

- **`Flush` is a precondition, not an optimisation.** A stream that cannot flush
  is not a slow stream: every frame sits in the transport buffer until the
  handler returns, and an event-stream handler by nature does not return. The
  client waits forever on a connection the server believes is working. `New`
  therefore refuses up front — `SSE_FLUSH_UNSUPPORTED`.
- **`canFlush` walks the unwrap chain by hand rather than calling `Flush`.**
  `http.ResponseController` answers the question only by *answering* it: calling
  `Flush` on a supported writer commits a `200` with no content type. The probe
  has to be side-effect free, so it mirrors net/http's own two forms
  (`FlushError`, then `http.Flusher`) plus `Unwrap() http.ResponseWriter`. A
  middleware that wraps the response to count bytes is the normal shape of an
  HTTP stack, so the chain walk is not optional —
  `TestNewAcceptsAFlusherBehindAWrapper` pins it.
- **The write deadline is per FRAME, never per response.** `http.Server`'s
  `WriteTimeout` is an absolute deadline for the whole response, so a group that
  sets one would cut every stream at exactly that instant, silently, and the
  client would reconnect forever in a loop the operator cannot see. The stream
  replaces it through `ResponseController.SetWriteDeadline`, refreshed on every
  frame — a stream has no total write budget by nature, but one write must still
  be bounded or a peer that stopped reading pins a goroutine.
- **A response that supports `Flush` but not `SetWriteDeadline` is served
  anyway.** `httptest.ResponseRecorder` is exactly that, and so is any
  hand-rolled writer. The streaming contract is `Flush`; deadlines are a bound
  the stream applies when it can and records that it cannot when it cannot.
  That tolerance had a cost nobody had noticed: because the recorder AND this
  package's own `capture` double both lack `SetWriteDeadline`, every test built
  a stream with `deadlines` false and **no test in the repository executed the
  per-frame refresh at all** — neither the refresh nor the `s.deadlines = false`
  degradation under it. `deadlineWriter` and its two tests close that, and the
  same gap is why `BENCH.md` publishes two harnesses rather than one.
- **Three endings, one observable outcome.** The client going away, the server
  draining, and `Close` all close `done`, and `Send`/`Comment` then refuse with
  `SSE_STREAM_CLOSED`. Both halves are load-bearing: `Done()` lets a
  well-written handler stop between events, and the `Send` refusal is the floor
  under a handler that only ever loops on `Send`. Without the second, one
  handler shape could still hold a drain open to its budget.
- **`Send` refuses once draining — there is no farewell frame.** The alternative
  was to keep the stream writable after the signal so a handler could send a
  last event; that reopens the hole the signal exists to close, because a
  handler is then free to keep sending. The client's reconnect delay is the
  right place for that intent, and `Retry` sets it once at the top of the
  stream.
- **The encode buffer is reused for the stream's life, and that needed a
  bound.** A `Stream` keeps one buffer and appends every frame into it, which is
  what makes a steady-state send allocate nothing — measured, and now gated
  (`TestSteadyStateSendAllocatesNothing`), because until 2026-09 the claim was
  prose with no executable statement anywhere in the repo. The catch is that
  "for the stream's life" is literal: one outsized event pinned **1 056 768
  bytes per stream, forever**, which on 10 000 streams is 10.6 GB bought by
  something that happened once. Nothing could see it, because `B/op` counts
  bytes ALLOCATED and this is bytes still HELD — the allocation happened once
  and legitimately. `retain` bounds it, and the SHAPE of the bound is the
  decision: a plain "release anything above 64 KiB" was written first and
  measured **3.89× slower** on a stream whose every frame is outsized, because
  it re-grew the buffer on every send. What ships releases only when the buffer
  is above the ceiling AND the last frame used less than half of it, so an
  oversized buffer is kept while the frames still USE it and given back on the
  first one that does not. Both halves have their own gate.
- **Keep-alive is a COMMENT, not an event.** A comment is ignored by every
  client by construction, so it can never be mistaken for a payload by a
  consumer that forgot to filter it. An `event:` with no `data:` would not work
  as a substitute — see the core's refusal below.
- **A zero keep-alive interval is clamped; a negative one is refused**
  (ADR 0031). A stream with keep-alive silently disabled works perfectly on a
  developer's loopback and dies at one minute behind a real proxy, which is the
  worst possible place to learn it — so zero gets `DefaultKeepAlive`. "Never"
  has its own spelling, `WithoutKeepAlive()`, and a negative interval is neither
  of those. `Test_resolve` pins both halves, because neither is observable from
  outside the package without waiting fifteen seconds.
- **`Connection` is deliberately not set.** It is hop-by-hop, net/http manages
  it for HTTP/1.1, and it is forbidden outright over HTTP/2 — a stream that set
  it would be broken on the transport most likely to carry it.
  `X-Accel-Buffering: no` *is* set, because nginx buffers proxied responses by
  default and turns a real-time stream into a batch delivered at close. Both it
  and `Cache-Control` are set only when the caller left them empty;
  `Content-Type` is imposed, because the media type *is* the protocol handshake.

## Last-Event-ID

`Stream.LastEventID()` returns the request header. **Nothing is replayed from
it**, and the shape of the problem is why rather than a matter of scope:

- A replay buffer held by the stream is empty at exactly the moment a resume
  needs it. A reconnect is a NEW connection, therefore a new `Stream`, therefore
  a fresh buffer — it can never contain the events the reconnecting client
  missed.
- A buffer that outlived the stream would be an application store. It has to
  know how many events to keep, how long they stay valid, and whether replaying
  one is even safe — "your balance changed" replayed is a lie. Those are
  questions about what the events MEAN, which the transport cannot answer.

So the cursor is handed to the handler, which resumes from its own log. Minting
ids is the handler's job for the same reason: an id the SDK invented would be a
number that means nothing to the application the client sends it back to. An
`Event` with no `ID` emits no `id:` field, which leaves the client's stored
cursor untouched — the format's own behaviour, not an omission.

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
- the **keep-alive**, which is not started at all when it is disabled.

`end()` (close `done` once) is separate from `Close()` (end, then join) on
purpose: the keep-alive goroutine calls `end` when its own write fails, and
calling `Close` there would make it join itself.

**The two goroutines are not merged, and the reason is measured on both sides.**
Merging them would save one `worker.LoopDaemon`, its channels and a
`time.Ticker` — 8 allocations, 594 B of heap and 2 687 B of stack per stream,
or 34.1 MB per 10 000 streams, which is 42 % of what an idle stream costs. It is
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
- **A frame is 1.97×–4.60× cheaper than it was**, in situ, from
  `internal/core/net`'s terminator scan (which was 91 % of encoding a frame).
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
