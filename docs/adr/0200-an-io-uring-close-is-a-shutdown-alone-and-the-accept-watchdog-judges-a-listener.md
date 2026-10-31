# ADR 0200 — an io_uring close is a shutdown alone, and the accept watchdog judges a listener

- **Status**: Accepted
- **Date**: 2026-10-31
- **Accepted**: 2026-10-31, on the measurements of a single-vCPU HTTP
  benchmark of the loop engine (go-loop-kodflow, its S11 runs at 60,000 and
  140,000 keep-alive connections) and on the kernel sources cited below. It
  can be revisited while the SDK is v0
  ([ADR 0040](0040-changing-a-published-shape-while-v0.md)).
- **Deciders**: SDK maintainers
- **Amends**: [ADR 0196](0196-a-drain-stops-the-routing-first-takes-the-listen-queue-then-closes-and-loopsauto-reads-gomaxprocs-once.md)
  §D3 (the drain's semantics are unchanged; its cost is); the package's
  close rule ("a close is a shutdown first", `internal/service/net/loop/CLAUDE.md`)
- **Related**: [ADR 0186](0186-a-plaintext-group-may-run-on-an-event-loop-and-its-http-is-a-profile-that-refuses.md)
  (the io_uring backend), [ADR 0189](0189-a-kernel-capability-is-probed-reported-and-switched-off-by-name.md)
  (the release check)

## Context

A benchmark held 140,000 keep-alive connections, nearly all idle, on one
vCPU, with `MaxConns` 196,000: three rings on one listener, each with a
65,535-entry direct-descriptor table. Two faults of the loop showed.

**The watchdog drained healthy rings.** It judged a ring alone: an accept
armed, the listener's queue not empty, and no accept completion on *that
ring* for 100 ticks. Three rings arm single-shot accepts on one listener and
the kernel hands each connection to one waiter; a ring whose accepts lose that
race for a second looks stalled while the queue moves. Rings 1 and 2 were
drained (`watchdog_fires` 26/4/4), ring 0 took every accept, filled its table
(`accept_pauses` 29) and starved its buffers: 5.8 % of the requests timed out.

**Stopping was quadratic.** Every close was `IORING_OP_SHUTDOWN` with
`IORING_OP_ASYNC_CANCEL` (`FD | FD_FIXED | ALL`) hard-linked behind it. A
cancel by descriptor cannot use the poll table's hash, which is keyed on
user_data: `io_poll_file_find` walks every bucket and every armed request in
it, and `__io_async_cancel` with `ALL` repeats the walk until it finds
nothing; `io_wq_cancel_cb` walks the io-wq's pending works each time too
(io_uring/cancel.c, io_uring/poll.c, 6.8, 6.12, 7.0). The shutdown always
runs in io-wq (`REQ_F_FORCE_ASYNC`, io_uring/net.c), so the linked cancel
ran there, one per connection, each over every connection still armed. A
drain or a `Close` of n idle connections cost n² — 2.2 s at 10,000, 24 s at
30,000, 249 s at 60,000, over 15 minutes at 140,000 — the threads in D
state, the process unkillable and port 80 held. Under Docker, on 7.0 with
eight CPUs, 20,000 idle connections on three rings took 5.15 s to drain.

A third fault showed once the cancel was gone: a secondary ring is entered
only to submit, and the kernel moves a CQ it held back (`NODROP`) into the
ring only on its wait path (`io_cqring_wait`, 6.8 to 7.0), never on a plain
submit. Thousands of shutdowns completing at once overflowed a secondary's
CQ, and its closes then waited on completions the loop never saw.

## Decision

### D1 — The watchdog judges a listener's queue, every ring together

The group's listener set counts each listener's accepts, every ring of every
loop together. When the watchdog would judge a ring stalled on a listener —
an accept armed, the ring not paused, the queue not empty, no accept
completion of its own for 100 ticks —, it first reads that count: if it moved
since its last look, another ring took connections and the queue moves; the
ring is judged again 100 ticks later and its re-arms start over. Only a
listener no ring accepted from for the whole window counts as a stall, which
re-arms, and after three re-arms drains the ring, as before. A ring whose own
accepts never win while others serve stays armed: it costs nothing, and it
takes connections again as soon as it wins one.

### D2 — A close is a shutdown alone; a backstop cancels by user_data

Every close is `IORING_OP_SHUTDOWN` (`SHUT_RDWR`) on the descriptor, counted,
then `close_direct` once the count reaches zero. No cancel follows it.
`inet_shutdown` sets `sk_shutdown` and calls `sk_state_change` whatever the
socket's state, `TCP_CLOSE` included (net/ipv4/af_inet.c, 6.8 to 7.0), which
wakes every waiter of the socket — io_uring's poll entries included, a wake
without a key matching any mask: the recv retries and ends on zero, a send
waiting for room fails with `EPIPE`. So the shutdown ends what the cancel
used to end, and each close costs one io-wq work and its completions. The
release check closes its victim the same way, a shutdown and then the
recv's end, so the probe judges the close the loop runs.

A slot still waiting on an operation `closeBackstopTicks` (1 s) after its
shutdown was issued has its recv, and its send if one is in flight,
cancelled by user_data, which looks one hash bucket up. The slots wait in the
order their shutdowns went out, so a batch looks only at the ones due. A
shutdown the SQ had no room for is backstopped the same way.

### D3 — An overflowed CQ is flushed by an enter with GETEVENTS

When a ring reports `IORING_SQ_CQ_OVERFLOW`, the loop enters it with
`IORING_ENTER_GETEVENTS` and `min_complete` zero, which never blocks and runs
the kernel's flush, then reaps again.

## Consequences

- A drain or a `Close` costs O(n) in the kernel: under Docker on 7.0, 20,000
  idle connections on three rings drain in 0.2 s and close in 0.16 s with
  the race detector on (5.15 s and 1.56 s before). The drain's semantics
  (ADR 0196 §D3) are unchanged; `Shutdown`'s context still bounds it, and
  `Close` still waits at most a second for the closes before the rings close.
- On 6.8 and 6.12, where a direct descriptor's put waits for older requests,
  the shutdown still sends the FIN at once; the sweep is unchanged.
- A ring of a group with several rings per listener is no longer drained for
  losing the accept race; a listener that truly stops still drains its rings
  one after another, as before.

## Breaking changes

None: no API changes. `State` reports the same counters; `watchdog_fires`
no longer counts a ring that lost the accept race.

## Alternatives considered

- **Cancel the recv by user_data, unlinked, instead of by descriptor** —
  refused as the rule: a cancel still walks the io-wq's pending works, which
  during a drain holds every shutdown not yet run, so a drain stays
  quadratic in that queue. It is the backstop.
- **Close the ring and let the kernel cancel everything on stop** — refused:
  it skips the FIN of a connection whose output is still in the kernel until
  the ring's exit work runs, and the provided buffers and the slab are
  unmapped while the kernel may still write into them. With D2 the stop is
  linear already.
- **Never drain a ring from the watchdog** — refused: a ring whose accepts
  are truly lost would hold its share of the listener for nothing; D1 keeps
  the check and judges it on the right signal.

## Deferred

- The recv-stall watchdog and the forced-full CQ test the package's
  CLAUDE.md names as not built.
- A measure of D2 at 60,000 and 140,000 connections on the benchmark host.

## References

- Linux io_uring/cancel.c `__io_async_cancel`, `io_try_cancel`;
  io_uring/poll.c `io_poll_find`, `io_poll_file_find`; io_uring/io-wq.c
  `io_wq_cancel_cb`; io_uring/net.c `io_shutdown_prep`, `io_shutdown`;
  io_uring/io_uring.c `io_cqring_wait` (6.8, 6.12), io_uring/wait.c (7.0);
  net/ipv4/af_inet.c `inet_shutdown` — tags v6.8, v6.12, v7.0.
- `io_uring_enter(2)` (`IORING_ENTER_GETEVENTS`, `IORING_FEAT_NODROP`),
  `io_uring_prep_cancel(3)`, `io_uring_prep_shutdown(3)`.
- `internal/service/net/loop/conformance_stop_linux_test.go` bounds a drain
  and a close of 20,000 idle connections on every backend;
  `watchdog_uring_linux_test.go` pins D1.
