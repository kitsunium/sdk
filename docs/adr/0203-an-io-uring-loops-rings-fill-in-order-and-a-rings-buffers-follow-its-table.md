# ADR 0203 — an io_uring loop's rings fill in order, and a ring's buffers follow its table

- **Status**: Accepted
- **Date**: 2026-11-01
- **Accepted**: 2026-11-01, on a single-vCPU HTTP benchmark of the loop
  engine (go-loop-kodflow, S12: `MaxConns` 196,000, three rings) and the
  benchmarks below. It can be revisited while the SDK is v0
  ([ADR 0040](0040-changing-a-published-shape-while-v0.md)).
- **Deciders**: SDK maintainers
- **Amends**: [ADR 0201](0201-an-io-uring-rings-buffers-follow-its-bursts-and-a-slot-table-grows-by-steps.md)
  §D1 (a ring's count of provided buffers, and when it grows); nothing
  published
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

At 140,000 connections, 47,000 to 50,000 a ring, the holds failed with
p99 at 5 to 8.5 s and about 48,500 `buffer_starved` each: a ring may hand
its kernel at most 8,192 buffers (ADR 0201 §D1), and a burst of tens of
thousands of requests arriving while the loop serves a batch takes a
buffer each. Three flaws compounded it, measured in Docker (7.0) with
50,000 connections on one ring sending at once while the loop is held:

- the count: 8,192 buffers for a table of 65,535;
- the growth: `provide` compared the recvs waiting with the buffers it
  believed the kernel held, after the batch's buffers came back, and stayed
  at 2,049 buffers;
- the re-arm: a starved recv was re-armed against `inRing`, which counts a
  buffer out only once its completion is reaped. With thousands of
  completions still in the CQ or held back by the kernel, the re-armed recvs
  found the ring empty and starved again: 786,384 `ENOBUFS` for a burst of
  50,000, each one a completion and a re-arm.

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

### D2 — A ring's buffers follow its table, its growth its starvation

A ring may hand its kernel as many 2 KiB buffers as its table holds
connections, rounded up to a power of two: 8,192 at least, 65,536 at most
(a bid is 16 bits). The kernel's buffer ring holds fewer than 65,536
entries, so the buffers are grouped by 8,192: group *g* holds bids
8,192·*g* to 8,192·(*g*+1)−1 and is registered as buffer group 1+*g* only
when its first bid is handed out. An idle ring keeps one group of 128 KiB
of kernel memory and 1 MiB of resident buffers, as under ADR 0201; a group
the kernel refuses ends the growth there.

`provide` grows by as many buffers as recvs starved since it last looked,
`bufsStep` (512) at least — each `ENOBUFS` is a buffer the burst lacked —,
and by `bufsStep` when the kernel holds under a quarter of those provided.
A loop's rings together hand out at most `loopBufBudget`, 128 MiB.

A recv is armed on the group with the most buffers beyond the recvs
already armed on it: a multishot recv stays on its group, so a group armed
past its buffers would starve at every burst whatever the others hold. A
starved recv is re-armed only while the kernel holds a buffer for it: the
completions still in the CQ are counted out first, and nothing is re-armed
while the kernel holds completions back.

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
- `TestConformance_noStarvationAtScale` (50,000 connections on one ring, all
  sending at once while the loop is held, Docker linux/arm64, 7.0): the
  first burst starves 49,489 recvs once each and grows the ring to 50,001
  buffers; the second starves 21 and grows a step; the third starves none.
  Every echo arrives in 243 to 266 ms after the first burst, against 454 to
  466 ms and 786,384 `ENOBUFS` a burst on 8,192 buffers of which 2,049 were
  ever provided.
- Memory: a ring holds what its bursts took, never more than its table
  rounded up — 97 MiB of resident buffers after a burst of 50,000, 1 MiB
  idle — and a loop at most 128 MiB, every ring together. On a 1 GB host
  that is the cost of serving such a burst without starving; past the
  budget a burst starves again, and its recvs are re-armed as buffers come
  back. Each further group is 128 KiB of kernel memory, charged to
  `RLIMIT_MEMLOCK` where the kernel accounts it.
- An idle connection costs 139.1 B of Go heap with io_uring (one byte more
  a slot: the group its recv is armed on), 137.0 B with epoll
  (`TestMemory_idleConnection`).

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
- **One group of 32,768 entries** — refused: the kernel's ring is sized at
  registration, 512 KiB a ring whether used or not, charged to
  `RLIMIT_MEMLOCK` on kernels that account it; and it still starves a table
  of 65,535 at a burst of 50,000.
- **Smaller buffers** — refused for the reason ADR 0201 gives: a request
  with a few headers passes 1 KiB.
- **Giving buffers back after a burst** — deferred, as in ADR 0201: a buffer
  in the kernel's ring may be written at any moment, so only one the loop
  holds could be released, and the loop holds none between batches.

## References

- `internal/service/net/loop/fill_uring_linux_test.go`
  (`TestURingFill_inOrder`, `TestConformance_ringsFillInOrder`,
  `BenchmarkURingRings_fill`);
  `starve_uring_linux_test.go` (`TestConformance_noStarvationAtScale`);
  `bufs_uring_linux_test.go` (`TestBufs_groupsAsNeeded`).
- Linux io_uring/kbuf.c `io_register_pbuf_ring` (entries a power of two,
  under 65,536; a CQE's bid 16 bits) — tags v6.12, v7.0.
