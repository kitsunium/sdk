<!-- updated: 2026-09-09T00:00:00Z -->
# pkg/v1/server/websocket/

## Purpose

Public façade for WebSocket, server side (RFC 6455 / ADR 0047). Aliases onto
`internal/core/net` and `internal/service/net/websocket` plus thin forwarding
constructors. No logic lives here.

It is a subpackage of `pkg/v1/server` rather than a set of `WS`-prefixed symbols
on it, for the same reason `sse/` is: `websocket.Upgrade(w, r)` /
`websocket.Message{…}` / `websocket.PingInterval(d)` read the way the protocol is
discussed, and the package stands alone for a consumer using plain `net/http`.

## Surface

| Symbol | Role |
|---|---|
| `Upgrade` | completes the RFC 6455 handshake; writes the HTTP refusal itself on failure |
| `Conn` | the open connection — `Receive`, `Send`, `SendText`, `SendBinary`, `Ping`, `Close`, `CloseWith`, `Done`, `Subprotocol`, `PeerCloseCode` |
| `Message` | one whole application message — `Binary`, `Data` |
| `CloseCode`, `Close*` | the §7.4.1 registry, plus `CloseCode.Sendable` / `Echoable` |
| `Option`, `Subprotocols`, `MaxMessageSize`, `MaxFrameSize`, `PingInterval`, `WithoutPing`, `WriteTimeout`, `AllowOrigins`, `AllowAnyOrigin` | connection options |
| `GUID`, `Version`, `MaxControlPayload`, `DefaultPingInterval`, `DefaultWriteTimeout`, `DefaultMaxMessageSize`, `DefaultMaxFrameSize` | the protocol's and the domain's constants |
| `AcceptKey` | the §4.2.2 digest, for a hand-written client handshake or a test |
| `DrainSignal` | the shutdown signal, re-exported so a handler need not import the parent |
| `HandshakeFailed`, `UpgradeUnsupported`, `ProtocolViolation`, `MessageTooLarge`, `InvalidPayload`, `ConnClosed`, `ConnMisconfigured` | sentinels |

`server.DrainSignal` is the same function, re-exported from the parent.

## Why-this-shape

- **`Upgrade` writes the refusal itself.** A handler that had to decide between
  400, 403, 405 and 426 would get it wrong, and the version refusal in
  particular must carry `Sec-WebSocket-Version: 13` or the client is left
  guessing (§4.4). The handler's whole error path is `return`.
- **`HandshakeFailed` and `UpgradeUnsupported` are separate sentinels.** One
  means the peer's request was not a handshake; the other means this deployment
  cannot take the socket over — HTTP/2, or a middleware that wraps the
  `ResponseWriter` without forwarding `Unwrap`. The first is a client error and
  the second is an operational one, and merging them would send an operator
  hunting through client logs.
- **`Receive` returns whole messages, and the payload aliases the connection's
  buffer.** Valid until the next `Receive`; keep it longer and copy it. That is
  what makes a steady-state read allocate nothing, and it is the same rule
  `server.Conn.Buffer()` already carries.
- **`Done()` and the `Send`/`Receive` refusal are both load-bearing.** A handler
  that selects on `Done()` stops between messages; a handler that only loops on
  `Receive` stops on the next one. Either alone leaves a shape that cannot be
  told a deployment is under way.
- **A close code that must never travel is refused, in both directions.**
  `CloseWith(CloseAbnormal, …)` is an error, and a peer that sends 1006 fails
  the connection. One predicate — `CloseCode.Sendable` — answers both, so the
  two cannot drift.
- **Zero ping interval is clamped, negative is refused** (ADR 0031), and
  "never" has its own spelling. `WithoutPing` disables the connection's ONLY
  liveness check, which the doc comment says out loud rather than leaving to be
  discovered.
- **The origin is checked by default**, and turning that off is
  `AllowAnyOrigin()` — a name a reviewer can grep for. The browser's same-origin
  policy does not apply to WebSocket, so the default-off switch is the
  difference between an endpoint and a CSRF primitive.
- **The sentinels are the engine's own values, not copies.**
  `TestSentinelsAreMatchableThroughTheFacade` pins that `errors.Is` still
  answers across the module boundary; re-declared sentinels would quietly answer
  false.
- **`TestEveryOptionForwarderReachesTheEngine` exists because the forwarders
  cannot be wrong in a way the service tests would catch.** A copy-paste that
  wired `MaxFrameSize` to `MaxMessageSize` compiles, and every test in
  `//internal/service/net/websocket` still passes. The only place that mistake
  is visible is here.

## The client side is NOT here, and that is a decision

There is no WebSocket **client** in this SDK, and the obstacle is the same one
that blocks an SSE client. `internal/core/net/response.go` documents the
outbound contract:

```go
// Body is the fully-read response body, already bounded by the client's
// size cap. It is nil for a status that carries no body, such as 204.
Body []byte
```

`ResponseValue.Body` is a `[]byte` the client has already read to completion and
closed. The type comment gives two reasons, and both are deliberate: a returned
`*http.Response` makes the caller responsible for closing the body — one
forgotten `Close` leaks a connection — and it makes an accurate byte count
impossible, because a body's size is only known once it has been read.

A WebSocket client needs strictly more than an SSE client does. It needs the
`101` **and the socket underneath it**: `http.Client` consumes the response and
hands back a body, never a `net.Conn`, so a client would either dial the socket
itself — bypassing the transport, its `Policy`, its redirect budget, its
`CallHook` and its TLS identity, i.e. every guarantee `pkg/v1/client` exists to
make — or `ResponseValue` would have to grow a hijacked-connection field, which
is a change to a published shape (ADR 0040) for a case no other caller has.

Either is a `pkg/v1` decision of its own, with its own ADR. The server half is
complete and useful alone: the consumer of a WebSocket endpoint is
overwhelmingly a browser's `WebSocket`, which needs nothing from this SDK.

## Do NOT

- Hand-edit `README.md` — it is generated from the package doc comment
  (`cd pkg/v1 && go generate ./server/...`, or `make docs-readme`).
- Add logic here; it belongs in `internal/service/net/websocket`.
- Call `Conn.Receive` from more than one goroutine — the protocol is one ordered
  frame stream and two readers would each take half of a message.
- Retain a `Message.Data` past the next `Receive` without copying it.
- Add a WebSocket client without the decision above being taken first.

## Verification

```
bazel test --config=race //pkg/v1/server/websocket:websocket_test
# Fallback:
cd pkg && GOWORK=off go test -race -cover ./v1/server/...
# expected: 100% for this facade — every forwarder is exercised through a real
# upgrade on the SDK engine, because a passthrough that forwards the WRONG
# option is invisible everywhere else.
```

## Reference

- ADR 0047 — `docs/adr/0047-sdk-net-websocket.md`
- ADR 0029 (the net domain), ADR 0043 (drain is a signal)
- ADR 0030 (stdout is a protocol channel), ADR 0031 (zero values are never inert)
- `internal/service/net/websocket/CLAUDE.md`, `internal/core/net/CLAUDE.md`
- The outbound contract this cites — `internal/core/net/response.go`
