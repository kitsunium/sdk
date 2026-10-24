# ADR 0174 — a TLS identity is checked on every handshake and reloaded in place, and a connection is admitted before its handshake

- **Status**: Accepted on 2026-10-13 by the owner, with ADR 0152, which required it accepted first (R3-17). Reserved by ADR 0152 (D2, D4, R7-00d, R7-26) for its two declared siblings outside the consensus domain. Short on purpose: the reasons are ADR 0152's and are cited, not repeated. Both siblings are implemented: `net/tlsid`'s identity and `net/server`'s `Admission`, which the consensus transport uses
- **Date**: 2026-10-13
- **Accepted**: 2026-10-13
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
   **The union only grows until the process restarts (review of [PR #301](https://github.com/kitsunium/sdk/pull/301),
   L2).** Removing a compromised CA from the roots file is not enough:
   the operator restarts every process that loaded it, after the file no
   longer names it. A refused pair is read again at the next look, even
   when its files keep the same modification time, and counted once (L3).

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

- ADR 0152's transport admits with `net/server`'s `Admission` and
  authenticates through `net/tlsid`'s identity: its own check, the
  canonical URI SAN and the member's pinned SPKI, runs after the
  identity's chain verification, on every handshake.
- `design/net.yaml` gains the admission's declarations.

## Alternatives considered

- **`MaxConns` alone.** It bounds the group, so one address can fill it.
- **Admission after the handshake.** Every refused peer would first cost a
  full handshake, which is what a flood is made of.

## Amendment — a reload decides by the files' bytes, not their times (2026-10-24, #633)

D1's reloadable source looked at its files only when the newest of their
modification times was after the one it last kept. A time is not a
change: a copy that keeps its source's time (`cp -p`, `rsync -a`,
`tar x`, a package), a filesystem of a one-second granularity, or a
clock set back gives a new file a time older than, or the same as, the
last one. A CA rollover's bundle laid down that way was never trusted,
and a renewal landing at the kept time was never served: the node
expired with `ReloadFailures` at 0
([#633](https://github.com/kitsunium/sdk/issues/633)).

- **A look reads the three files and compares their bytes**, by the
  SHA-256 of each, with the bytes the last look decided on. A look is
  still made at most once per `Every`. Bytes that did not change are not
  parsed again, whatever their times; bytes that changed are, whatever
  their times. A pair is parsed from the very bytes compared.
- **A per-file signature was not chosen.** Size, time, inode and change
  time come from `syscall.Stat_t`, whose fields differ per platform and
  are missing on some the SDK builds for (Windows), and on a filesystem
  of a coarse granularity the change time is as coarse as the
  modification time: a rewrite in place, of the same size, within the
  same second, keeps every one of them. Bytes have neither hole, and
  three PEM files of a few KiB once per `Every` cost nothing that
  matters.
- **L2 and L3 hold.** The roots only grow. A roots file that cannot be
  read is no change; one that names no certificate, an empty one
  included, is counted once and never replaces nor narrows them. A
  refused pair is read again at every look and counted once for the
  bytes refused; the same bytes are not parsed again, since they would
  be refused again. A pair caught mid-write is refused, then taken at
  the next look once whole.
- Nothing published changes: no frame, no signature, no doc comment.
