# ADR 0047 — WebSocket (RFC 6455), server side, in the stdlib

- **Status**: Accepted
- **Date**: 2026-09-09
- **Deciders**: SDK maintainers
- **Extends**: [ADR 0029](0029-sdk-net-domain.md) — the network domain gains a
  second long-lived server-side protocol beside Server-Sent Events
- **Related**: [ADR 0043](0043-drain-is-a-signal-not-a-cancellation.md),
  [ADR 0030](0030-stdout-is-a-protocol-channel.md),
  [ADR 0031](0031-policy-zero-values-are-never-inert.md),
  [ADR 0040](0040-changing-a-published-shape-while-v0.md)

## Context

The `net` domain already does raw TCP, TLS and mTLS identity, per-phase
deadlines, middlewares, an outbound guarded transport, and — since ADR 0043 —
Server-Sent Events, a server→client stream that never ends.

WebSocket is the bidirectional case. It is a one-shot HTTP handshake followed by
a binary frame protocol, and both halves are small: a SHA-1 digest over a
concatenated GUID, and a header of at most fourteen bytes. Nothing in it needs a
dependency.

What it *does* need is a great deal of refusing. RFC 6455 is unusual in how much
of it is written as MUST-fail rather than should-tolerate, and for a reason
worth stating: a frame stream is length-prefixed, so two endpoints that disagree
about one frame do not lose one message — they lose the stream, and every byte
after it is read as something the sender never wrote. Tolerance is not
generosity here, it is desynchronisation.

## Decision

### D1 — stdlib only, no `gorilla/websocket`

The protocol is implemented from RFC 6455 directly. The parts a library would
save are the easy ones (bit packing, base64, SHA-1); the parts that are hard are
the refusals, and delegating those means trusting a dependency's reading of
"MUST fail the connection" on this SDK's behalf, on a socket that faces the
internet. It also keeps `internal/service` dep-light, which ADR 0016 and
ADR 0018 already require.

### D2 — the format is core, the connection is service

`internal/core/net/websocket*.go` owns the wire format: opcodes, close codes,
the frame header parser and encoder, the masking transform, the UTF-8 rule and
the accept-key digest. `internal/service/net/websocket` owns the connection: the
handshake, the reader, the assembler, the write path, the heartbeat and the
drain watcher. `pkg/v1/server/websocket` is aliases and forwarders.

This is the SSE split, applied again, and it is what lets the adversarial tests
sit at the layer they belong to: a malformed *header* is a core test with a byte
slice, a malformed *sequence* is a service test with a socket.

### D3 — every MUST-fail in RFC 6455 fails the connection

Enumerated, because "conformant" is not a claim a reader can check:

| Refused | Section | Close code |
|---|---|---|
| an unmasked client frame | §5.1 | 1002 |
| a set RSV bit with no extension negotiated | §5.2 | 1002 |
| a reserved opcode (0x3–0x7, 0xB–0xF) | §5.2 | 1002 |
| a fragmented control frame | §5.5 | 1002 |
| a control payload over 125 bytes | §5.5 | 1002 |
| a length not in its minimal encoding | §5.2 | 1002 |
| a 64-bit length with the sign bit set | §5.2 | 1002 |
| a continuation with no message in progress | §5.4 | 1002 |
| a data frame interrupting a fragmented message | §5.4 | 1002 |
| a close payload of exactly one byte | §5.5.1 | 1002 |
| a close code of 1004, 1005, 1006, 1015 or an unallocated range | §7.4.2 | 1002 |
| a text message that is not valid UTF-8 | §8.1 | 1007 |
| a close reason that is not valid UTF-8 | §5.5.1 | 1007 |
| a frame or message past the configured ceiling | — | 1009 |

**Masking is the one to underline.** It protects nothing cryptographically — the
key travels in the frame — and it is easy to read as ceremony. Its purpose is
that a hostile script cannot steer a browser into emitting attacker-chosen bytes
that a transparent intermediary would read as a second HTTP request. A server
that accepted unmasked frames would hand that script the one mechanism the
design has against cache poisoning. It is a security requirement, not a
tolerance dial.

**The minimal-length rule is enforced**, though Autobahn treats it as
non-strict. §5.2 says MUST, and a second spelling of the same frame is exactly
the ambiguity a length-prefixed protocol cannot afford between two parsers that
disagree.

### D4 — UTF-8 is judged on the reassembled message, never per frame

A four-byte rune may straddle a fragment boundary. A per-frame validator would
close perfectly conformant connections with 1007 — the failure mode is
*rejecting valid input*, which is worse than the one it was trying to prevent,
because it only appears against senders that chunk differently. The check runs
once, on the whole message, and `TestUTF8IsJudgedOnTheReassembledMessage` pins
both directions: the split rune that must survive and the truncated one that
must not.

### D5 — bounds are checked against the ANNOUNCEMENT, before any allocation

A frame's length is a 64-bit field the peer writes. `MaxFrameSize` is compared
against it the moment the header is parsed, before a byte is read and before a
buffer is sized; `MaxMessageSize` is compared against the accumulated total,
because a peer under the frame limit can still pass the message limit a thousand
small frames at a time.

Neither has an "unbounded" spelling. That is not an omission: an unbounded
ceiling on a number the peer chooses is not a configuration option, it is a
remote memory allocator. Zero is clamped to the default (ADR 0031); negative is
refused; a frame ceiling above the message ceiling is refused as a mistake with
two readings and no way to pick between them.

Control frames are exempt from the caller's frame ceiling — §5.5 already caps
them at 125, and applying a smaller bound on top would make `Ping` unanswerable
to enforce a limit that was never about control frames.

### D6 — permessage-deflate is out of scope, and the refusal is on the wire

The extension would pull `internal/service/transform` into this package and
roughly double its surface: a compression context per connection, a
sliding-window parameter negotiation, `client_no_context_takeover` and its three
siblings, and a decompression-bomb bound to get right.

It is refused twice, so the refusal is verifiable rather than asserted:

1. the 101 carries **no** `Sec-WebSocket-Extensions` header, which §4.2.2
   defines as the way a server says it uses none — offering it does not fail the
   handshake, the connection simply proceeds uncompressed;
2. `ParseWSFrameHeader` refuses any **RSV bit**, which is precisely what a
   deflated frame looks like, so a client that compressed anyway gets 1002
   instead of handing the application bytes nothing can decode.

### D7 — the origin is checked by default

The browser's same-origin policy does **not** apply to WebSocket: any page may
open a connection to this server, and the browser attaches the user's cookies to
the handshake. An upgrader that accepted every origin by default is a cross-site
request forgery primitive with a default-on switch, which is exactly the shape
ADR 0030 rejects — a zero value is the choice made by someone who has not yet
learned the question exists, so it must not be the dangerous one.

Default: an `Origin` header, when present, must match the request's own host
(scheme, host and port compared together — matching the host alone would accept
`http://` for an `https` server). A request with **no** `Origin` is allowed: a
CLI, a service or a Go client has no ambient credential to abuse. The opaque
`null` origin is refused, since it is equal to nothing including itself.
`AllowOrigins(…)` replaces the rule; `AllowAnyOrigin()` removes it, by a name a
reviewer can grep for.

### D8 — the heartbeat counts frames, not pongs, and it is the only liveness check

A peer that vanishes without closing leaves a socket that is perfectly readable
and simply never produces another byte: no error, no Close frame, nothing. The
heartbeat sends a Ping and, one interval later, ends the connection if not a
single frame has arrived since. Counting *any* frame rather than a matching Pong
means a chatty peer is never probed to death, and a silent one is obliged to
answer.

Per ADR 0031, a zero interval is clamped rather than meaning "never" —
"never" is `WithoutPing()`, and that option's doc comment says that it disables
the connection's only liveness check.

### D9 — a hijacked connection is the handler's, and the engine was closing it

This is the defect the domain exposed, and it predated it.

`Upgrade` hijacks the response. A hijacked socket has left the HTTP lifecycle:
`net/http` stops tracking it, and `http.Server.Shutdown` documents that it
neither closes nor waits for one ("Shutdown does not attempt to close nor wait
for hijacked connections such as WebSockets"). The SDK engine did neither. It
saw `StateHijacked` through `ConnState`, released the `ServeConn` blocked on the
connection, and then ran the deferred `release` — which **closed the socket**.
That close is right for every request/response handler and fatal for exactly one
case.

Measured before the fix, with nothing but `net/http`:

| | before |
|---|---|
| handler hijacks, waits 200 ms, writes | client reads `EOF` |
| error reported anywhere | none |

The engine now records the hand-over on the pooled wrapper and `release` skips
the close. Two consequences are deliberate:

- **the connection is not waited for by the drain**, matching `net/http`'s own
  carve-out, and it stops being counted in `State().Active`;
- **the drain signal is therefore the whole mechanism** rather than a courtesy.

The flag is read on **both** arms of the `select` in `ServeConn`, not only under
`<-done`: a hijack that lands while the drain is cancelling the context leaves
both cases ready, and `select` picks between ready cases at random — reading it
on one arm only would sever an upgraded connection roughly half the time.

`TestAHijackedConnectionSurvivesTheEngine` provokes the defect with a plain
`net/http` handler and no WebSocket frame, because the defect was never about
WebSocket. It is mutation-checked: forcing the close back on fails it and
nothing else.

### D10 — the drain closes with 1001, and the request context is not watched

The connection captures `corenet.DrainSignal(r.Context())` as a **channel** at
the upgrade. A watcher goroutine turns its close into a Close frame carrying
1001 — §7.4.1's "an endpoint is going away, such as a server going down" — and
then closes the socket. `Receive` and `Send` refuse from the same instant, so a
handler that only loops on them terminates too.

The request context is deliberately **not** watched. `net/http` cancels it the
moment `ServeHTTP` returns, and a hijacked socket outlives the handler by
design, so a connection watching it would end at an instant that says nothing
about the peer. Capturing the channel is what keeps the signal usable
afterwards — and is the reason ADR 0043 made it a value rather than a
cancellation.

### D11 — no client, and the obstacle is named

There is no WebSocket client here. `internal/core/net/response.go` fixes the
outbound contract:

```go
// Body is the fully-read response body, already bounded by the client's
// size cap. It is nil for a status that carries no body, such as 204.
Body []byte
```

A client needs strictly more than an SSE client does. It needs the `101` **and
the socket underneath it**, and `http.Client` hands back a body, never a
`net.Conn`. So it would either dial the socket itself — bypassing the transport,
its `Policy`, its redirect budget, its `CallHook` and its TLS identity, i.e.
every guarantee `pkg/v1/client` exists to make — or `ResponseValue` would grow a
hijacked-connection field, which is a change to a published shape (ADR 0040) for
a case no other caller has.

Either is its own `pkg/v1` decision with its own ADR. The server half is
complete alone: the consumer of a WebSocket endpoint is overwhelmingly a
browser's `WebSocket`, which needs nothing from this SDK.

## Consequences

- Error block `0.2.11.27` – `0.2.11.33`, declared in `internal/core/net` like
  every other code in the domain. No new range in `codeRangeOwners` — the net
  domain already owns `0.2.11.*` (ADR 0035).
- `Receive` hands back a slice that aliases the connection's reassembly buffer,
  valid until the next call. It is the `Conn.Buffer()` rule again, and it is
  what makes a steady-state read allocate nothing.
- Reads are single-goroutine by contract; writes are safe for concurrent use.
  There is no lock that would make two readers correct, so there is none.
- The SDK never fragments what it sends. Fragmentation exists for a sender that
  does not yet know its message length; every message this API can express is
  already in memory.
- Anything else that hijacks — a `CONNECT` proxy, a custom binary protocol over
  an `Upgrade` — now works on the engine. That was not the goal; it is what
  fixing D9 at the right layer buys.

## What this ADR does NOT cover

- **permessage-deflate** and every other extension (D6).
- **A WebSocket client** (D11).
- **RFC 8441 / WebSocket over HTTP/2.** A different mechanism entirely
  (extended `CONNECT`), refused by name with a `426` rather than half-served.
- **Subprotocol semantics.** The handshake negotiates a *name*; what that name
  means is the application's.
- **Per-message compression, backpressure policy, and reconnection.** A `Conn`
  is a socket with framing; queueing, shedding and retry are decisions about the
  application's data, which the transport cannot make.
- **Autobahn Testsuite as a CI gate.** The conformance cases are transcribed by
  hand into `TestAdversarialFramesFailTheConnection` with their RFC sections
  cited; running the Python suite would add a container to the lane for cases
  the table already covers. Adding it later is a testing decision, not a
  protocol one.

## Why not

- **Depend on `gorilla/websocket`.** Rejected — D1. It would also be the first
  network dependency in `internal/service`, against ADR 0016 and ADR 0018.
- **Surface frames instead of messages.** Rejected: fragmentation is the
  sender's private choice of chunk size, so every consumer would carry an
  implementation detail of the peer's writer.
- **Validate UTF-8 per frame.** Rejected — D4. It rejects valid input.
- **Default to accepting every origin, like most libraries.** Rejected — D7.
  The zero value must not be the dangerous one.
- **Give WebSocket its own shutdown path.** Rejected for the same reason
  ADR 0043 rejected giving SSE one: the ownership defect in D9 was not
  protocol-specific, and a protocol-specific fix would have left the general
  case broken and harder to find next time.
- **Let a zero ping interval mean "never".** Rejected — ADR 0031. A connection
  with liveness silently disabled works perfectly on loopback and stops
  noticing vanished peers in production, which is where mobile clients live.

## References

- RFC 6455 — the sections each refusal cites appear in the test names
- `internal/core/net/websocket.go`, `websocket_frame.go`, `websocket_close.go`,
  `websocket_message.go`, `websocket_opcode.go`
- `internal/service/net/websocket/{websocket.go,handshake.go,options.go}`
- `pkg/v1/server/websocket/websocket.go`
- `internal/service/net/server/{conn_waiter.go,http_adapter.go,pool.go,conn.go}`
  — the hand-over in D9
- `internal/service/net/server/hijack_external_test.go` — the mutation-checked
  regression guard
- ADR 0029 §D3 (net/http adapted, not reimplemented), ADR 0043 §shutdown
