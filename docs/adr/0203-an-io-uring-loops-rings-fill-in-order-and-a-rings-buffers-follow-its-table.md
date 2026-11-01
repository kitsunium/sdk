# ADR 0203 — an io_uring loop's rings fill in order, and a ring's buffers follow its table

- **Status**: Accepted
- **Date**: 2026-11-01
- **Accepted**: 2026-11-01, on a single-vCPU HTTP benchmark of the loop
  engine (go-loop-kodflow, S12: `MaxConns` 196,000, three rings) and the
  benchmarks below. It can be revisited while the SDK is v0
  ([ADR 0040](0040-changing-a-published-shape-while-v0.md)).
- **Deciders**: SDK maintainers
- **Amends**: nothing published; the package's accept and ring sizing
  (`internal/service/net/loop/CLAUDE.md`)
- **Related**: [ADR 0186](0186-a-plaintext-group-may-run-on-an-event-loop-and-its-http-is-a-profile-that-refuses.md)
  §D5 (the ring formula), [ADR 0200](0200-an-io-uring-close-is-a-shutdown-alone-and-the-accept-watchdog-judges-a-listener.md)
  (the watchdog judges a listener), [ADR 0202](0202-a-loop-batch-pays-its-fixed-cost-once-and-a-grouped-wait-starts-on-the-request-rate.md)
  (a batch's fixed cost)

## Context

A loop holds ⌈max(MaxConns, SoftConns) / min(RLIMIT_NOFILE, 1 << 20)⌉ rings
(ADR 0186 §D5): `MaxConns` 196,000 under an `RLIMIT_NOFILE` of 65,535 is
three. Every ring armed its accepts at start, and the kernel hands a
listener's connections to its waiters in turn, so 10,000 connections sat
about 3,330 on each ring. A batch then submitted on each secondary — one
`io_uring_enter` each — and every completion on a secondary wrote the
eventfd that wakes the primary, a poll completion more to reap. Interleaved
on one build, on a single-vCPU host, three rings cost 68.8 µs a request and
one ring 61.9: 6.9 µs more, 5.7 of them in the kernel, for a load one ring
holds.

## Decision

### D1 — A ring past the first accepts only once those before it are full

A ring is *full* once its free entries fall to an eighth of its table (and
never fewer than twice the accept reserve), or when it accepts nothing for
a reason of its own: the watchdog drained it, or a test keeps the primary
idle. It is no longer full only once more than twice that margin is free.
Ring *i* arms accepts only while every ring before it is full; a ring that
loses that right cancels its accepts in flight once, and keeps the
connections it holds. Only ring 0 accepts at start.

The margin opens the next ring well before the current one pauses under its
reserve, so no accept gap opens between them; the hysteresis keeps a table
hovering at the margin from opening and closing the next ring at each batch.
The group's drain takes every listener's queue on every ring as before: the
fill is the serving group's, and is not moved while draining.

A ring with no connection and no accept in flight costs a batch what it
cost before: no submit (nothing pending makes no system call, ADR 0202), no
completion, so no eventfd write and no wake.

## Consequences

- A load one ring holds runs on one ring whatever `MaxConns` derived:
  `BenchmarkURingRings_fill` (3,000 keep-alive connections, 64 requests in
  flight, Docker linux/arm64, 7.0, two CPUs, 8 interleaved runs) measures
  10.41 µs a request on one ring and 10.43 on three, where three rings
  spread cost 13.63 (−23.45 %, p=0.000).
- A connection's ring follows the order it arrived in, not a balance: past
  the first table the rings fill one after the other. Nothing balanced the
  rings' work before either — the kernel's hand-off did.
- The watchdog sees no stall on a ring that is closed: it has no accept
  armed.

## Breaking changes

None: no API changes.

## Alternatives considered

- **Fewer rings by default** — refused: the ring count is the formula of
  ADR 0186 §D5, and a table of 65,535 entries holds no more than that;
  `MaxConns` past one table must still be served.
- **Open the next ring only when the current one pauses** — refused: an
  accept gap of a batch at each crossing, and an open-close at each batch
  for a table at its reserve.
- **Balance the rings by their live connections** — refused: it is the
  spread this ADR removes.

## References

- `internal/service/net/loop/fill_uring_linux_test.go`
  (`TestURingFill_inOrder`, `TestConformance_ringsFillInOrder`,
  `BenchmarkURingRings_fill`).
