# ADR 0204 — an io_uring ring's buffers are consumed incrementally where the kernel allows

- **Status**: Accepted
- **Date**: 2026-11-01
- **Accepted**: 2026-11-01, on the measurements below, for the single-vCPU
  benchmark's 150,000 to 180,000 keep-alive connections. It can be revisited
  while the SDK is v0 ([ADR 0040](0040-changing-a-published-shape-while-v0.md)).
- **Deciders**: SDK maintainers
- **Amends**: [ADR 0189](0189-a-kernel-capability-is-probed-reported-and-switched-off-by-name.md)
  (`uring-pbuf-inc` leaves class B for class A-kernel);
  [ADR 0201](0201-an-io-uring-rings-buffers-follow-its-bursts-and-a-slot-table-grows-by-steps.md)
  (incremental consumption, deferred there, is built)
- **Related**: [ADR 0203](0203-an-io-uring-loops-rings-fill-in-order-and-a-rings-buffers-follow-its-table.md)
  §D2 (a ring's buffers follow its table; the group slack)

## Context

A recv into a provided buffer takes the whole buffer, 2 KiB, whatever it
received: a keep-alive request of 100 to 300 bytes leaves most of it unused
until the batch's end returns it. A burst of 50,000 requests on one ring
needs 50,000 buffers at once — 98 MiB resident once the ring met it, and
49,489 `ENOBUFS` while it grows there (ADR 0203 §D2). At 150,000 to 180,000
connections a loop's 128 MiB budget cannot hold such a burst, on a host of
1 GB.

Since 6.12 a buffer ring registered with `IOU_PBUF_RING_INC` is consumed as
far as each completion took: the kernel moves the head buffer's address and
length on, posts the completion with `IORING_CQE_F_BUF_MORE`, and hands the
rest of the same buffer to the next recv, whichever connection it serves
(io_uring/kbuf.c `io_kbuf_inc_commit`, 6.12 and 7.0). The buffer leaves the
ring on its last completion, the one without `BUF_MORE`.

## Decision

### D1 — The rings ask for incremental consumption, and fall back on a refusal

Every buffer group of a ring is registered with `IOU_PBUF_RING_INC`. A kernel
that does not know the flag refuses the registration with `EINVAL`: the ring
registers the group as before, and the loop's later rings do not ask again.
The report's `uring-pbuf-inc` row reads `on`, or `absent` with the refusal;
the operator's `KITSUNIUM_NET_DISABLE=uring-pbuf-inc` gives the former
behaviour back, never asking.

### D2 — A buffer goes back only after its completion without BUF_MORE

The loop keeps, per bid, how many bytes the buffer's completions took: a
completion's bytes start there. A completion with `BUF_MORE` leaves the
buffer the kernel's — it is neither counted out of the ring nor queued to
go back; the one without it queues it, and the buffer is handed back after
`OnBatchEnd`, whole again, its count at zero. A completion's bytes stay
valid until `OnBatchEnd` as before: the kernel writes only past them.

Starvation, the growth on it, the groups and their slack of ADR 0203 §D2
are unchanged: an `ENOBUFS` still means a ring with no buffer left, and a
recv is still armed on the group with the most buffers beyond its armed
recvs and 32. Under incremental consumption that is a conservative bound —
a buffer serves many recvs.

### D3 — Class A-kernel

`uring-pbuf-inc` is on wherever the kernel has it, with no opt-in: what it
changes is where a recv's bytes land, not what the loop does with them, and
`TestConformance_bufferFaults` (two 64-byte buffers, scrambled returns,
0xAA fills) passes in both modes. No lane requires it yet: the noble 6.8
leg lacks it and runs the fallback.

## Consequences

Measured in Docker, linux/arm64, kernel 7.0:

- `TestConformance_noStarvationAtScale`, 50,000 connections on one ring all
  sending at once while the loop is held: 512 buffers (1 MiB) and no
  `ENOBUFS` from the first burst on, against 50,801 buffers (98 MiB on
  main) and 49,489 `ENOBUFS` on the first burst.
- The same bursts finish later: on two CPUs, twelve bursts each mode
  alternated, every echo arrived in 315 ms at the median incrementally
  against 271 ms with a buffer a recv (+16 %). Recvs that meet a buffer's
  end split a line in two — 240 to 1,058 more completions a burst of
  50,000, 0.5 to 2 % — which does not account for all of it; the rest is not
  explained. One buffer a recv remains the operator's switch.
- One request's round trip under a steady load (`BenchmarkURingPbufInc`,
  3,000 connections, 64 in flight, 10 runs, two CPUs): 16-byte lines the
  same (p=0.912); 240-byte requests 3.7 % faster incrementally (p=0.019).
  Against main, `BenchmarkURingRings_fill` is unchanged (p=0.80 one ring,
  p=0.68 three).
- Each bid's count is 4 bytes of Go heap per buffer provided, only under
  incremental consumption.

## Breaking changes

The default changes on kernels from 6.12: `KITSUNIUM_NET_DISABLE=uring-pbuf-inc`
restores it. No API changes.

## Alternatives considered

- **Class A-code, an opt-in** — refused: the memory and starvation it
  removes are those of the loads it is for, and the operator's switch
  covers a deployment the burst latency matters more to.
- **Smaller buffers** — refused in ADR 0201, and still: a request with a
  few headers passes 1 KiB; incremental consumption sizes each use to the
  request instead.
- **Receive bundles** (`IORING_RECVSEND_BUNDLE`, 6.10) — separate, and
  later: they gather several buffers into one completion, which is what a
  large body needs, not a small request.

## References

- `io_uring_register_buf_ring(3)` (`IOU_PBUF_RING_INC`), include/uapi/linux/io_uring.h
  (`IORING_CQE_F_BUF_MORE`), io_uring/kbuf.c `io_kbuf_inc_commit` — tags
  v6.12, v7.0.
- `internal/service/net/loop/pbufinc_uring_linux_test.go`
  (`TestURingPbufInc_bufMore`, `TestURingPbufInc_refused`,
  `BenchmarkURingPbufInc`); `starve_uring_linux_test.go` and
  `conformance_linux_test.go`'s `TestConformance_bufferFaults`, each in
  both modes.
