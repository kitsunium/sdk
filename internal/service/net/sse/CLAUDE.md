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
finishes in milliseconds instead of burning its whole budget.

## Contents

| File | Surface |
|---|---|
| `sse.go` | `Stream` — `New`, `Send`, `Comment`, `Done`, `LastEventID`, `Close`; the keep-alive and watcher goroutines; the flush probe |
| `options.go` | `Option` — `KeepAlive`, `WithoutKeepAlive`, `WriteTimeout`, `Retry`; `resolve` and the ADR 0031 clamp/refuse split |

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

## Verification

```
# Primary (Bazel)
bazel test --config=race //internal/service/net/sse:sse_test

# Fallback (go test — quick local iteration)
cd internal/service && GOWORK=off go test -race -cover ./net/sse/...
```

## Reference

- ADR 0029 — `docs/adr/0029-sdk-net-domain.md`
- ADR 0030 (stdout is a protocol channel), ADR 0031 (zero values are never inert)
- Contract layer — `internal/core/net/CLAUDE.md`
- The drain signal's publisher — `internal/service/net/server/CLAUDE.md`
