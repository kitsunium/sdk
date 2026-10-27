# ADR 0192 — an HTTP group's protocols are computed once, and its ALPN follows them

- **Status**: Accepted
- **Date**: 2026-10-27
- **Accepted**: 2026-10-27, on the owner's decisions of 2026-10-24 for the
  unified network work (HTTP/2 comes from `net/http`'s `Protocols`, for
  `http.Handler` groups only, D-N4; a configuration that is not honoured is
  refused, never narrowed, R6), carrying the h2 sub-step of step P2. It can be
  revisited while the SDK is v0 ([ADR 0040](0040-changing-a-published-shape-while-v0.md)).
- **Deciders**: SDK maintainers, owner
- **Related**: [ADR 0029](0029-sdk-net-domain.md) D3 (`net/http` does the
  protocol on the SDK's listener), [ADR 0187](0187-the-handler-type-chooses-the-engine.md)
  (D-N4; the kind set a group's options record),
  [ADR 0186](0186-a-plaintext-group-may-run-on-an-event-loop-and-its-http-is-a-profile-that-refuses.md)
  (the loop's `http1` profile, HTTP/1.1 only)

## Context

An HTTP group served by `HandleHTTP` hands its connections to an
`http.Server`; the group's own listener does TLS, with the identity's
`ServerConfig`. Which HTTP version a connection speaks was therefore decided
in two places that never looked at each other:

- **the listener's ALPN** was the identity's `NextProtos`, verbatim — empty
  unless the identity's file named it, so a TLS group negotiated nothing and
  never served HTTP/2, while one whose identity listed `h2` did;
- **`http.Server`** left `Protocols` nil and enabled HTTP/2 itself on `Serve`.

Nothing refused an identity offering `h2` to a server configured without it,
which completes the handshake and then drops the connection, and nothing set
HTTP/2's bounds: a release of the standard library could move the window a
stream that is never read holds, or how many streams a peer opens.

## Decision

### D1 — One list, computed once at Start

`Protocols(protos ...string)` names the versions an HTTP group serves by ALPN
identifier, `http/1.1` and `h2`, in the order its TLS listeners prefer them;
it records `OptionProtocols` (20). Unset, a group with TLS serves `h2` then
`http/1.1`, and one without serves `http/1.1`: cleartext HTTP/2 (h2c) is not
served. At `Start` the group computes, once, from that list and its identity:

- **the ALPN list** its TLS listeners offer: the list itself when the
  identity names no `NextProtos`, else the identity's entries the list holds,
  in the identity's order — the operator's preference;
- **`http.Server.Protocols`**: HTTP/1 when the list holds `http/1.1`, HTTP/2
  when it holds `h2`.

Both derive from the one computation, so a version is negotiated if and only
if it is served. `http.Server.TLSConfig` is never given the identity: the
listener's configuration is the one that negotiates (`net/http` fills an
empty one of its own on `Serve`, which carries no certificate).

### D2 — What Start refuses

`ProtocolsRefused` (`0.2.11.62`, field `reason`): an empty list; a repeated or
unknown identifier; `h2` without TLS; an identity whose `NextProtos` names an
HTTP version the list excludes — it would be negotiated, then dropped — or
nothing the list holds, which fails every handshake; and `Protocols` on a
group that serves no `http.Handler`. A loop group refuses the option with
`LoopOptionUnsupported`, like every kind it does not honour: the `http1`
profile is HTTP/1.1 only. None of these narrows the configuration in silence
(R6).

### D3 — HTTP/2's bounds are set

`http.Server.HTTP2` is set for every HTTP group: 100 concurrent streams,
16 KiB frames (the protocol's minimum), 4 KiB HPACK tables, a 256 KiB window
a stream's body may hold unread and 1 MiB for the connection — four streams'
worth, so one handler that never reads its body does not stop the others —
and the group's write timeout as the write-byte bound. An unfinished header
block — HEADERS without END_HEADERS and no CONTINUATION — opens no stream, so
the connection is idle: the group's `IdleTimeout`, or its `ReadTimeout` when
it sets none, closes it. `ReadHeaderTimeout` remains HTTP/1.1's;
`net/http`'s HTTP/2 server has no header phase of its own, which this record
documents rather than works around.

## Default change

A TLS HTTP group whose identity names no `NextProtos` offered no ALPN, so it
served HTTP/1.1 only; it now offers `h2` then `http/1.1` and serves HTTP/2 to
every client that asks. This is a change of default, allowed while v0 (ADR
0040). The earlier behaviour comes back in code with `Protocols("http/1.1")`.
There is no operator switch for it: `KITSUNIUM_NET_ENGINE` and
`KITSUNIUM_NET_DISABLE` only restrict engines and kernel capabilities and never
touch TLS (ADR 0188, R5), so the `net:default-change` label's `Override:`
block, which names those variables alone, cannot express it and the pull
request does not carry the label.

## Consequences

- New: `Protocols`, `OptionProtocols` (20), `ProtocolsRefused`
  (`0.2.11.62`), re-exported by `pkg/v1/net/server`.
- Tests: ALPN with HTTP/1.1 alone, h2 alone, both in either order and the
  identity's order; every refusal; the adapter's `Protocols` and `HTTP2`;
  an unfinished header block closed by the idle or the read bound; an unread
  stream held to its window while another stream on the connection sends a
  body larger than it.

## Alternatives considered

- **Leave `Protocols` nil and only filter the ALPN list.** Rejected: two
  places would still decide, and a release of `net/http` that changes its
  default would change the group.
- **Narrow an identity's `NextProtos` to what the group serves in silence.**
  Rejected (R6): an operator who listed `h2` would get HTTP/1.1 and no error.
- **Serve h2c on a plaintext group.** Out of scope: nothing asks for it, and
  a cleartext upgrade is one more parser in front of every request.
