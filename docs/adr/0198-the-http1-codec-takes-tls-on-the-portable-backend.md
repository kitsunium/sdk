# ADR 0198 — the http1 codec takes TLS on the portable backend

- **Status**: Accepted
- **Date**: 2026-10-27
- **Accepted**: 2026-10-27, carrying the owner's decision D-N6 of 2026-10-24
  and rule R6, which [ADR 0187](0187-the-handler-type-chooses-the-engine.md)
  §D5 recorded and left to land with the simple API. How R6 reads on this path
  (D3 below) was the lead maintainer's choice while the owner was away, and is
  open to the owner's review. It can be revisited while the SDK is v0
  ([ADR 0040](0040-changing-a-published-shape-while-v0.md)).
- **Deciders**: SDK maintainers, owner
- **Implements**: ADR 0187 §D5; amends [ADR 0186](0186-a-plaintext-group-may-run-on-an-event-loop-and-its-http-is-a-profile-that-refuses.md)
  §D2 and §D3, as ADR 0187 §D5 announced

## Context

An `HTTP1Handler` is the SDK's own HTTP: one codec, one profile, one canonical
target, lanes. Until now it could not take TLS: a loop group refused it
(`LoopTLSUnsupported`), so a program that needed HTTPS had to fall back to
`http.Handler` and net/http, with another parser and none of the chain's links.
The owner decided (D-N6) that crypto/tls performs the handshake on the
goroutine engine and the cleartext then goes through the codec, on the
portable backend split into lanes; no kTLS, no TLS on a native loop (D-N3).

## Decision

### D1 — The path

A loop group whose handler is the http1 adapter (`http1.HandleLoop`, which
exposes `Unwrap()`), given `TLS(id)`, runs on the portable backend, whatever
the platform:

- its listeners are wrapped in TLS with the identity's configuration,
  offering **`http/1.1` alone** by ALPN — a client that asks for `h2` only
  fails the handshake;
- each accepted connection is negotiated on a goroutine of its own, bounded
  by `HandshakeTimeout` (the domain's 10 s default when unset), **before** its
  lane sees it: a failed or stalled handshake is closed and never reaches
  `OnOpen` nor `TotalConns`; while it negotiates it counts against
  `MaxConns`, so a flood of silent peers is refused at the ceiling;
- the cleartext then reaches the codec on the portable backend's lanes, so
  the profile, its refusals, the canonical target and `Lane` are exactly the
  plaintext path's.

`HTTP(addr, h1, TLS(id))` takes this path: the simple API's HTTP entry serves
an `HTTP1Handler` with TLS. The environment's switches still only restrict
(R5): the backend is already the portable one.

### D2 — What stays refused

A raw `LoopHandler` given TLS is refused with `LoopTLSUnsupported`, as ADR
0186 §D2 decided: only the codec has a cleartext it can be handed.

### D3 — R6 on this path (the lead's reading, taken while the owner was away)

R6 refuses a loop-only option combined with TLS. On this path,
`LoopTLSUnsupported` with the option named refuses:

- `TLS` with a zero identity — nothing to present;
- `Loop(v)` naming a native backend, io_uring or epoll — TLS runs on the
  portable backend alone (`option` = `Loop`);
- `UseLoop` — a loop middleware sees the codec's callbacks, not the TLS
  connection's, and is refused rather than half-applied.

`Loop(v)` with the backend left to auto or set to portable is honoured — its
`Loops`, `SpillCap`, `OutputCap` and `Outbound` size the lanes — since D-N6
puts this path on "the portable backend split into `Loops` lanes"; reading R6
literally would have fixed it at one lane. `HandshakeTimeout` is honoured.

## Consequences

- `http1.LoopHandler.Unwrap`; `LoopTLSUnsupported` now carries an `option`
  field on this path. No new code.
- A TLS codec group's State reports the portable backend and its lanes.
- Tests: the codec served over TLS on two lanes, ALPN offering HTTP/1.1
  alone; a silent peer closed at `HandshakeTimeout`, never counted; P2's
  conformance — N connections × N tokens through `Routes` and `Bearer`,
  released buffers marked — over portable+TLS under `-race`; each refusal;
  the simple API's HTTP entry with an `HTTP1Handler` and TLS.

## Alternatives considered

- **TLS on a native loop through kTLS.** Excluded by the owner (D-N3).
- **Serve an `HTTP1Handler` with TLS through net/http.** Rejected: two parsers
  would judge the same request, which is what the canonical target exists to
  prevent.
