<!-- updated: 2026-09-09T00:00:00Z -->
# pkg/v1/server/sse/

## Purpose

Public façade for Server-Sent Events, server side (ADR 0029). Aliases onto
`internal/core/net` and `internal/service/net/sse` plus thin forwarding
constructors. No logic lives here.

It is a subpackage of `pkg/v1/server` rather than a set of `SSE`-prefixed
symbols on it: `sse.New(w, r)` / `sse.Event{…}` / `sse.KeepAlive(d)` read the
way the format is discussed, and the package stands alone for a consumer using
plain `net/http`.

## Surface

| Symbol | Role |
|---|---|
| `New` | starts a stream on a `ResponseWriter`; writes and flushes the headers |
| `Stream` | the open stream — `Send`, `Comment`, `Done`, `LastEventID`, `Close` |
| `Event` | one frame — `ID`, `Name`, `Data`, `Retry` |
| `Option`, `KeepAlive`, `WithoutKeepAlive`, `WriteTimeout`, `Retry` | stream options |
| `ContentType`, `LastEventIDHeader`, `MinRetry`, `DefaultKeepAlive`, `DefaultWriteTimeout` | the format's and the domain's constants |
| `DrainSignal` | the shutdown signal a long-lived handler watches |
| `FieldInvalid`, `FlushUnsupported`, `StreamClosed`, `StreamMisconfigured` | sentinels |

`server.DrainSignal` is the same function, re-exported from the parent so a
handler that does not stream need not import this package to observe a drain.

## Why-this-shape

- **`Done()` and the `Send` refusal are both load-bearing.** A handler that
  selects on `Done()` stops between events; a handler that only loops on `Send`
  stops on the next one. Either alone leaves a shape that can hold a graceful
  shutdown open until its budget expires.
- **A newline is not an error and not an escape — it is a split.** `Event.Data`
  becomes one `data:` line per line and the client rejoins them with `"\n"`.
  The same absence of escaping is why a terminator in `ID` or `Name` is
  *refused*: a silently truncated id is a resume token pointing at the wrong
  place.
- **An `Event` with a `Name` and no `Data` is refused.** Every client discards a
  frame whose data buffer is empty — and discards the event type with it — so
  the caller's "ping" event would never arrive. Use `Comment` for a
  payload-free heartbeat.
- **`Retry` below one millisecond is refused, not rounded.** The wire field is
  an integer millisecond count; `retry: 0` does not mean "very soon", it tells
  the client to reconnect immediately, which turns a small backoff into a hot
  loop against a server that is probably already struggling.
- **Zero keep-alive is clamped, negative is refused** (ADR 0031), and "never"
  has its own spelling. `TestKeepAliveIsNeverSilentlyNever` pins it at the
  public edge.
- **The sentinels are the engine's own values, not copies.**
  `TestSentinelsAreMatchable` pins that `errors.Is` still answers across the
  module boundary; re-declared sentinels would quietly answer false.

## The client side is NOT here, and that is a decision

There is no SSE **client** in this SDK, and adding one is not a matter of
writing a parser. `internal/core/net/response.go` documents the outbound
contract:

```go
// Body is the fully-read response body, already bounded by the client's
// size cap. It is nil for a status that carries no body, such as 204.
Body []byte
```

`ResponseValue.Body` is a `[]byte` that the client has already read to
completion and closed. The type comment gives two reasons, and both are
deliberate: a returned `*http.Response` makes the caller responsible for closing
the body — one forgotten `Close` leaks a connection — and it makes an accurate
byte count impossible, because a body's size is only known once it has been
read, so an observation hook firing at header time can never report it.

An event stream has no completion and no size. Consuming one means holding an
open `io.ReadCloser`, which is exactly the shape that contract exists to refuse.
Making it fit would mean one of:

- widening `ResponseValue` with a streaming body, which reintroduces the leak
  and breaks the byte count for **every** caller, streaming or not; or
- a second entry point on the client that returns a reader, i.e. a second,
  weaker contract living beside the first — the thing `response.go` was written
  to avoid.

Either is a change to a published port's shape and to the outbound domain's
central promise, so it is a `pkg/v1` decision of its own, with its own ADR,
taken deliberately — not a side effect of shipping the server half. The server
half is complete and useful on its own: the consumer of an SSE endpoint is
overwhelmingly a browser's `EventSource`, which needs nothing from this SDK.

## Do NOT

- Hand-edit `README.md` — it is generated from the package doc comment
  (`cd pkg/v1 && go generate ./server/...`, or `make docs-readme`).
- Add logic here; it belongs in `internal/service/net/sse`.
- Retain a `Stream` past the handler that created it — `net/http` recycles the
  response the moment `ServeHTTP` returns.
- Add an SSE client without the decision above being taken first.

## Verification

```
bazel test --config=race //pkg/v1/server/sse:sse_test
# Fallback:
cd pkg && GOWORK=off go test -race -cover ./v1/server/...
```

## Reference

- ADR 0029 — `docs/adr/0029-sdk-net-domain.md`
- ADR 0030 (stdout is a protocol channel), ADR 0031 (zero values are never inert)
- `internal/service/net/sse/CLAUDE.md`, `internal/core/net/CLAUDE.md`
- The outbound contract this cites — `internal/core/net/response.go`
