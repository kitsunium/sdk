# internal/service/net/sse — notes

Reference moved out of `CLAUDE.md` (ADR 0253), unchanged and under the same section titles.

## On a loop group (#858)

`NewLoopHandler` is the event stream on the loop engine: no goroutine a stream, the
keep-alive comments on the loop's timer, the drain's `retry:` field the last thing
a client reads. It is a `corenet.HTTP1Handler` and `corenet.HTTP1Streamer`, handed to
`http1.HandleLoop` by the program; it does not import `http1` (no member of
`service/net` imports another). The head, the frames and the comment are what
`Stream` writes on the goroutine engine, so a client cannot tell the two apart.
`KeepAlive`, `WithoutKeepAlive` and `Retry` mean what they mean there; `WriteTimeout`
is refused: the loop's `OutputCap` is what bounds a stream that stopped reading.

## Contents

| File | Surface |
|---|---|
| `sse.go` | `Stream` — `New`, `Send`, `Comment`, `Done`, `LastEventID`, `Close`; the keep-alive and watcher goroutines; the flush probe; `retain` and the two constants that bound the encode buffer |
| `loop.go` | #858: `NewLoopHandler`, `LoopHandler` (the `HTTP1Handler` and `HTTP1Streamer` the http1 adapter serves), `LoopStreamValue`, and the port `LoopSource` in `gen_ports.go`; the lane's frame buffer |
| `options.go` | `Option` — `KeepAlive`, `WithoutKeepAlive`, `WriteTimeout`, `Retry`; `resolve` and the ADR 0031 clamp/refuse split |
| `frame.go` | the encoder — `AppendEvent` (an event's wire form; `pkg/v1/net/sse.AppendEvent` forwards to it) and `AppendComment` (the keep-alive frame), the one-pass terminator scan (`appendData` and its two forward cursors), `validateComment` |
| `frame_external_test.go`, `frame_bench_test.go` | the encoder's tests and benchmarks, including the exhaustive equivalence corpus judged against `splitIndexAny`, the oracle |
| `sse_alloc_internal_test.go` | the malloc-total gates under the "a steady-state send allocates nothing" claim (deliberately NOT `testing.AllocsPerRun` — its integer division reports 0.0 for anything allocating less than once per call) — `//go:build !race`, so the race-off alloc lane is its ONLY lane (see §Verification) |
| `allocwindow_test.go` | `mallocsOver`, the allocation window the gates count with: the allocations made on the window's own call stack, never the process's, and its negative control — one copy of the SDK's instrument, held identical to the others by `check-alloc-lane-coverage.sh` |
| `loop_external_test.go`, `loop_engine_linux_test.go`, `loop_memory_linux_test.go` | #858: the head and frames over the http1 adapter on a fake loop; on epoll, io_uring and the portable backend, 1 000 events to a net/http client and a drain's retry field; (race off) 10 000 streams under 512 B of Go heap each and every one drained |
| `BENCH.md` | what a stream costs open, per frame, and to drain — see §Cost |

The frame as a value — `corenet.SSEEventValue` and its `Validate`, the rules a
frame must satisfy to be carried at all — lives in `internal/core/net/sse.go`.
Its wire form is a mechanism, and since ADR 0160 §4 it is this package's
(`frame.go`), moved with its tests and benchmarks: the stream and the bytes it
writes are both here, and the core keeps what a second implementation would
share.

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

## The loop's stream

A stream on the loop answers with a response delimited by close (RFC 9112 §6.3
item 8): `Connection: close`, no `Content-Length`, no chunk, so ADR 0186 §D6's
refusal of every transfer coding stands. The handler then writes through
`corenet.HTTP1StreamValue`, the `LoopIO`'s own copying `Write`: a slow reader meets
`LoopOutputFull`, past which `LoopStreamValue.Send` returns the refusal and the keep-alive
ends the stream.

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
