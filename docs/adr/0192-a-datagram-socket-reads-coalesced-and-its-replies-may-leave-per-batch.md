# ADR 0192 — a datagram socket reads coalesced, and its replies may leave per batch

- **Status**: Accepted
- **Date**: 2026-10-25
- **Accepted**: 2026-10-25, on the owner's decisions of 2026-10-25 for step
  P4a of the unified network work: the L3 gate's CPU is the machine's, and the
  batching of replies is opt-in, `Packet.Reply` staying synchronous by
  default. It can be revisited while the SDK is v0
  ([ADR 0040](0040-changing-a-published-shape-while-v0.md)).
- **Deciders**: SDK maintainers, owner
- **Amends**: [ADR 0029](0029-sdk-net-domain.md) (the datagram engine: one
  `Packet` a datagram, now also out of a coalesced message), and
  [ADR 0187](0187-the-handler-type-chooses-the-engine.md) (a new `OptionKind`,
  19, judged by its rules)
- **Related**: [ADR 0186](0186-a-plaintext-group-may-run-on-an-event-loop-and-its-http-is-a-profile-that-refuses.md)
  §D8 (`State`), [ADR 0188](0188-the-operators-switches-only-restrict-and-state-says-what-the-server-runs-without.md)
  (`Faults`), [ADR 0163](0163-the-sdk-is-designed-by-its-diagram-and-kit-writes-only-data-and-test-pins.md)
  (design first), [ADR 0165](0165-a-performance-contract-is-the-designs-kit-measures-what-the-compiler-decides-and-a-test-holds-the-rest.md)
  (performance contracts)

## Context

The datagram engine read with `recvmmsg` on Linux, one datagram a message,
and replied with one `WriteTo` a datagram. Three things were wrong or missing:

- **The capacities were set once.** The kernel writes back into each header
  the name and control lengths the last message used. A message that carried
  no control data leaves `msg_controllen` at zero, so the next read has no room
  for any — a fault invisible while the reader asked for no control message,
  fatal the moment it does.
- **No UDP GRO.** A sender that batches with UDP GSO — QUIC stacks, media
  relays, the L3 load of the merge gate — has each send segmented again on
  receive: one socket buffer, one queue operation and one copy-out header a
  datagram. With `UDP_GRO` the kernel queues the send whole and says its
  segment size in a control message.
- **No batched send.** A handler answering every datagram of a batch paid one
  syscall a reply, and none of them could be one GSO send.

The plan's P4a gate (P8) first held the step to two ratios on L3 — UDP
datagrams of 1200 bytes from a GSO sender of 54 segments, 4 flows at a fixed
rate, over a veth pair: datagrams a receive at least 4× (lower bound), CPU a
datagram at most 0.75× (upper bound), on benchstat's interval at n = 10. D7
leaves the first reported, not gated.

## Decision

### D1 — Every read restores every capacity

Before each `recvmmsg` — the waiting read and the drain of D4 — every header's
`msg_namelen` is `sizeof(struct sockaddr_storage)` (`RawSockaddrAny`),
`msg_controllen` the room its slot has, `CMSG_SPACE(int)` — the one control
message asked for, `UDP_GRO` —, and `msg_flags` zero. The returned length and
`msg_flags` are read for each message.

### D2 — A message cut short is dropped and counted

`MSG_CTRUNC` (the control data did not fit, so the segment size may be lost)
and `MSG_TRUNC` on a coalesced read (several whole datagrams and a part, which
cannot be told apart) drop the message, counted in
`StateValue.Datagrams.TruncatedPackets`. `MSG_TRUNC` without GRO is one
datagram past the ceiling, which the probe byte already shows: it stays
`OversizedPackets`, as before.

### D3 — UDP GRO is on wherever the cmsg reader is

The batched reader switches `UDP_GRO` on at construction for a UDP socket;
the portable reader, which cannot split, never does, and a kernel or a
socket that refuses keeps one datagram a message. Under GRO a slot holds a
whole coalesced message, at least 65 535 bytes (the 16-bit IP length a send is
bounded by), and the read loop serves each datagram of it as its own `Packet`,
every one `seg` bytes but the last, each judged against `MaxPacketSize` on its
own. The memory is the batch's: 16 slots by default, 1 MiB a socket.

### D4 — A handover drains with GRO on, then switches it off

`Shutdown` hands every datagram socket over before closing it, for a
successor that inherits it (systemd's socket, a supervisor) and may not split:
the read loop drains what the socket holds without waiting, until `EAGAIN`,
serving it, then switches `UDP_GRO` off and ends; `Shutdown` closes the stream listeners first, then
waits for the drains no longer than 250 ms (`handoverBudget`) inside its own
budget — a socket receiving faster than one batch a read never reaches
`EAGAIN`, and the stream and loop drains after it share the same context —;
a drain still running then has GRO switched off at once, and the close cuts
it. The order is the kernel's, and it is the reverse of
the plan's first wording ("GRO off, then drain"): `udp_recvmsg` writes the
`UDP_GRO` control message only while the option is on, so a message coalesced
before the switch and read after it arrives without its segment size — one
datagram the length of all of them. A test pins it. The instant between the
drain's empty read and the switch is the residue: a send coalesced there is
left to the successor. `Close` switches GRO off and waits for nothing.

### D5 — Replies are synchronous by default; `BatchReplies` batches them

By default `Packet.Reply` is one `WriteTo` and returns its error, as always.
`BatchReplies()`, a `GroupOption` of kind `OptionBatchReplies` (19), changes a
datagram group's replies: one made while its batch is served copies its
datagram into a preallocated queue (128 replies, 256 KiB) and reports it sent,
`len(b)` and nil; the batch's replies leave when it ends, before the next
read. One that fails then is counted in `StateValue.Datagrams.ReplyFailures`,
since its handler has returned. A reply made while no batch is served is
written at once. Under the option `Reply` is valid only during the handler's
call, as `Data` already is: the read loop reuses one `Packet` value for every
datagram — its `From` already followed the next datagram before this record —,
so one kept and replied to while a later batch is served is queued with that
batch, to the peer `From` then names. A `Packet` of its own per datagram would
cost an allocation a datagram on the path held to none.

- **Linux, a UDP socket**: one `sendmmsg` a batch, resumed from the count each
  call returns. Every run of consecutive replies to one peer of one size, the
  last possibly shorter, at most `min(64, ⌊(65535 − iphdr − 8) / size⌋)`
  (iphdr 20 on IPv4, 40 on IPv6: 54 of 1200 bytes on both) and no larger than
  an Ethernet frame's payload, is one GSO send (`UDP_SEGMENT`). `EIO` — the
  device has no checksum offload, which GSO requires — switches GSO off for
  the socket's life, reported as a `gso` fault in `State`, and the replies
  leave one a message. `EMSGSIZE` is a sizing bug: counted in
  `Datagrams.GSOOversized`, the span's replies counted failed, GSO left on.
  `EINVAL` — a segment past the path's MTU — sends that span one by one, GSO
  left on.
- **Elsewhere, a Unix datagram socket, or one with no raw descriptor**: the
  same queue, one `WriteTo` each at the batch's end. darwin sends one datagram
  a call. A zoned IPv6 peer, whose zone only `WriteTo` resolves, is written at
  once.

The option is honoured or refused by ADR 0187's rules (R6): a datagram group
honours it on every platform — the contract is the same, only the syscall
count differs —, a stream group, goroutine or loop, refuses it at `Start` with
`LoopOptionUnsupported` naming it.

### D6 — L3's CPU is the machine's

The P4a gate's `cpu-ns/datagram` on L3 is every CPU's busy time in
`/proc/stat` over the window — all fields but idle and iowait, in `USER_HZ`
ticks — divided by the datagrams received, over a window of 400 000
datagrams a sample fixed in the bench (`l3Window`), whatever `-benchtime` says:
`USER_HZ` is 100 on every Linux ABI, so at the job's 100 000 datagrams one tick
was 100 ns a datagram, 2.5 % of engine A, and the quantisation alone pushed the
bound past the threshold (0.757, median 0.65); at 400 000 a tick is 25 ns. The
metric and the threshold are the owner's; only the instrument's resolution is
the bench's. The receiver's own `rusage`, which
the gate first read, is reported beside it as `rcv-cpu-ns/datagram`, not
gated. The receiver's CPU is dominated by the copy-out of 1200 bytes a
datagram, the same for both engines, and what GRO saves — the segmentation of
each GSO send on receive, its socket buffers and queue operations — runs in
softirq, charged mostly to the sender's context on a veth pair. Measured
(docker, Linux 7.0, 4 CPUs, 10 interleaved rounds): the receiver's rusage
gave B/A medians near 0.81 and an upper bound of 1.05; the machine's CPU,
at 400 000 datagrams a sample, gives medians of 3 542 and 1 825 ns, an upper
bound of 0.576. A GitHub-hosted runner (Linux 6.17, 4 CPUs) gives medians of
2 188 and 725 ns, an upper bound of 0.368. Ten rounds of L3 take about a
minute and a half of the bench job's 30. The sender is the same GSO sender on
both engines, so it adds the same work to both sides of the ratio. The L3
child never replies, so the gate does not depend on D5's mode.

### D7 — Datagrams a receive is reported, not gated

On the owner's decision of 2026-10-26, P4a's gate is its CPU a datagram
alone; `datagrams/syscall` stays measured and is written in netgate's report
as a `Reported` criterion — judged against its floor of 4×, never settling
the gate. On L3 engine A already reads one GSO send's 54 datagrams a receive,
since the kernel segments a send and queues the pieces at once, so B's count
is how many sends queue before its reader wakes: the runner's scheduling, not
the engine. The same code gave a lower bound of 4.73 under Docker and 3.54 on
a GitHub-hosted runner (A 54.0, B 189 to 196), where its CPU passed at 0.368.

## Consequences / Semantics

- `core/net` gains `DatagramStateValue` (`TruncatedPackets`, `GSOOversized`,
  `ReplyFailures`), `StateValue.Datagrams` and `OptionBatchReplies`;
  `FaultValue.Source` may be `gso`. `net/server` gains `BatchReplies`.
  `pkg/v1/net/server` re-exports them (`DatagramState`, `BatchReplies`,
  `OptionBatchReplies`).
- A UDP group on Linux reads with GRO on: one `recvmmsg` returns several
  datagrams a message, the handler sees the same `Packet`s as before, and the
  read batch holds 64 KiB a slot.
- `sendmmsg` is not in the standard library's `syscall` table on linux/amd64
  and linux/386: its number is declared by architecture
  (`sendmmsg_linux_*.go`).
- tools/netgate's P4a row is active: the merge gate requires its CPU a
  datagram; `criterionEntity` gains `Reported`, a criterion judged and
  written in the report that never settles a gate.

## Breaking changes

Deliberate, and allowed while v0 (ADR 0040):

1. **A UDP group's read batch is larger on Linux**: 64 KiB a slot under GRO,
   where it was the ceiling plus one byte. Old shape: `MaxPacketSize + 1`
   bytes a slot. New shape: at least 65 535.
2. **`Shutdown` serves what a coalescing socket queued before closing it**,
   within its budget. Old shape: the socket was closed with whatever it held.

`BatchReplies` changes nothing for a group that does not ask for it.

## Alternatives considered

- **Batch replies by default.** Rejected by the owner: `Reply`'s error is the
  caller's today, and a reply reported sent before it left is a different
  contract, taken by asking for it.
- **Turn GRO off, then drain** (the plan's first wording). Rejected on the
  kernel's behaviour (D4): the messages queued coalesced lose their segment
  size once the option is off.
- **Keep the receiver's rusage and lower the threshold.** Rejected: the
  threshold would be fitted to a measure that misses where the saving is.
- **Size GRO slots to the ceiling.** Rejected: a coalesced message past its
  slot is cut, and every datagram in it lost.
- **A switch to turn GRO or GSO off from the environment.** Deferred: it joins
  ADR 0188's registry with the kernel capabilities of step P3.

## Deferred

- `KITSUNIUM_NET_DISABLE` tokens for GRO and GSO, with P3's capability reports.
- A sender address cache, so a read allocates nothing a message.
- Datagrams on the loop engine (P4b), only if a measurement shows a gap.

## References

- Plan P4a and P8 (the gates table).
- `include/uapi/linux/udp.h` (`UDP_SEGMENT` 103, `UDP_GRO` 104);
  `net/ipv4/udp.c` (`udp_recvmsg`, `udp_send_skb`).
- Impl: `design/net.yaml`, `internal/core/net/` (`state_datagram.go`,
  `loop.go`), `internal/service/net/server/` (`multireader_linux.go`,
  `multiwriter_linux.go`, `reply_queue.go`, `packet_bound.go`,
  `packet_loop.go`, `batch.go`, `sendmmsg_linux_*.go`,
  `load_l3_linux_internal_test.go`), `tools/netgate/gates.go`.
