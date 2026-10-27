# ADR 0193 — a Windows listener holds its address exclusively, closes gracefully and rebinds, bounded

- **Status**: Accepted
- **Date**: 2026-10-27
- **Accepted**: 2026-10-27, carrying the Windows sub-step of step P2 of the
  unified network work (owner's decisions of 2026-10-24). It can be revisited
  while the SDK is v0 ([ADR 0040](0040-changing-a-published-shape-while-v0.md)).
- **Deciders**: SDK maintainers, owner
- **Related**: [ADR 0029](0029-sdk-net-domain.md) (the server engine),
  [ADR 0103](0103-a-bucket-per-caller-one-backoff-curve-and-a-retry-on-the-clock-it-is-given.md) (the backoff), [ADR 0188](0188-the-operators-switches-only-restrict-and-state-says-what-the-server-runs-without.md)
  (State says what the server runs with)

## Context

On Windows a socket bound without options can have its port taken over by
another socket that sets `SO_REUSEADDR`, after which which socket receives a
connection is indeterminate. Microsoft's guidance ("Using SO_REUSEADDR and
SO_EXCLUSIVEADDRUSE") is that every server sets `SO_EXCLUSIVEADDRUSE`. The
same page names the price: a socket bound exclusively cannot be bound again
while a connection it accepted is still active, and a connection closed with
`closesocket` alone may stay active — unread data, unacknowledged output — so
a restarted server can fail to bind its own address. The remedy it gives is
a graceful close: `shutdown(SD_SEND)`, then `recv` until zero.

The engine bound with the standard library's defaults (no option on
Windows), closed every connection with `Close`, and failed a bind at once.

## Decision

- **D1 — Exclusive by default.** On Windows every unicast TCP listener and
  unicast UDP socket the engine binds — goroutine, loop and datagram groups
  alike — sets `SO_EXCLUSIVEADDRUSE` before bind, in `net.ListenConfig`'s
  `Control`; a socket that cannot take it is not bound. A multicast socket is
  excluded: its port is shared with the group's other members by design.
  AF_UNIX has no port. Other platforms have no such option, and Go binds
  their listeners with `SO_REUSEADDR`, which there does not allow a takeover.
- **D2 — An adopted socket is read, never set.** A supervisor's socket is
  already bound; the option is read back and reported, and its absence is
  never fatal.
- **D3 — Reported.** `ListenerStateValue.Exclusive` says whether the socket
  holds its address exclusively; `ListenerStateValue.BindWait` how long the
  bind waited for it.
- **D4 — A graceful close.** On Windows, when a connection ends — its handler
  returned, it was not hijacked — the engine shuts its write half
  (`shutdown(SD_SEND)`, `CloseWrite`), reads until the peer's zero, bounded by
  500 ms and 64 KiB, then closes. The budget's own severing (`closeLive`)
  stays a plain close: it exists to be immediate.
- **D5 — A bounded rebind.** On Windows a bind refused with `WSAEADDRINUSE` is
  tried again, eight attempts at most, 25 ms doubling to 1 s between them
  (≈ 2.6 s in all), and the wait lands in `BindWait`. Elsewhere one attempt:
  an address in use there is a real conflict.

## Consequences

- A second process can no longer bind, or take over, a Windows server's
  address while it runs; a restart rebinds once the predecessor's connections
  end, which the graceful close makes prompt.
- An engine-closed connection on Windows can take up to 500 ms of its own
  goroutine to finish; a peer that never answers costs that, never more.
- New `ListenerStateValue` fields `Exclusive` and `BindWait`.
- Tests: the retry and the applicability rule on every platform; on the
  windows lane, both sockets exclusive, a second bind refused, a socket bound
  without the option reading false, and a restart on the same address after
  three echoed connections, every answer delivered whole.

## Alternatives considered

- **`SO_LINGER` instead of the graceful close.** Rejected, as Microsoft's page
  does: a short linger resets connections, a long one lets a peer stall the
  close.
- **Retry every bind on every platform.** Rejected: on Linux and the BSDs an
  address in use is another process, and waiting only delays the error.
