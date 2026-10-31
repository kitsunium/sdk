# ADR 0201 — an io_uring ring's buffers follow its bursts, and a slot table grows by steps

- **Status**: Accepted
- **Date**: 2026-10-31
- **Accepted**: 2026-10-31, on the memory measurements of a single-vCPU HTTP
  benchmark of the loop engine (go-loop-kodflow, S10 and S11: 10,000 to
  140,000 keep-alive connections). It can be revisited while the SDK is v0
  ([ADR 0040](0040-changing-a-published-shape-while-v0.md)).
- **Deciders**: SDK maintainers
- **Amends**: nothing published; the package's provided-buffer and slot-table
  sizing (`internal/service/net/loop/CLAUDE.md`)
- **Related**: [ADR 0186](0186-a-plaintext-group-may-run-on-an-event-loop-and-its-http-is-a-profile-that-refuses.md)
  §D7 (pointer-free control, mapped payloads), [ADR 0189](0189-a-kernel-capability-is-probed-reported-and-switched-off-by-name.md)
  (`uring-pbuf-inc` stays class B), ADR 0200 (the same benchmark's close and
  watchdog)

## Context

Each io_uring ring registered 4,096 provided buffers of 4 KiB. The kernel
takes them in ring order and the loop returns them at the tail, so it rotates
through all of them: 16 MiB resident per ring after a few thousand requests,
whatever the load. At 20,000 idle connections, each having sent one
request, that was most of the process's 3.5 KB per connection, and with
three rings 50 MB of the 132 MB of process memory at 140,000. A request there
is under 1 KB.

The count still ran short: 303,499 and 1.1 million `buffer_starved` at
47,000–65,000 connections a ring, when a batch received more requests than
the ring held buffers.

The slot table grew by `append`: past a few thousand slots Go's growth leaves
up to a quarter of the array unused, and each growth briefly holds the old
array beside the new one — about 100 B a slot each.

## Decision

### D1 — Buffers are mapped up front and handed to the kernel on demand

A ring maps 8,192 buffers of 2 KiB, private and anonymous, and registers a
buffer ring of 8,192 entries (`IORING_REGISTER_PBUF_RING`, its entries a
power of two under 65,536, io_uring/kbuf.c, 6.12 and 7.0). Only the first
512 are added to the ring at setup. After each batch, once the batch's
buffers are back, the ring adds buffers it never had, by bid, 512 at least,
when recvs wait for more buffers than the ring holds or the ring holds under
a quarter of those it was given. It never takes any back.

A page of an anonymous private mapping is resident only once written
(`mmap(2)`), and the kernel writes a buffer only once it is in the ring. So a
ring's resident buffers are the most it had out at once: 1 MiB until a
burst, 16 MiB at most, against 16 MiB always. Starvation is cured by count:
up to twice the former number of buffers, half the size, added as the bursts
ask.

2 KiB holds a keep-alive request; a larger one arrives over several
completions and the spill joins them, as any split read always did. A test
asking for a tiny buffer ring (the conformance's two buffers of 64 bytes)
gets all of it from the start.

### D2 — A slot table grows by bounded steps

A ring's slot table doubles from 64 up to 2,048 slots, then grows 2,048 at a
time, never past the table size; the timer wheel's arrays take the same
capacity at the same time.

## Consequences

- An idle or lightly loaded ring holds 1 MiB of buffers instead of 16 MiB;
  a ring that met a burst keeps what the burst took, at most 16 MiB.
- Each ring's buffer ring is 128 KiB of kernel memory (8,192 entries of
  16 bytes), charged to `RLIMIT_MEMLOCK` where the kernel accounts its
  regions, against 64 KiB before.
- 10,000 idle connections cost 137.9 B of Go heap each with io_uring and
  136.4 B with epoll, against 145.8 B each (`TestMemory_idleConnection`,
  Docker, 7.0 arm64); at 140,000 the unused tail of the table is at most
  2,048 slots instead of up to a quarter.
- A burst past the ring's buffers still meets `ENOBUFS` once: the starved
  recvs wait for the batch's end and are re-armed on the buffers added then.

## Breaking changes

None: no API changes.

## Alternatives considered

- **Incremental consumption (`IOU_PBUF_RING_INC`, 6.12)** — deferred: it lets
  several requests share a buffer, but a buffer stays the kernel's until it
  is full, so the loop must track each bid's offset and return a buffer only
  on its last completion; and turning `uring-pbuf-inc` on means a probe and a
  class change the 6.8 lanes cannot require (ADR 0189). D1 answers the
  measured starvation and memory without it; it stays class B.
- **Divide one budget between a loop's rings** — refused: a fixed share is
  still resident in full once rotated; D1 makes each ring's memory its own
  demand, which is the split the load decides.
- **1 KiB buffers** — refused: a request with a few headers or a cookie passes
  1 KiB, and each extra completion costs a CQE and a spill copy.
- **A two-level slot table, never copied** — refused for now: every access
  indexes `r.conns`, and D2 removes most of the waste for a few lines.

## Deferred

- `IOU_PBUF_RING_INC` behind a probe, as above.
- Giving buffers back after a burst (the ring would need to know which bids
  are untouched since).

## References

- `io_uring_register_buf_ring(3)`, `io_uring_buf_ring_add(3)`, `mmap(2)`
  (`MAP_PRIVATE | MAP_ANONYMOUS`, pages allocated on first touch).
- Linux io_uring/kbuf.c `io_register_pbuf_ring` (entries a power of two,
  under 65,536), io_uring/memmap.c (regions accounted, 7.0) — tags v6.12,
  v7.0.
- `internal/service/net/loop/bufs_uring_linux_test.go`,
  `slots_internal_test.go`, `memory_linux_test.go`.
