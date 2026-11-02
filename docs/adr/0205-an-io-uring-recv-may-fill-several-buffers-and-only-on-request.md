# ADR 0205 — an io_uring recv may fill several buffers, and only on request

- **Status**: Accepted
- **Date**: 2026-11-02
- **Accepted**: 2026-11-02, on the measurements below. It can be revisited
  while the SDK is v0 ([ADR 0040](0040-changing-a-published-shape-while-v0.md)).
- **Deciders**: SDK maintainers
- **Amends**: [ADR 0189](0189-a-kernel-capability-is-probed-reported-and-switched-off-by-name.md)
  (a class A-code capability, `uring-recv-bundle`; class B's `uring-bundle`
  keeps the send bundles)
- **Related**: [ADR 0203](0203-an-io-uring-loops-rings-fill-in-order-and-a-rings-buffers-follow-its-table.md)
  §D2 (groups and slack), [ADR 0204](0204-an-io-uring-rings-buffers-are-consumed-incrementally-where-the-kernel-allows.md)
  (incremental consumption, the default)

## Context

A multishot recv fills one provided buffer per completion. Bytes beyond the
buffer, or beyond what is left of it under incremental consumption, come in
the next completions. `IORING_RECVSEND_BUNDLE` (6.10,
`IORING_FEAT_RECVSEND_BUNDLE`) lets one completion fill as many buffers as
the bytes waiting need, taken in their ring's order.

How bundles combine with `IOU_PBUF_RING_INC`, read in the kernel's sources:

- **Through 6.14, a bundle gets one buffer.** `io_ring_buffers_peek`
  limits a bundle to a single buffer on an incrementally consumed ring
  (`if (bl->flags & IOBL_INC) nr_avail = 1;`, io_uring/kbuf.c v6.12 l. 225,
  v6.13 and v6.14 l. 227). v6.15 removed the limit, and v7.0 does not
  have it (`io_ring_buffers_peek`, kbuf.c l. 249–330).
- **The completion's bid is the first buffer's.** `req->buf_index` is read
  from the head entry before the peek loop (kbuf.c v7.0 l. 297), and
  `__io_put_kbufs` posts it (l. 407–421).
- **`IORING_CQE_F_BUF_MORE` describes the last buffer.**
  `io_kbuf_inc_commit` (l. 35–60) consumes buffer after buffer and returns
  false, which posts `BUF_MORE`, only when it stops inside the last one.
- **Without incremental consumption the buffers are consumed whole.**
  `io_kbuf_commit` advances the head by the bundle's buffer count (l. 74;
  the count comes from `io_bundle_nbufs`, io_uring/net.c l. 875).
- **io-wq recvs get no bundle.** A recv that runs unlocked, in io-wq, gets
  one buffer (`io_recv_buf_select`).

## Decision

### D1 — A completion's bytes are walked buffer by buffer in ring order

Each group keeps, for every ring entry, the bid it last placed there
(`pbufGroup.order`), and each bid keeps its entry (`uring.bufPos`). A
completion's bytes start in its bid, at the bid's count under incremental
consumption, and go on through the entries after it (`uring.spans`).

Every buffer is done with except possibly the last, which the kernel keeps
when `BUF_MORE` is set under incremental consumption. Each done buffer is
counted out of `inRing`, queued on `recycle` and returned after
`OnBatchEnd`, as before. The bytes reach the protocol one buffer at a time,
as split reads always did.

Starvation, growth, groups and their slack (ADR 0203 §D2) are unchanged.

### D2 — Opt-in, gated by a probe that measures the combination

`uring-recv-bundle` is class A-code: a group asks for it in
`LoopValue.Capabilities`, and `KITSUNIUM_NET_DISABLE=uring-recv-bundle`
overrides that. A loop arms its recvs as bundles only if three things
hold:

1. the kernel advertises `IORING_FEAT_RECVSEND_BUNDLE`;
2. on a ring of its own, whose buffers are consumed the same way as the
   serving rings' (incrementally or not), a single-shot bundle recv of 100
   bytes into two 64-byte buffers returns all 100 bytes;
3. the group opted in.

An `EINVAL`, or one buffer filled — incremental consumption before 6.15 —
reads `absent` (`missing` if opted into) with the reason, and the recvs
fill one buffer per completion.

## Consequences

Measured in Docker, linux/arm64, kernel 7.0, two CPUs:

- **No gain, and a loss on large requests.** `BenchmarkURingRecvBundle`
  (3,000 connections, 64 in flight, 10 runs, incremental consumption),
  bundles against one buffer per completion:
  - 240-byte requests: the same (p=0.631);
  - 8,192-byte requests: **25 % slower** (6.82 µs against 5.44 µs,
    p=0.000);
  - capping a bundle at four buffers (`sqe.len`): still 20 % slower (6.77
    against 5.65 µs).

  This is why the capability is opt-in rather than class A-kernel.
- **The default does not change.** Against main, `BenchmarkURingRings_fill`
  is unchanged (p=0.912 with one ring and with three).
- **On 6.12 and up to 6.14 the probe finds no bundle**, since incremental
  consumption is the default there (ADR 0204).
- `TestConformance_bufferFaults` opts in, so where the kernel has bundles
  a completion spans its two 64-byte buffers in their scrambled ring order.
- **Memory:** two bytes of Go heap per buffer provided (`bufPos`), and two
  per entry of each registered group (`order`).

## Breaking changes

None. The facade gains `server.CapabilityURingRecvBundle`, an opt-in
token; the default behaviour does not change.

## Alternatives considered

- **Class A-kernel, on by default** — refused on the measurements above:
  no gain on small requests, a loss on large ones.
- **Bundles only without incremental consumption** — not chosen: the
  probe already measures what the serving rings' mode yields, and a group
  that wants bundles can switch `uring-pbuf-inc` off.
- **A version check instead of a probe** — refused: kernels carry
  backports. The probe measures the combination itself.

## References

- include/uapi/linux/io_uring.h (`IORING_RECVSEND_BUNDLE`,
  `IORING_FEAT_RECVSEND_BUNDLE`, `IORING_CQE_F_BUF_MORE`), v6.12.
- io_uring/kbuf.c `io_ring_buffers_peek`, `io_kbuf_inc_commit`,
  `io_kbuf_commit`, `__io_put_kbufs`; io_uring/net.c `io_recv_buf_select`,
  `io_recv_finish` — tags v6.12, v6.13, v6.14, v6.15, v7.0.
- `internal/service/net/loop/uring_bundle_linux.go` (the probe);
  `bundle_uring_linux_test.go` (`TestBufs_spans`,
  `TestURingRecvBundle_spansBuffers`, `TestURingRecvBundle_refused`,
  `TestURingRecvBundle_optIn`, `BenchmarkURingRecvBundle`).
