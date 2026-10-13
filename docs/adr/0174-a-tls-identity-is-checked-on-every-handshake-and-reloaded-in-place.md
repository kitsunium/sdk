# ADR 0174 — a TLS identity is checked on every handshake and reloaded in place, and a connection is admitted before its handshake

- **Status**: Proposed. Reserved by ADR 0152 (D2, D4, R7-00d, R7-26) for its two declared siblings outside the consensus domain. Short on purpose: the reasons are ADR 0152's and are cited, not repeated
- **Date**: 2026-10-13
- **Deciders**: SDK maintainers
- **Depends on**: [ADR 0029](0029-sdk-net-domain.md) (`net/server`, stream groups), [ADR 0039](0039-extending-a-published-port-without-breaking-it.md) (siblings)
- **Related**: [ADR 0152](0152-a-write-is-committed-when-a-majority-holds-it-on-disk.md) (the first consumer of both siblings)

## Context

ADR 0152's transport authenticates every peer by a certificate naming a
slot and a key pinned by its SPKI (D4, R7-41, R8-07). Two things it needs
are not in the SDK's network domain:

1. **A TLS identity checked on every handshake and reloaded in place.**
   `net/server`'s `TLS(identity)` exposes neither the peer's certificate
   nor a verification hook, and a renewed certificate means a restart.
2. **Admission before the handshake.** A peer that opens sockets and says
   nothing costs a goroutine and a slot each until its handshake times out.
   `MaxConns` caps the group, not one address, so a single address can
   exhaust it and lock the members out (ADR 0152 R2-22).

## Decision

### D1 — The identity sibling (`net/tlsid`, three parts)

1. a `VerifyConnection` gate, fail-closed, run on every handshake, resumed
   ones included; session tickets are off, so none is resumed without it;
2. the verified peer identity, bound to the connection and handed to the
   handler after the handshake;
3. a reloadable source (`GetCertificate`, `GetClientCertificate`) that
   validates the key against the certificate, refuses a new SAN or a new
   key, keeps the old identity on failure and counts the failures; during
   a CA rollover it trusts the union of the roots, and a connection is
   closed at its peer certificate's `NotAfter` and at a maximum age.

The dialer verifies the chain, roots and extended key usage, before it
reads any SAN; it never sets `InsecureSkipVerify` without doing that itself.

### D2 — The pre-handshake admission hook (`net/server`)

`net/server` gains an `Admission`: a per-address budget of connections that
have not completed their handshake, keyed on the IPv4 address or the IPv6
/64, with a larger reserved budget for the addresses a caller declares
(the members' addresses, resolved; one that does not resolve gets none).

- It runs in the accept loop, before any TLS byte is read: a connection
  over its address's budget is closed at once and counted.
- A connection leaves the budget when its handshake completes, or when it
  ends without one; a plaintext group's connections leave it when they end.
- It is a `StreamGroup` option, `PreHandshake(a)`, and a value a transport
  with its own listener calls directly (`Admit`, `SetReserved`), so ADR
  0152's transport and a `net/server` group share one rule.
- A zero budget is clamped to its default (4 per address, 8 reserved), never read as "unlimited" (ADR 0031, clamp half).

## Consequences

- ADR 0152's transport admits with `net/server`'s `Admission`. The
  identity sibling is not written yet: the transport carries its own
  `VerifyConnection`, SPKI pin and PEM reload until `net/tlsid` exists,
  then moves onto it without a change of its published surface.
- `design/net.yaml` gains the admission's declarations.

## Alternatives considered

- **`MaxConns` alone.** It bounds the group, so one address can fill it.
- **Admission after the handshake.** Every refused peer would first cost a
  full handshake, which is what a flood is made of.
