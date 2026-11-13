# ADR 0189 — a kernel capability is probed, reported, and switched off by name

- **Status**: Accepted
- **Date**: 2026-10-25
- **Accepted**: 2026-10-25, on the owner's decisions of 2026-10-24 for the
  unified network work, R4 (class A split into A-kernel, on by default, and
  A-code, opt-in; `MIN_TIMEOUT` moved to class B; the accept stays
  single-shot), R5 (the environment only restricts) and R6 (an option not
  honoured is refused, never folded back). It can be revisited while the SDK
  is v0 ([ADR 0040](0040-changing-a-published-shape-while-v0.md)).
- **Deciders**: SDK maintainers, owner
- **Amended 2026-11-10** (#856, #857): class A-code gains `reuseport-loops` (opted into by `Shards(0)` on a loop group), `loop-affinity` (by `LoopValue.Affinity`), `so-incoming-cpu` (by `Capabilities`, with Affinity and per-loop listeners) and `tcp-notsent-lowat` (by `Capabilities` or `LoopValue.NotsentLowat`); class B gains `tcp-fastopen`. A row whose opt-in is another option — `reuseport-loops`, and `reuseport-cbpf`, a datagram group's Steer — is refused in `LoopValue.Capabilities` with `LoopCapabilityRefused`. A LoopValue field out of range is `LoopValueInvalid` (`0.2.11.88`).
- **Amended 2026-11-11** (#979, #980): `busy-poll` joins class A-code, its window `LoopValue.BusyPoll` and `BusyPollBudget`; `uring-zcrx` stays class B with its 6.15 floor, its probe stage, `LoopValue.ZCRX` and its area designed; see the two amendments at the end.
- **Amends**: [ADR 0188](0188-the-operators-switches-only-restrict-and-state-says-what-the-server-runs-without.md)
  §D1 (the registry gains the kernel capabilities) and §Deferred (the optional
  capabilities join it here); [ADR 0186](0186-a-plaintext-group-may-run-on-an-event-loop-and-its-http-is-a-profile-that-refuses.md)
  §D5 (the staged probe, and `DEFER_TASKRUN`, until now a test hook)
- **Related**: [ADR 0187](0187-the-handler-type-chooses-the-engine.md)
  §D10 (gates at the merge), [ADR 0163](0163-the-sdk-is-designed-by-its-diagram-and-kit-writes-only-data-and-test-pins.md)
  (design first), [ADR 0018](0018-sdk-cross-platform-portability.md) (the
  uapi in-tree)

## Context

The loop engine already used most of what makes io_uring fast — multishot
receive, the provided-buffer ring, direct descriptors — and epoll used
`EPOLLEXCLUSIVE`, each decided inside the probe and invisible from outside.
Three things were missing before the next kernel features could be turned on:

- **A name per capability.** An operator whose kernel misbehaves under one
  feature had only `KITSUNIUM_NET_DISABLE=iouring`: every other feature went
  with it.
- **A report.** `State` said which backend ran and the probe stage it reached,
  never which feature the kernel had, lacked, or was told not to use.
- **A way in for the features not proven yet.** `DEFER_TASKRUN`,
  `NO_SQARRAY`, registered ring descriptors and `SQ_REWIND` each change how a
  ring is driven; none may become a default before its own conformance, and
  `DEFER_TASKRUN`, the one already tried as a hook, stalls a secondary ring
  whose completions only its own enter would post.

## Decision

### D1 — The capabilities are in the registry, by class

`design/net.yaml` declares, beside the engines of ADR 0188, three blocks of
`Capability` values, each named in `KITSUNIUM_NET_DISABLE`:

- **Class A-kernel**, on once the probe validates them: `uring-multishot-recv`,
  `uring-pbuf`, `uring-direct`, `epoll-exclusive`, `tcp-nodelay`.
- **Class A-code**, off until `LoopValue.Capabilities` opts in and the probe
  validates them: `uring-defer-taskrun`, `uring-no-sqarray`, `uring-ring-fds`,
  `uring-sq-rewind`.
- **Class B**, declared, not built: `uring-send-zc`, `uring-bundle`,
  `uring-pbuf-inc`, `busy-poll`, `uring-zcrx`, `reuseport-cbpf`,
  `tcp-defer-accept`, `uring-min-timeout`. One joins class A-code only once
  its package's BENCH.md shows the threshold its own ADR sets; until then
  `LoopValue.Capabilities` refuses it.

`KITSUNIUM_NET_ENGINE` takes an engine only: a kernel capability there is an
unknown token. An unknown token, in either variable, switches every optional
capability off (R5: "no optional capability"), each row saying so. The
parser's registry and `check-net-default-change.sh` read the same blocks.

### D2 — A switched-off capability is never probed

Server hands the leaf the capabilities the environment switched off
(`loop.Config.Disabled`); the probe never asks the kernel about one. Some
switches change the backend rather than a flag:

- `uring-pbuf` off: the io_uring backend receives into provided buffers only,
  so io_uring is ruled out and never set up — an override, like
  `iouring` off, never a fallback.
- `uring-multishot-recv` off: every receive is single-shot, re-armed by the
  state machine after each completion.
- `uring-direct` off: connections live in the process table; neither the
  self-test nor the release check registers a table.
- `epoll-exclusive` off: every loop's epoll is woken per connection.
- `tcp-nodelay` off: the listeners keep the kernel's default; the portable
  backend clears the netpoller's on each connection.

### D3 — The probe descends, on throwaway rings, under the thread's lock

The probe runs on the loop's own thread, locked from the first setup to the
last close, on rings of 16 entries it closes before serving:

1. **Setup, flag by flag.** Every flag the switches allow first; on `EINVAL`
   the newest is given up and the setup retried: `SQ_REWIND`, then
   `NO_SQARRAY`, then `DEFER_TASKRUN`, then `SINGLE_ISSUER` — so a flag is
   never dropped before one that requires it (REWIND⇒NO_SQARRAY,
   DEFER⇒SINGLE). Each flag given up is reported absent.
2. **Features**: `EXT_ARG`, `NODROP`, `SINGLE_MMAP` required, as before;
   `FEAT_MIN_TIMEOUT` read for class B's row.
3. **`REGISTER_PROBE`** (5.6) for every op issued. `REGISTER_QUERY` (6.15) is
   not used: every kernel the backend serves answers the probe, and the
   query adds nothing the backend reads.
4. **Ring descriptors**: the probe's ring is registered with the thread
   (`IORING_REGISTER_RING_FDS`) and unregistered before it closes.
5. **`SQ_REWIND`'s partial submit**: two NOPs filled from SQE 0, one
   submitted, the other moved down and submitted, each completing once.
6. **The self-test** on the primary's flags, then the release check, as
   ADR 0186 describes.

`EEXIST` is a defect of the probe, never an answer of the kernel: it is what a
`SINGLE_ISSUER` ring returns when entered off the thread that set it up, which
a probe locked to its thread never does. A reason naming it is reported as
`probe defect`, and the conformance suite fails on one.

### D4 — The class A-code capabilities, each with its conformance

- **P3a, `uring-defer-taskrun`**: `DEFER_TASKRUN` with `SINGLE_ISSUER` on the
  primary ring only, the one the thread waits on. The secondaries keep
  `COOP_TASKRUN` and post their own completions, which their registered
  eventfd wakes the primary for; a deferred secondary would post nothing until
  entered with `GETEVENTS`, which the loop never does on it. On the primary,
  every submit carries `GETEVENTS` with `min_complete` zero, from the owner
  thread. Its test: traffic on a secondary ring alone, the round trip's median
  under half a tick; with `DEFER_TASKRUN` on every ring the second
  round trip times out.
- **P3b, `uring-no-sqarray`**: a layout variant, no SQ array to fill;
  `sqOff.array` reads zero.
- **P3c, `uring-ring-fds`**: each ring registered with its thread, at most 16
  (`IO_RINGFD_REG_MAX`), the ones past entered by descriptor; unregistered
  before the ring closes, since a registered ring outlives its close and its
  direct descriptors with it. Its test: the group closed, the peer reads the
  end.
- **P3d, `uring-sq-rewind`**: `SQ_REWIND`, which needs `NO_SQARRAY`: the
  submitter fills from SQE 0, and a partial submit moves what is left down
  before the next. It stays class A-code until a kernel 7.x lane runs it.

An opted-in capability the kernel lacks is `missing`, and a fault in `State`
(`capability`): the group serves without it, and says so (R6 refuses an
option no backend honours, which `LoopCapabilityRefused` does; a kernel that
lacks a feature is not a wiring mistake, and a refusal would make a binary
built for 7.x fail to start on 6.8). The environment wins over an opt-in.

### D5 — The report

`LoopStateValue.Capabilities` (`[]CapabilityValue`: name, class, status,
reason) lists every capability of D1 the loop reports, then server's
`tcp-nodelay` row — its socket probe, read back from each listener. Status is
`on`, `available`, `absent`, `missing`, `disabled` or `unprobed`. The
portable backend reports every row `unprobed`, or `disabled`.

### D6 — Never SQPOLL, and the gate

`SQPOLL` is never set, and no capability names it. The uapi numbers this
change adds are frozen by `TestURingABI`. Each leg of `net-kernel-gate.yml`
requires every class A capability its kernel has (`requiredCaps`), fail-closed:
a leg whose kernel lacks one fails. No leg requires `uring-sq-rewind` before a
7.x lane exists.

## Consequences / Semantics

- `core/net` gains seventeen `Capability` constants, `CapabilityValue`,
  `LoopStateValue.Capabilities`, `LoopValue.Capabilities` and
  `LoopCapabilityRefused` (`0.2.11.60`). `net/loop` gains
  `Config.Disabled`, `Config.DisabledBy`, `CheckCapabilities` and `Unprobed`.
  `pkg/v1/net/server` re-exports `CapabilityValue` and the four class A-code
  constants.
- A server with neither variable set serves as before: the class A-kernel
  capabilities were already in use; only the report is new.
- Measured on Ubuntu 6.8.0-147 and Debian 6.12.113 (arm64): every class A
  capability present but `uring-sq-rewind` (`EINVAL` at setup), the release
  check choosing direct descriptors on both; `FEAT_MIN_TIMEOUT` advertised on
  6.12, not on 6.8; the secondary-only round trip's median 64 µs and 50 µs
  (90th percentile 114 µs and 189 µs) under `DEFER_TASKRUN`.

## Breaking changes

None to a published behaviour: the new fields are empty for a value built by
hand, and the switches only act when set. `DEFER_TASKRUN`'s test hook is gone,
replaced by the opt-in.

## Alternatives considered

- **Turn the class A-code capabilities on by default where the probe finds
  them.** Rejected (R4): each changes how a ring is driven, and
  `DEFER_TASKRUN` on every ring already showed a stall the suite had missed.
- **Refuse an opted-in capability the kernel lacks.** Rejected: see D4; the
  fault says what runs without it.
- **One setup with every flag, and give them all up on `EINVAL`.** Rejected:
  it reports nothing per flag, and an older kernel would lose
  `SINGLE_ISSUER` for not knowing `SQ_REWIND`.
- **`REGISTER_QUERY`.** Deferred: see D3.

## Amendment 2026-11-10 (kitsunium/sdk#971): three socket options on the listener

Three rows join the registry, each a listener option Linux copies into every
accepted socket (the inheritance `tcp-nodelay` already rests on), set once per
TCP listener before any accept and read back with `getsockopt` into the
leaf's report:

- **`tcp-keepalive`, class A-kernel**: `SO_KEEPALIVE` with `TCP_KEEPIDLE` and
  `TCP_KEEPINTVL` of `Config.KeepAlive` (zero is 15 s, the standard library's,
  so both engines agree; negative sets nothing, the row `available`) and
  `TCP_KEEPCNT` 9. A peer that vanished no longer keeps its slot, its buffer
  and its armed recv on a group with no `IdleTimeout`. **A default change**:
  every loop group's accepted sockets now carry keepalive.
- **`tcp-user-timeout`, class A-kernel**: `TCP_USER_TIMEOUT` (18, Linux
  2.6.37, absent from package syscall, declared in-tree) of
  `Config.UserTimeout`, in whole milliseconds, rounded up; zero sets nothing,
  the row `available`. Output the kernel cannot deliver ends the connection
  with an error instead of the whole `tcp_retries2` sequence.
- **`tcp-defer-accept`, from class B to class A-code**: opt-in through
  `LoopValue.Capabilities`; `TCP_DEFER_ACCEPT` of `Config.ReadTimeout` in whole
  seconds, rounded up (the kernel rounds it to its retransmission schedule), so
  a peer that connects and sends nothing takes no slot until it does or the
  bound runs out. Opted into with no read bound, the row reads `unprobed`.

A Unix listener takes none of them, and `TCP_NODELAY` is no longer tried on it:
the options are set on TCP listeners only, and a group with none reads each row
`unprobed`, reason "no TCP listener". What the loop leaf does not do, and the
rest of #971 asks of `net/server`: `TimeoutsValue.KeepAlive` and `UserTimeout`
as the public values behind `Config.KeepAlive` and `Config.UserTimeout`, the
rows in the operator's registry (`operator.go`) so that
`KITSUNIUM_NET_DISABLE=tcp-keepalive` is a known token, and the goroutine and
portable engines' keepalive and `TCP_USER_TIMEOUT`.

## Amendment 2026-11-10 (kitsunium/sdk#974): Unix listeners

`net/loop` refuses a unixpacket listener at `Start` with `LoopOptionUnsupported`
naming `network=unixpacket` (both backends read a connection as a stream into a
buffer that cuts a longer record without a flag), and a Unix listener no longer
tries `TCP_NODELAY` or the rows above. The loop's `LoopIO` is also a `LoopPeer`
(ADR 0039's sibling, ADR 0148): `Peer(ref)` returns the connecting process's
uid, gid and pid, `Verified`, from `SO_PEERCRED` on a process descriptor — epoll's,
and io_uring's once a Unix listener keeps the group in the process table
(`uring-direct` then reads `unprobed`). Not built: reading a direct descriptor's
credentials with `IORING_OP_URING_CMD`/`SOCKET_URING_OP_GETSOCKOPT` (6.7) and the
`uring-sock-getsockopt` row (class A-kernel) that would switch it, so a mixed
group pays the process table for its TCP connections too; `net/server`'s
`loopRefusal` and its false `tcp-nodelay` row on a Unix listener; the portable
backend's `Peer`.

## Amendment 2026-11-13 (kitsunium/sdk#974): uring-sock-getsockopt

`uring-sock-getsockopt` joins class A-kernel: `IORING_OP_URING_CMD` (46) with
`SOCKET_URING_OP_GETSOCKOPT` (2, Linux 6.7) reads `SO_PEERCRED` on a direct
descriptor, so a group with a Unix listener keeps direct descriptors. The
self-test proves it on a Unix socket in the direct table — one end of a pair,
installed by `IORING_OP_FILES_UPDATE` at an entry the kernel allocates
(`IORING_FILE_INDEX_ALLOC`, 5.19) —, which must answer this process's own pid,
before the release check (a TCP descriptor is no witness: Fedora's 6.14
answered it the length of a `struct ucred` and left every Unix peer unknown,
2026-11-13); a refusal, or `KITSUNIUM_NET_DISABLE=uring-sock-getsockopt`, puts
the group in the process table as before (`uring-direct` `unprobed`, the stage
`self_test+fds`), and `Peer` asks the socket there. On direct descriptors a Unix
connection is accepted, its credentials read by the command into a cell of the
ring kept per direct-descriptor index — never on a stack, which may move while
the command is in flight —, and only then opened: `OnOpen` and every byte come
after the peer is known, a TCP connection of the same group opens at once and
answers false. A command still in flight when the loop stops lets its slot go
unopened, so no `OnClose` is called for a connection no `OnOpen` announced. The
row reads `on`, `absent` with the kernel's errno, `available` when the release
check chose process descriptors, `unprobed` without a Unix listener or with
`uring-direct` switched off.

## Deferred

- Each class B capability, with its ADR and its BENCH.md threshold.
- `uring-sq-rewind` in class A-kernel, after a kernel 7.x lane.
- A class A-code capability in class A-kernel, each with its own
  `net:default-change` pull request.

## References

- Linux `include/uapi/linux/io_uring.h` (`IORING_SETUP_NO_SQARRAY` 1<<16,
  `IORING_SETUP_SQ_REWIND` 1<<20, `IORING_ENTER_REGISTERED_RING` 1<<4,
  `IORING_REGISTER_RING_FDS` 20, `IORING_FEAT_MIN_TIMEOUT` 1<<15),
  `io_uring/tctx.c` (`IO_RINGFD_REG_MAX`, `-EEXIST` for a `SINGLE_ISSUER`
  ring off its task), `io_uring/io_uring.c` (`SQ_REWIND` requires
  `NO_SQARRAY`, refuses `SQPOLL`).
- Impl: `design/net.yaml`, `internal/core/net/capability.go`,
  `internal/service/net/loop/` (`caps.go`, `uring_caps_linux.go`,
  `uring_ring_linux.go`, `uring_probe_linux.go`, `epoll_linux.go`),
  `internal/service/net/server/` (`operator.go`, `loop_engine_linux.go`,
  `loop_portable.go`, `stall.go`), `scripts/ci/check-net-default-change.sh`,
  `.github/workflows/net-kernel-gate.yml`.

## Amended 2026-11-10 — a datagram socket's drop count (kitsunium/sdk#973)

`udp-rxq-ovfl` joins class A-kernel: SO_RXQ_OVFL, set on every batched UDP socket
before its first read unless `KITSUNIUM_NET_DISABLE` names it, has the kernel
attach to each message the socket's cumulative count of what it refused to queue.
`DatagramStateValue.KernelDrops` sums the growth of that count (modular: the
kernel's counter is 32 bits and wraps) and `DatagramStateValue.Capabilities`
carries the row; the reader's control room holds that message beside GRO's. The
count is the kernel's, of what it did not queue: under UDP GRO a coalesced message
is one drop whatever datagrams it carried, so it stands below the datagrams lost.
L3 reports it as `kernel-drops/op` beside `lost/op` and refuses a round in which
it passes the loss (a check on the two, no threshold). Elsewhere than Linux the
row reads absent.

## Amendment 2026-11-10 (#971, the goroutine and portable halves)

`TimeoutsValue.KeepAlive` and `TimeoutsValue.UserTimeout` carry the rows' values
to the engines that are not the loop: the options `KeepAlive` and `UserTimeout`
(zero the standard library's 15 s and the write bound, respectively). The
goroutine engine and the portable backend set keepalive through the listener's
`ListenConfig` and per accepted connection, `TCP_USER_TIMEOUT` on the listener
before bind, and `KITSUNIUM_NET_DISABLE=tcp-keepalive` and `tcp-user-timeout`
switch them off there as they do on the loop.

## Amendment 2026-11-11 (#852): uring-multishot-recvmsg

`uring-multishot-recvmsg` (`CapabilityURingRecvmsg`) joins class A-kernel,
after `udp-pktinfo` in the registry: a datagram lane of a group
`PacketGroup.HandleLoop` serves is on io_uring once its probe passed — the ring,
RECVMSG, SENDMSG, ASYNC_CANCEL and POLL_ADD in REGISTER_PROBE, the provided-buffer
ring, then a loopback multishot RECVMSG that must come back with a buffer and
F_MORE. The row is `DatagramStateValue.Capabilities`', present only once such a
group started: on when every lane serves on io_uring, absent naming the first
refused stage (`setup`, `probe`, `buf_ring`, `recvmsg`, `arena`, `socket`,
`eventfd`, `dup`) and raising no fault, unprobed where the platform has no lane
(stage `arch`), disabled under `KITSUNIUM_NET_DISABLE=uring-multishot-recvmsg`,
`uring-pbuf`, or an engine capped below io_uring — never probed then.

## Amendment 2026-11-11 (#969): uring-send-zc is class A-code

`uring-send-zc` leaves class B for class A-code before its threshold is
measured, opt-in only, so the code path exists for the gate to measure it: a
group that names it in `LoopValue.Capabilities` probes, after the bundle probe,
REGISTER_PROBE for `IORING_OP_SEND_ZC` (47, Linux 6.0), then one zero-copy send
of a datagram on loopback on a ring of its own, which must complete with
`IORING_CQE_F_MORE` and then its notification (`IORING_CQE_F_NOTIF`, 1 << 3);
`failStage=send_zc` plays a refusal. On, a flight of at least
`LoopValue.SendZCMin` bytes (zero selects 16 KiB, a negative value
`LoopValueInvalid`) leaves zero-copy, every send of it, a short one's rest
included; a shorter flight leaves by `IORING_OP_SEND` as before. The send and
its notification are two operations of the slot (`ops`), and `zcPend` counts
the notifications a flight still waits for: a flight sent whole is held — its
slab chunk not freed, `fSending` kept, the slot not closed — until the last
one, in whichever order the two CQEs come; a send's CQE without `F_MORE` takes
its notification back. One flight at a time a connection, as before: the next
waits for the previous one's notification, which on TCP may come only with the
peer's ACK. Not built: registered buffers (`IORING_REGISTER_BUFFERS2` sparse
over the slab's arenas, the issue's `IORING_RECVSEND_FIXED_BUF`) — each send
pins its pages —, and the gate: `BenchmarkLoadL1/pair=loop:loop-sendzc` at
64 KiB bodies, its BENCH.md rows and the `tools/netgate` threshold that would
move the row on to class A-kernel. Off and refused stay as for every class
A-code row: `KITSUNIUM_NET_DISABLE=uring-send-zc` reads disabled and never
probes, an opted-in row the kernel refuses reads missing, a capability fault.

## Amendment 2026-11-11 (#979): busy-poll joins class A-code

`busy-poll` leaves class B for class A-code: opted into through
`LoopValue.Capabilities`, switched off by `KITSUNIUM_NET_DISABLE=busy-poll`, never
on by default. Two `LoopValue` fields carry it: `BusyPoll`, the window (zero 50 µs,
the value of `Documentation/networking/napi.rst`'s examples, held until the gate
below measures another; negative or past one second `LoopValueInvalid`), and
`BusyPollBudget`, epoll's packets a poll (zero the kernel's; past 65535
`LoopValueInvalid`, past 64 the kernel's `EPERM` without `CAP_NET_ADMIN`, the row
then missing).

- **Floor.** Both levers are Linux 6.9 — not 6.8, which the issue named:
  `IORING_REGISTER_NAPI` = 27 with `struct io_uring_napi` (16 bytes; 6.13 renames
  `pad[0]` `opcode` and half of `resv` `op_param`, zero for the dynamic tracking
  asked here), and `EPIOCSPARAMS` = `_IOW(0x8A, 1, struct epoll_params)` (8 bytes),
  whose direction bits differ on powerpc and mips (`busypoll_ioc*_linux.go`).
  `TestURingABI_napi` and `TestBusyPoll_epollABI` freeze them.
- **Probe (§D3).** A stage after the receive bundle's: a zero window registered on
  a throwaway ring (`EINVAL` before 6.9, reason `register_napi: …`), zero
  parameters set on a throwaway epoll instance (`ENOTTY`, reason `epiocsparams: …`);
  `failStage: "busy_poll"` plays both. A switched-off row issues neither call.
- **Engine.** io_uring registers the window with `prefer_busy_poll` on ring 0 of
  each loop, the ring its thread waits on: a ring nobody waits on never polls. It
  is never unregistered — the ring's close frees it. epoll sets the window, the
  budget and `prefer_busy_poll` on each loop's own instance. `SO_BUSY_POLL` (46) is
  not set: it governs blocking reads and `poll(2)`, not these waits. The portable
  and goroutine engines cannot reach the runtime's own epoll: the row reads
  unprobed there.
- **Report.** On, the reason states that only sockets the kernel gave a NAPI id are
  polled — none on loopback nor a veth without GRO. `SO_INCOMING_NAPI_ID` is not
  read: on io_uring the connections are direct descriptors no `getsockopt` reaches.
- **Gate, not built.** The row leaves class A-code only through a
  `net:default-change` pull request citing a paired `BenchmarkSparseKeepAlive`,
  loop against loop with busy-poll, on the veth lane with GRO switched on
  (`ethtool -K … gro on`, which `scripts/ci/net-veth.sh` does not do yet), ADR
  0190's paired ratio: the request's p99 at most 0.85 of the plain loop's and
  cpu-ns/req at most 1.30 times it, both on the upper bound of the interval. No such
  row exists; `internal/service/net/loop/BENCH.md` says so.

## Amendment 2026-11-11 (#980): uring-zcrx's floor, its probe stage and its area

`uring-zcrx` stays class B — `LoopValue.Capabilities` refuses it — but no longer
gives the generic sentence: its row names the stage that stops it. What can be
checked on the SDK's hosts is built; the receive path is designed here and not
built, since no host of the SDK's has run it.

- **Floor.** Linux 6.15: `IORING_OP_RECV_ZC` = 58, `IORING_REGISTER_ZCRX_IFQ` = 32,
  `struct io_uring_zcrx_ifq_reg` (96 bytes: `if_idx`, `if_rxq`, `rq_entries`,
  `flags`, `area_ptr`, `region_ptr`, `offsets`, `zcrx_id`), `struct
  io_uring_zcrx_area_reg` (48), `struct io_uring_zcrx_rqe` (16: `off`, `len`),
  `struct io_uring_zcrx_cqe` (16, a CQE's second half), `struct
  io_uring_region_desc` (64), `IORING_ZCRX_AREA_SHIFT` = 48, `IORING_SETUP_CQE32` =
  1<<11. `TestURingABI_zcrx` freezes them, the 32-byte CQE included; they were
  written from the 6.15 header, which the SDK's hosts (6.12) do not carry. A
  `dma-buf` area (`IORING_ZCRX_AREA_DMABUF`, 6.16) is out of scope.
- **Prerequisites.** The receiving ring carries `DEFER_TASKRUN` and `CQE32`: in this
  loop, ring 0 of each loop under the `uring-defer-taskrun` opt-in, its CQ stride
  32 bytes, the secondaries keeping 16. The host carries a driver with the queue
  API and header/data split (bnxt and gve at 6.15), header split switched on
  (`ethtool -G <if> tcp-data-split on`), one receive queue steered to the group's
  port by an ntuple rule the operator sets, and `CAP_NET_ADMIN`.
- **Configuration.** `LoopValue.ZCRX` (`ZCRXValue`: `Interface`, `Queue`,
  `AreaBytes`, 4 MiB by default, rounded up to pages); a zero `Interface` keeps the
  row off (ADR 0031); a negative field is `LoopValueInvalid`.
- **Probe (§D3), stage `zcrx`, after `busy_poll`, on loop 0 only** (two loops would
  register one queue at once): `REGISTER_PROBE` on a throwaway ring for opcode 58 —
  absent, `kernel: IORING_OP_RECV_ZC unsupported (before 6.15)`; present with no
  interface — available, `available: no interface configured`; an interface named —
  the queue registered on a throwaway ring set up `SINGLE_ISSUER|DEFER_TASKRUN|CQE32`
  on the loop's locked thread, with an area of `AreaBytes` and a refill ring of 128
  entries in one page, both mapped there, then closed: absent with the kernel's
  answer (`ENODEV`, `EPERM`, `EINVAL`, `EOPNOTSUPP`), or available, `accepted on a
  throwaway ring`. Registering restarts the NIC queue, and so does the release:
  naming a queue is the operator's request for that. Each reason ends with the
  class B sentence; epoll and the portable backend read unprobed. The suite plays
  the stages past the opcode (`zcrxKernel`, `zcrxErrno`, `failStage: "zcrx"`); on
  6.12 the register's own call only meets `EINVAL`, an opcode the kernel lacks.
- **The area and the refill ring (designed, not built).** One area a loop, mapped
  outside the Go heap as the slab's arenas are (ADR 0186 §D7) and registered on ring
  0 with its refill ring. Each connection of ring 0 arms a multishot `RECV_ZC`; a
  completion's `off` names its area above bit 48 and its offset below, `res` its
  length, and `OnData` receives those bytes of the area. They stay valid until
  `OnBatchEnd`, after which the loop writes one `rqe {off, len}` a completion into
  the refill ring and publishes its tail, as `returnBuffers` hands bids back today;
  a refill ring full defers the rest to the next batch. A packet that missed the
  steered queue reaches the same area through the kernel's copy fallback, so the
  adapter sees one path. The secondaries and epoll keep provided buffers, and the
  row's reason names which rings receive without a copy.
- **Promotion.** The row joins class A-code only through a pull request labelled
  `net:default-change` citing a `BenchmarkLoadL1/pair=loop:loop-zcrx` row of ADR
  0190's paired gate, run on a host of the class above, that reports cpu-ns/req and
  the receiver's copied bytes a request: the zcrx side's cpu-ns/req at most 0.85 of
  the plain loop's on the interval's upper bound, and its copied bytes a request at
  most a tenth of the plain loop's. Out of reach today: CI's veth (no queue API),
  Docker (no `CAP_NET_ADMIN`), the secondaries (no `DEFER_TASKRUN`), every kernel
  before 6.15 — this VM's 6.12 among them.

## Amendment 2026-11-13 (#969, #979): a zero-copy send refused at send time; the two gates' loads

**`uring-send-zc` refused by the socket or by the memory limit.** The probe
sends a datagram zero-copy on a UDP socket of its own; a connection's socket can
still refuse `IORING_OP_SEND_ZC`, and before this amendment its refusal failed
the connection. Two refusals are now a fallback, not a failure: `EOPNOTSUPP`,
from a socket whose kernel object lacks `SOCK_SUPPORT_ZC` — a Unix socket, and
every socket accepted from an MPTCP listener, which Go's `net.Listen` opens by
default on Linux since Go 1.24 (`net`'s `defaultMPTCPEnabledListen`) —, after
which every ring of the loop — its listeners are theirs too — sends every later
flight by `IORING_OP_SEND`; and `ENOMEM`, the
pages a zero-copy send pins being charged to `RLIMIT_MEMLOCK` (io_uring's
notification accounting), which a load of flights waiting for their peers' ACKs
reaches — 1 000 connections of 64 KiB do past the usual 8 MiB —, after which
only that flight does. Nothing of the refused send left: the flight is held
until its notifications came, then its rest leaves by opcode 26; a flight whose
pending notifications reach 255, the count's bound, is held the same way.
`TestSendZC_refusedBySocket` (a Unix and an MPTCP listener) and
`TestSendZC_refusedForMemory` (`RLIMIT_MEMLOCK` lowered to zero after Start) fail
on the code before it, the peer reading EOF at byte 0. The row stays `on` after
such a refusal: the probe's verdict is the kernel's, and a socket refusing is
the socket's. A server group's own listener being MPTCP by default, a group
that opts into `uring-send-zc` today sends zero-copy only where its listener
was made plain TCP (`GODEBUG=multipathtcp=0`, or a listener adopted): whether
the server should listen with plain TCP for such a group is the owner's to
decide.

**The gates' loads are built.** `BenchmarkLoadL1big/pair=loop:loop-sendzc`
(`internal/service/net/server/load_big_linux_test.go`: 64 KiB bodies, the child
listening with plain TCP) runs in net-kernel-gate's `bench` job and
`tools/netgate`'s `send-zc` row reports it against 0.9; measured locally on
loopback it costs 1.033–1.114 times the copy (edition 2026-11-13 of that
package's BENCH.md), so the row stays class A-code. `failStage=send_zc` reads
`missing`, a capability fault, as every refused opt-in does since the
2026-11-11 amendment: #969's "absent with no fault" is this ADR's §D4 rule
applied, not a defect. `BenchmarkLoadSparse/pair=loop:loop-busypoll`
(`load_sparse_veth_linux_test.go`) pairs the sleeping loop with the busy-polling
one over the veth pair, its far end given a NAPI instance by GRO
(`scripts/ci/net-veth.sh gro`), each engine's CPU a request and 99th percentile
reported; `tools/netgate`'s `busy-poll` row reports both against 1.0 until this
row's threshold is set from them.

## Amendment 2026-11-13 (#969): a zero-copy group listens with plain TCP

The owner decided the question the amendment above left open: **a loop group
that opts into `uring-send-zc` listens with plain TCP**. Its listeners are bound
with `net.ListenConfig.SetMultipathTCP(false)`, Go's default Multipath TCP
switched off for that group alone, when the operator has not switched the row
off and its plan may run io_uring — `auto` or `io_uring`
(`plainTCPListens`, `internal/service/net/server/multipath.go`). Every other
group keeps the standard library's default, MPTCP included where the kernel has
it: a loop group that did not opt in, the goroutine engine, and an opted-in
group the operator gave back — `KITSUNIUM_NET_DISABLE=uring-send-zc`, or
`KITSUNIUM_NET_ENGINE` capping it at epoll or the portable backend, neither of
which sends zero-copy. Without it the opt-in did nothing: every socket accepted
from Go's default listener refused `IORING_OP_SEND_ZC` (`EOPNOTSUPP`) and the
loop sent every later flight by opcode 26.

**State says so.** `ListenerStateValue.MultipathTCP` reports, on every stream
listener's row, whether the socket is MPTCP, read back from it (`SO_PROTOCOL`
is `IPPROTO_MPTCP`, Linux) after bind or adoption, and false on every other
platform, for a Unix socket and for a datagram one: an opted-in group's rows
read false, the others' what Go's default gave. A listener a supervisor handed
over keeps the protocol it was bound with; an adopted MPTCP listener's
connections still refuse, and the loop then sends plainly as above.

`TestMultipathTCP_sendZCGroupListensPlain` (server) reads the rows of an
opted-in group (false), of a loop group and a goroutine group without the
option, of an opted-in group the operator switched off and of one capped at
epoll (each what Go's default listener reads here, MPTCP on this host), and
fails without the change (`MultipathTCP = true, want false`);
`TestPlainTCPListens` pins the decision on every platform;
`TestSendZC_goListener` (loop) serves a listener of `net.ListenConfig` with
MPTCP off, as server binds the group's, and sees every flight of an 8 MiB answer
and the two behind it leave by opcode 47 and none by opcode 26, while its
control, Go's default listener, reproduces the code before: one refused
`SEND_ZC`, then plain sends only. `BenchmarkLoadL1big` keeps its child's
`GODEBUG=multipathtcp=0`, now for the pair's other engine, `loop`, so the two
differ by the send alone.
