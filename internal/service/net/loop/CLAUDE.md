<!-- updated: 2026-11-02T18:00:00Z -->
# internal/service/net/loop/

## Purpose

The network domain's event-loop engine (ADR 0186): the native backends a
plaintext stream group runs on when a `corenet.LoopHandler` serves it:
io_uring on `linux && (amd64 || arm64 || riscv64 || loong64)` after a staged
probe of the running kernel, and epoll on every Linux architecture and after
any probe failure; kqueue on darwin, FreeBSD, OpenBSD, NetBSD and DragonFly
after its own staged probe (ADR 0222) — `server` hands it kqueue only when the
code names it, its `auto` staying portable there until the paired gate holds
(ADR 0222, amended 2026-11-11). The
portable backend is not here: it needs the netpoller and `net.Conn`, so it lives
in `server` (`loop_portable.go`). `server.Group.HandleLoop` is the one way in
(§D9 step 11); nothing of this package is published itself. Beside the
stream groups, `StartPacket` runs one io_uring lane of a datagram group a
`corenet.PacketLoopHandler` serves (#852, `server.PacketGroup.HandleLoop`).

## Contents

| File | What |
|---|---|
| `group.go` | `Listener` (the one hand-off: a duplicated descriptor, `Loop` naming the only loop that accepts on a shard, #856), `checkShards`, `Config`, `Group` (`Shutdown`, `Close`, `State`, `Rejected`, `Live`) and `Start`, which resolves the sizing, starts the backend and writes the one start line |
| `sizing.go` | `resolve`: the defaults, `ResolveLoops` (`LoopsAuto` read as GOMAXPROCS less one, at least one, which server calls once per group, zero one, the source State reports — ADR 0196 §D1, ADR 0199), `batchWaitOf` (the grouped wait's bound and count) and `yieldFor` (the yield on only while GOMAXPROCS exceeds the loops), the `LoopNoDescriptors` refusal of a zero `RLIMIT_NOFILE`, and the one ring formula (explicit, else ⌈max(MaxConns, SoftConns) / min(RLIMIT_NOFILE, IORING_MAX_FIXED_FILES)⌉, else 1) |
| `conn.go` | `connCtl`, one slot's pointer-free control state; its states, flags and close reasons |
| `core.go` | the backend-neutral core: `ring` (slot table grown by `slotCap`: doubling to 2048 slots, then 2048 at a time up to the table — ADR 0201 §D2, free list, LRU, dirty and compaction lists, timers), `loop` (the `corenet.LoopIO` implementation; `Loop` is its index, set at build — its lane, ADR 0191: `server` calls the chain's `LoopStarter.OnStart(Loops)` before `Start`, so nothing here does), `driver` (what a backend does: flush, shutdown, unread), the spill, the double-buffered output, the close and eviction rules |
| `slab.go` | the mmap'd slab: power-of-two size classes from 2 KiB, arenas mapped lazily, a budget a loop that sheds load by name; `shrink` (#809), once a tick (`loop.shrinkSlab`), unmaps every wholly free arena of a class but one, `shrinkPerTick` (8) at most, highest first, an unmapped arena a hole the next `grow` fills; `reclaimable`, what keeps an epoll or kqueue loop with no connection on the tick; `release` at teardown. `slabBudgetFor` (`sizing.go`) resolves the budget: `LoopValue.SlabBudget`, else a quarter of `Config.Memory`'s cgroup allowance shared by the loops (at most 128 MiB, at least one connection's largest chunk), else 128 MiB, its source in State |
| `wheel.go` | the hashed timer wheel, index-linked, one timer per slot, 10 ms ticks |
| `uring_peer_linux.go` | io_uring's direct-descriptor peer (#974): `prepPeerCred` (`IORING_OP_URING_CMD` 46, `SOCKET_URING_OP_GETSOCKOPT` 2), `askPeer`/`onPeer` (the `stPeer` slot opened once its credentials are read), `directPeer`, `unixListeners` |
| `peer_linux.go` | `LoopPeer` (#974, ADR 0148): `peerOf` reads `SO_PEERCRED` on a Unix socket with no allocation (the standard library's `GetsockoptUcred` returns a heap pointer; a TCP socket answers zeros, so `SO_DOMAIN` is checked first), `unixListener` |
| `listener_linux.go` | the listener options of ADR 0189's rows (#971): `prepareListeners` sets `TCP_NODELAY`, `tcp-keepalive`, `tcp-user-timeout` and `tcp-defer-accept` on every TCP listener before any accept and reads each back into the report; `shardRow` reads `SO_REUSEPORT` back on the per-loop listeners into `reuseport-loops` (#856) |
| `affinity_linux.go` | `loop-affinity` (#857): `loop.pin` sets the locked thread's mask to `LoopValue.Affinity[i]` (`sched_setaffinity`, a 8192-bit mask), before the loop reports ready; `reportAffinity` reads every loop's answer into the row |
| `caps.go` | the kernel capabilities a group reports (ADR 0189): `loopCaps` and their classes, `CheckCapabilities` (an engine, a class B capability, an unknown opt-in or one of `optedElsewhere` — `reuseport-loops`, Shards(0)'s, and `reuseport-cbpf`, a datagram group's Steer's — is `LoopCapabilityRefused`; `checkValue` refuses an Affinity that is not one CPU a loop, from 0 to 8191, and a negative NotsentLowat with `LoopValueInvalid`), `capSwitches` (the operator's `Disabled`, the code's opt-ins: the operator wins), `capReport` (one row each, `disabled` never overwritten, an absent opt-in `missing`), `Unprobed` (the portable backend's report) |
| `busypoll.go`, `busypoll_uring_linux.go`, `busypoll_epoll_linux.go`, `busypoll_ioc*_linux.go` | busy-poll, class A-code (#979): `busyPollOf` (the window rounded up to µs, 50 µs by default; the budget), `busyPollField` (`LoopValueInvalid` past a second or 16 bits); `probeBusyPoll` (`IORING_REGISTER_NAPI` on a throwaway ring, then on ring 0 opted in), `probeBusyPollEpoll` and `epollBusy.apply` (`EPIOCSPARAMS` on a throwaway instance, then on each loop's, `prefer_busy_poll` set); the ioctl's direction bits per architecture |
| `zcrx.go`, `zcrx_uring_linux.go` | uring-zcrx, class B (#980): `zcrxField` (`LoopValueInvalid`), `zcrxOf` (4 MiB, page-rounded); the 6.15 ABI (`opRecvZC`, `regZCRXIfq`, `setupCQE32`, the register structs); `probeZCRX` on loop 0 (the opcode on a throwaway ring, then, an interface named, `zcrxRegister`: the queue on a `SINGLE_ISSUER|DEFER_TASKRUN|CQE32` ring of its own, closed at once); no `RECV_ZC` is ever submitted |
| `uring_caps_linux.go` | the probe's stages 1 to 3 (ADR 0189 §D3): `openDescending` (every flag the switches allow, then on `EINVAL` `SQ_REWIND`, `NO_SQARRAY`, `DEFER_TASKRUN`, `SINGLE_ISSUER` given up in that order), `probeRingFDs` (register, unregister before close), `rewindSelfTest` (the partial submit), `settle` (the primary's and the secondaries' flags: `DEFER_TASKRUN` on ring 0 only), `reportSelfTest`, `registerRing` (16 a thread), `probeReason` (`EEXIST` is a probe defect) |
| `kqueue_bsd.go` | the kqueue backend (ADR 0222): epoll's shape on the shared core — one locked thread, one kqueue, one ring a loop, a 2 MiB mmap'd arena —, level-triggered filters (`EV_CLEAR` would need a read to `EAGAIN`), `EVFILT_READ` and a disabled `EVFILT_WRITE` registered at accept so every change is an enable or a disable, `fdSlot` mapping a descriptor to its slot (kqueue names no slot), a batch's connection events served before its listeners' accepts so no event names a connection accepted in it, `EV_EOF` failing a connection that no longer reads only with the socket error in `fflags`, a non-blocking pipe as the wake on every BSD, `unread` a one-byte `MSG_PEEK`; `probeKqueue`'s stages `kqueue`, `kqueue-wake`, `kqueue-socket` (a socket pair, not a loopback accept: a jail may have no loopback), the conformance hook `failStage` honoured. No `fileSender`, no `peerer`: `LoopFileWriter` refuses, `Peer` answers false |
| `group_bsd.go`, `accept_darwin.go`, `accept_bsd4.go`, `keepalive_*.go`, `listener_bsd.go`, `affinity_bsd.go`, `debug_bsd.go`, `drain_take_bsd.go` | kqueue's platform half: `startNative` (auto and kqueue probed then started, io_uring and epoll `UnsupportedPlatform`), `RLIMIT_NOFILE` signed or not, `MAP_ANON`, `pipeCloexec` (no `pipe2` on darwin: close-on-exec under the fork lock); accept with `accept4`, or under the fork lock on darwin, with `SO_NOSIGPIPE`; the rows: `tcp-nodelay` and `tcp-keepalive` on the listener (the keepalive's parameters where the platform names them, OpenBSD none), `tcp-user-timeout`, `tcp-defer-accept`, `so-incoming-cpu` Linux's and `tcp-notsent-lowat` not built, each absent or missing when opted into; `loop-affinity` missing when `Affinity` names a CPU; `loopdebug` compiled out (no thread identifier in package `syscall`, OpenBSD's pinned system calls refuse a raw one); `takeAfter` without `ENONET` |
| `listener_native.go` | what the listener options share on Linux and the BSDs: `listenerRow`, `tcpListener`, `shardRow` (`SO_REUSEPORT` read back, the per-platform value), `capReport.apply`, `refuseSeqpacket` |
| `drain_native.go` (`drain_take_linux.go`) | the drain's shared parts (ADR 0196 §D3): `listenerSet` (each taker — a loop on epoll, a ring on io_uring — hands each listener back once it took its queue; the last closes it, `closeRest` the others once every loop returned; it also counts each listener's accepts, every ring together, for the watchdog — ADR 0200 §D1), `drainable` (no partial input, and heard, or silent for `drainQuiet`), `queueTakeMax` |
| `*_bench_test.go` | every Benchmark of the package, by file name for `check-bench-md.sh` (#981): `loop_bench_test.go` (`Post`, `Wheel`, `Slab`, `Write`; `post_wake_linux_test.go` adds the eventfd wake), `batch_cost_bench_test.go`, `uring_cost_bench_test.go`, `bundle_/budget_/fill_/pbufinc_uring_bench_test.go`; their numbers are `BENCH.md` |
| `postq.go` | the `Post` queue: preallocated, mutex, `pending` CAS false→true wakes once per drain |
| `stats.go` | the per-ring counters (plain on the loop, atomic for `State`, published at the end of each batch) and the batch-duration histogram behind `BatchP99`. #975 adds `BytesIn` (`deliver`, `deliverSpill`), `BytesOut` (`wrote`: epoll's write, io_uring's `sendProgress`, a short send counted as it left), `OutputRefused` (`refuseOutput`, Write's three refusals), `SpillClosed` and `IdleClosed` (`countClose`), `Timeouts` (`expire`) and the gauge `PendingOutput` (Write adds, `wrote` subtracts, `finish` subtracts the unsent `out`, `release` the unsent `flight`: a closed connection owes nobody), and per loop `SlabInUse`/`SlabMapped` published at `endBatch`, summed with `SlabBudget` in `Group.State`; plain fields on the loop's thread, the atomics stored at a batch's end and only when the counter moved (`storeU`, `storeI`, against `ring.seen`): an atomic store is a full barrier, and storing the new counters as the old ones were cost a batch +22 % (`BenchmarkCore_batch`, benchstat, n=10), where storing what moved costs it 30 % less than the base. The portable backend fills the same fields from its lanes (server's) |
| `epoll_linux.go` | the epoll backend: one thread, one epoll instance, one ring per loop; `EPOLLEXCLUSIVE` listeners; a 2 MiB mmap'd read arena per loop |
| `uring_abi_linux.go` | the io_uring uapi in-tree (no `x/sys`, ADR 0018): syscall numbers 425–427, flags, opcodes, `sqe`/`cqe`/`uringBuf`/`uringParams`/… layouts, `fixedFd` (0-based) and `targetIndex` (index + 1), `FILE_INDEX_ALLOC` raw `0xFFFFFFFF`; one `Syscall6` per waiting enter, `RawSyscall6` to submit only |
| `uring_ring_linux.go` | one io_uring: `SETUP_CLAMP`+`CQSIZE` with the granted sizes read back, the single mapping, the SQ/CQ views, the bounded submit (EINTR again; EBUSY/EAGAIN counted and left to the next batch), `flushOverflow` (an overflowed CQ moves into the ring only on an enter with GETEVENTS), the provided buffers (as many 2 KiB buffers as the table rounded up, 8192 to 65536, mapped at setup; 512 handed to the kernel at setup, more by `growBufs` as `uRing.provide` asks — ADR 0201, ADR 0203 §D2), their groups of 8192 (`pbufGroup`, registered as group 1+g when their first bid goes out) and each group's tail published by one 32-bit store of entry 0's bid and the tail, the sparse table, `REGISTER_PROBE`, the SQE encoders |
| `uring_loop_linux.go` | the io_uring loop: one locked thread owns every ring; the primary waits (EXT_ARG, the tick is ETIME; grouped by `waitArgs` once a batch reaped four or the arrivals come two within the bound, ADR 0199, ADR 0202), the secondaries signal it through one registered eventfd it polls multishot, the `Post` eventfd is another one; connection slot apart from kernel index (`slotOf`), user_data = op \| gen(24) \| slot; the multishot state machine; the close: shutdown → count → `close_direct`, a user_data backstop after 1 s (ADR 0200); ENOBUFS re-arms; accept paused under the reserve and resumed at twice it; the rings filled in order (`fillStep`, ADR 0203 §D1); the accept watchdog, judged per listener |
| `uring_probe_linux.go` | the last probe stage: a loopback multishot accept into a direct descriptor and a multishot recv through the buffer ring, on a ring of its own, then the release check (shutdown, the recv's end, then close_direct must show the peer the end; cancel→close without the shutdown tells whether puts are deferred) |
| `uring_none_linux.go` | the Linux architectures outside io_uring's wave 1: `LoopBackendUnavailable`, epoll serves |
| `group_linux.go`, `group_other.go` | backend choice, `RLIMIT_NOFILE`, `mmap` (kqueue refused on Linux, `UnsupportedPlatform`); elsewhere but darwin and the BSDs, `UnsupportedPlatform` |
| `packet.go`, `gen_ports.go` | the datagram lane's hand-off (#852): `PacketConfig` (the socket's duplicated descriptor, the lane's index, the ceiling, the handler, `PacketCountersValue` — server's counters, added once a batch), the lane's states, and the `PacketLane` port (`Handover`, `Stop`, `Done`) `StartPacket` returns |
| `packet_uring_linux.go` | the io_uring datagram lane: one locked thread, one ring of 256 SQEs (`COOP_TASKRUN`+`SINGLE_ISSUER`, plain on EINVAL), the probe's stages (`setup`, `probe` — RECVMSG, SENDMSG, ASYNC_CANCEL, POLL_ADD —, `buf_ring`, `recvmsg`, a loopback multishot RECVMSG that must come back with a buffer and F_MORE, then `arena`, `socket`, `eventfd`; `startPacket`'s `failStage` plays each, `PacketConfig` carrying no injection), the multishot RECVMSG into 128 buffers of an `io_uring_recvmsg_out`, 32 bytes of name, 128 of control and 64 KiB of payload (`splitRecvmsgOut`, `control`: UDP_GRO's segment, SO_RXQ_OVFL's growth), `deliver` (a GRO train split, a cut one or one past the ceiling counted and dropped), `Reply` into a 1 MiB mapped arena and 1024 SENDMSG records, `flush` (a run to one peer of one size, the last shorter, ≤ 64 datagrams and 65 507 bytes, as one UDP_SEGMENT send — `runEnd`, `extendsRun`; EIO turns GSO off, EMSGSIZE counted), the arena reset once nothing is queued or in flight (`settle`), the wake eventfd polled multishot, the handover (cancel, `recvmsg(2)` to EAGAIN, replies, GRO off) |
| `fanwake.go`, `uring_fanwake_linux.go` | #703, ADR 0199 amended 2026-11-11: whether a secondary ring's completion wakes the primary's wait. `slowFanWake` probes once a process, on the first io_uring loop with a secondary, before the group reports (`probeFanWake`: two rings as the loop's, the secondary's CQ notifying an eventfd the primary polls, a pipe written 2 ms into the primary's wait; later than half a tick, or never within 50 ms, is `slow`); `fanTickFor` bounds the plain wait of a loop with secondaries to `fanWakeTick` (1 ms) where slow, the tick otherwise, and `plainWait` keeps that bound only while a secondary holds a slot or an armed accept, so an idle loop waits the tick; `watchFan` (second amendment) judges the loop's own waits: three plain waits in a row of half a tick or more (an empty grouped window's plain tail included) after which the batch reaped a secondary's completions bound that loop to `fanWakeTick` and set the process's verdict `slow` (the probe's own is `fanProbed`); `loopHooks.fanSilent` registers no eventfd for the secondaries, `fanUnwatched` turns the detector off. `secondaryWakeVerdict` (portable, `unprobed` until then) is the group log line's `secondary_wake=`. `uring_fanwake_linux_test.go`: `_bounded` (#703's detector: 200 round trips on a secondary, p50 under half a tick), `_tickFor`, `_logged`, `_probeControls` (unregistered eventfd reads slow), `_idleKeepsTheTick` (forced slow, a two-ring loop serving nothing runs at most two batches a tick), `_plainWait`, `_detectedInService` (unheard secondaries: a tick a round trip with the detector off, under half a tick and the verdict slow with it on) |
| `uring_sendzc_linux.go` | #969, ADR 0189 amended 2026-11-11: `uring-send-zc`, class A-code. `probeSendZC` (after `probeBundle`: REGISTER_PROBE for opcode 47, then one zero-copy datagram on loopback whose CQE carries F_MORE and whose notification comes; `failStage=send_zc`), `ringUses.sendZCMin` (`LoopValue.SendZCMin`, 16 KiB by default, zero while off); `flush` marks a flight of at least that many bytes `zcOn` (`connCtl.zc`), `sendFlight` sends every send of it with `prepSendZC`, counting the notification as one more operation and one `zcPend`; `zeroCopyDone` takes a notification (or a send's CQE without F_MORE) back, and `sendProgress` holds a flight sent whole until `zcPend` is zero, then `releaseFlight`. A send the kernel refuses (ADR 0189 amended 2026-11-13) never fails the connection: `zeroCopyRefused` takes EOPNOTSUPP — a socket without SOCK_SUPPORT_ZC, a Unix socket or one accepted from an MPTCP listener, which Go's `net.Listen` opens by default since 1.24 — and turns the loop plain for good (`ringUses.sendZCMin` back to zero, every ring of its thread sharing its listeners), or ENOMEM — the pinned pages past `RLIMIT_MEMLOCK`, io_uring's charge — and only the flight; nothing of it left, so it is held (`zcHold`) until its notifications came and its rest is sent by opcode 26 (`sendPlainly`), at once when none is pending; `zcPend` at 255 holds the flight the same way. `sendzc_uring_linux_test.go`: `_opcodeBySize` (3 bytes by opcode 26, 64 KiB by 47, the row on), `_conformance` (the 8 MiB byte-exact response on 1 and 3 rings with every flight zero-copy, the stale-slot write on 1), `_notificationHoldsTheFlight` (CQEs played on the loop's thread: the chunk, the ops and the slot held past a failure until the notification; after it the slot closed and released, and the next connection open on it under a new generation), `_switchedOffAndRefused` (disabled, missing; 8 MiB by opcode 26 only), `_refusedBySocket` (a Unix and an MPTCP listener: one opcode 47 refused, the 8 MiB answer byte-exact by opcode 26, the next flight plain), `_refusedForMemory` (`RLIMIT_MEMLOCK` lowered to zero after Start: every zero-copy send refused, 8 MiB byte-exact, zero-copy again once the limit is back), `_negativeMinRefused`. The L1 gate is `BenchmarkLoadL1big/pair=loop:loop-sendzc` (`internal/service/net/server`), reported by `tools/netgate`'s `send-zc` row. Not built: registered buffers |
| `packet_pktinfo_linux.go` | the junction of #972 and #852 (ADR 0192 amended 2026-11-11): `pktinfoSource` reads `IP_PKTINFO` (`ipi_spec_dst`) or `IPV6_PKTINFO` (the address unless a group, a mapped broadcast or `::`), `remember` / `sourceFor` / `forget` the batch's newest 128 senders and the destination each reached, `writePktinfo` the source's cmsg at the head of a SENDMSG record's control data (before `UDP_SEGMENT`'s), `unpin` a record whose source the kernel refused (EINVAL) sent again without it. `packet_pktinfo_linux_test.go`: `_answersFromTheDestination` (one reply and a GSO run of eight from 127.0.0.2 on a 0.0.0.0 socket), `_refusedSourceFallsBack` (a multicast source: every answer arrives, none failed), `_sourceFor`, `_pktinfoSource`, `_allocsPinned` (0 allocations with the cmsg) |
| `packet_none.go`, `packet_close_{unix,other}.go` | elsewhere `StartPacket` closes what it was handed and refuses, `LoopBackendUnavailable` at stage `arch` |
| `debug_on_linux.go`, `debug_off_linux.go` | the `loopdebug` build tag: `Gettid` recorded at loop start and checked in `Write`, `Close`, `SetDeadline` and the `Post` consumer |

## Why-this-shape

- **A leaf.** `loop` never imports `server`. `server` binds or adopts, then hands
  over a descriptor duplicated with `F_DUPFD_CLOEXEC`, set non-blocking. The
  group owns that descriptor from `Start` on, whether it starts or is refused.
- **One thread owns everything of its loop.** The goroutine locks itself to its
  OS thread and only then builds its epoll instance, eventfd, arena and ring.
  `Write`, `Close` and `SetDeadline` take no lock because only that thread
  calls them. `loopdebug`, which CI turns on, proves it. `Post` is the one door
  in from other goroutines.
- **Pointer-free control, mapped payloads (§D7).** `connCtl` holds integers
  only, and a chunk is a `chunkRef{cls, idx, off, len}`. The read arena and the
  slab are `syscall.Mmap`'d, so the collector scans none of it and never moves
  what the kernel reads. An idle connection holds no chunk: its output is
  attached at the first `Write` and goes back once drained.
- **`buf` stays valid until `OnBatchEnd`.** Received bytes stay where they were
  read: in the arena, or behind the spill. A spill that grows moves to a new
  chunk and the old one waits on `deferred`. Compaction runs only after
  `OnBatchEnd`. `TestCore_spillHandedOutStaysPut` pins this.
- **The drain takes the listen queue, then closes the listener** (ADR 0196
  §D3). epoll: `CTL_DEL`, then at each batch `accept4` to `EAGAIN`
  (`takeQueues`, `queueTakeBatch` a batch, `queueTakeMax` in all), each
  connection admitted — a draining loop admits; `EMFILE`, `ENFILE` or a full
  table wait for the drain's closes (`takeAfter`, `ring.full`), except
  `EMFILE`/`ENFILE` with no connection left in the group, which end the take
  as the portable backend's `giveUp` does; `control`
  calls it at every batch while draining. io_uring:
  the accepts in flight are cancelled, and one that completed as its cancel
  crossed it is served (`refuses` refuses only on a ring the watchdog drained
  while the group serves; a ring the watchdog drained before the group, or
  whose accept failed during the take, takes nothing — `take`); once they
  came back, `takeQueues` arms single-shot accepts as many as `queued` says
  wait — `TCP_INFO` on TCP, a zero-timeout
  `ppoll` on a Unix socket —, within the room above the reserve (none left,
  or a process-descriptor backoff, waits: never read as empty), `queueTakeMax`
  in all, an accept's failure stopping the take only when `takeWaits` says the
  listener failed or no descriptor will come (`EMFILE`/`ENFILE`, the group
  holding no connection), cancels one left waiting on an empty queue, and hands the
  listener back once the queue is empty with none in flight. The listener closes when
  its last taker hands it back (`listenerSet`), before the established
  connections drain; a loop stops once its connections are gone and its
  listeners handed back. A connection that has sent nothing (`fHeard` unset)
  closes only once heard or after `drainQuiet` (1 s): the take's connections
  have their request on the way. The window between a taker's last empty read
  and its close is the kernel's: the server's grace empties the queue first,
  and a partial drain in an `SO_REUSEPORT` group is `tcp_migrate_req=1`'s.
- **A batch costs the slots it touched, never the open ones.** Nothing at a
  batch's end walks the LRU: the dirty list names the slots to flush, and the
  compaction list (`ring.compact`, deduplicated by `fCompact`) the slots whose
  spill's head was consumed. The compaction list is a slice, not a link in
  `connCtl`: a slot released inside the batch is reset whole by `emptyConn`,
  which would cut a linked list there. At a few hundred requests a second a
  batch holds one or two requests, so a walk of ten thousand idle keep-alive
  connections per batch was paid per request.
  `TestCore_batchCostIndependentOfOpen` pins it, `BenchmarkCore_batch`
  measures it.
- **A loaded io_uring loop groups its wait (ADR 0199, ADR 0202).** Once a
  batch reaped `batchWaitFrom` (4) completions, every ring together, or
  reaped some while two arrivals at least are expected within `batchWait`
  (`busy`: `spacing`, the moving average of the time between two recv
  completions carrying bytes, folded by `woke` an eighth a batch with
  arrivals), the primary's next wait asks for `batchWaitMin` (32, at most
  `CQBatch`) within `batchWait` (250 µs, at most the tick): one
  `io_uring_enter`, `EXT_ARG`, relative timeout. The kernel wakes on that
  many CQEs past the head or at the bound, and an `ETIME` with CQEs returns
  zero (`io_cqring_wait`); a window that ends with an empty CQ is waited
  through plainly, no batch served for it. Idle, draining or
  `BatchWait < 0`: one completion or the tick, as before. `min_wait_usec`
  (`IORING_FEAT_MIN_TIMEOUT`) is not used: mainline 6.12 to 6.18 keep
  waiting for `min_complete` once its window passed empty, the stable fixes
  (e15cb2200b93, 29fe1bd01b99) are not visible in the feature bit, and on a
  fixed 7.0 it measured as the bounded wait's equal (ADR 0202 §D5).
  `TestURingWait` pins the semantics on a coop and a deferred ring,
  `TestURingWaitArgs` and `TestURingWoke` the rule.
- **An io_uring batch may be bounded in time (ADR 0206).** An answer leaves
  only after its batch: `Write` copies, `endBatch` turns the output into send
  SQEs after `OnBatchEnd`, and the next wait submits them. With
  `LoopValue.BatchBudget` (`sz.batchBudget`) a ring reaps `budgetChunk` (16)
  at a time (`reapShape`) and `lp.overBudget` reads the clock between reaps;
  once spent with completions left the batch is cut (`reapAll` reports it,
  `reapRings` stops, `BatchesCut` counts it on that ring), and the next batch
  starts at that ring (`cutAt`) and only submits (`await`): a secondary's
  leftovers would otherwise sleep to the tick, its eventfd already fired. A
  batch past half its budget submits every ring's sends right after
  `endBatch` (`submitLate`). Zero changes nothing, epoll ignores it.
  `TestURingBudget_cutsBatches` checks the cut on a ring and on a secondary,
  `BenchmarkURingBatchBudget` measures a held burst's first, median and last
  echo.
- `WriteFile` (#982, `corenet.LoopFileWriter`): head then n bytes of a file, the
  descriptor duplicated (the engine's copy, so an SQE can name it until the file
  is over). Epoll: `sendfile`, one 1 MiB step a turn, EPOLLOUT between. io_uring:
  one pipe per file (never per ring: two connections' bytes would mix, and a stalled
  client hold everyone's), a linked `IORING_OP_SPLICE` pair, file to pipe then pipe
  to socket, non-blocking, POLLOUT when the socket is full. The output is frozen
  while a file leaves (Write and a second WriteFile `LoopOutputFull`), the file's end
  fires OnWritable; a file that ends early closes with `LoopFileTruncated`. `closeFDs`
  on every path: a released slot closes its file and pipe (the pipe's bytes with it).
- **A drain gives a handler one last word (#858, ADR 0186 amended).** `closeSettled`
  asks a `corenet.LoopDrainer` once per connection before it closes it; the handler
  may write a goodbye (an SSE `retry:`, a WebSocket 1001) and call `Close`, and the
  connection closes at the next batch whatever it does: nothing can keep a drain
  open from here.
- **A batch pays its fixed cost once (ADR 0202).** `begin` reads the
  monotonic clock since the epoch once at a batch's start (the tick),
  `endBatch` once at its end (the histogram, the yield); `SetDeadline` reads
  it again, so a deadline set late in a long batch is not brought forward. The accept watchdog looks at most once a tick (`judged`):
  its look is a `getsockopt` a listener. `uring.wait` with a CQE already
  there only submits what is pending (`RawSyscall6`), and makes no system
  call when nothing is. `BenchmarkCore_batchClock`,
  `BenchmarkURingWait_ready` and `BenchmarkURingWatchdog_quiet` measure them;
  `server`'s `BenchmarkSparseKeepAlive` the whole under a sparse keep-alive load.
- **A native loop yields to a spare P (ADR 0199).** A locked loop never moves
  its P's `schedtick`, so sysmon preempts it every 10 ms and retakes its P
  in its waits (10.9 % of the samples at one vCPU). `loop.yield` calls
  `runtime.Gosched` at the very end of a batch at most every `yieldEvery`
  (5 ms), from `endBatch`'s stamp, only when `Start` found GOMAXPROCS above
  the group's loops: with no spare P it was measured as a loss.
  `TestCore_yieldBound` pins the bound.
- **The close is never re-entrant.** `Close` marks the slot and the batch's end
  settles it once its output has left, so `OnClose` never runs inside another
  callback. `finish` calls `OnClose`, then the backend's `shutdown`. The slot
  returns to the free list (`release`) only once the backend says nothing
  remains in the kernel, and its generation moves on at the next `take`. A
  stale reference does nothing and returns `LoopStaleRef`.
- **epoll's pressure (wave 1).** It accepts at most 128 per listener readiness.
  On `EMFILE`/`ENFILE` the listeners leave the set for a backoff of 1 tick
  (5 ms rounded up) doubling to 1 s, or until a close; the loop goes under hard
  pressure and paced eviction runs (`EvictIdle`, LRU, never a connection with
  unread, unconsumed or unsent bytes). MaxConns is accepted and closed, then
  counted in `Rejected`. Soft pressure is a flag set at accept past
  `SoftConns`; the protocol adapter decides what to do with it.
- **io_uring: the slot is not the kernel's index.** A connection's slot (its
  memory and generation) comes from the free list. The direct-descriptor
  index the accept CQE returns is mapped to it (`slotOf`). The slot goes back
  only when its operation count reaches zero: every final CQE decrements it,
  `-ECANCELED` and the cancel's own included, and no counted operation uses
  `CQE_SKIP_SUCCESS`. Closing never hard-links. An unlinked
  `ASYNC_CANCEL_FD|FD_FIXED|ALL` goes first, and `close_direct` is issued only
  at count zero: until then the index stays taken in the kernel, so `ALLOC`
  cannot hand it to a new connection while a completion for the old one may
  still come. Slots waiting on their close count against the accept reserve.
- **io_uring: a ring's buffers follow its bursts, up to its table (ADR 0201
  §D1, ADR 0203 §D2).** As many 2 KiB buffers as the table holds
  connections, rounded up to a power of two (`bufCountFor`: 8192 to 65536, a
  bid being 16 bits), are mapped private and anonymous at setup, and only the
  first 512 are handed to the kernel: a buffer it never had is no resident
  page. The kernel's buffer ring holds under 65 536 entries, so the bids go
  in groups of 8192, group g registered as buffer group 1+g only when its
  first bid is handed out: an idle ring has one, 128 KiB of kernel memory.
  After each batch `provide` hands it as many more as recvs starved since it
  last looked (`starvedSeen`), `bufsStep` at least, or `bufsStep` when it
  holds under a quarter of those provided, within `bufBudgetFor` (128 MiB a
  loop, every ring together, less under `MaxConns`: a buffer a connection of
  the loop's share and each ring's first 512; `uLoop.bufBytes`); it never takes any back. A
  ring's resident buffers are the most it had out at once — 1 MiB idle,
  98 MiB after 50 000 connections sent at once. A recv is armed on the
  group with the most buffers beyond the recvs armed on it and `groupSlack`
  (32; `roomiest`, `given`, `armedOn`, `recvGroup`): a multishot recv stays
  on its group, and one that received tries again at once, taking a buffer
  it recycles on EAGAIN or failing with ENOBUFS on an empty ring
  (`io_recv_finish`) — a group with exactly a buffer a recv ends each burst
  with spurious ENOBUFS. A growth adds the slack to what the burst lacked. A
  request larger than a buffer arrives over several completions and the
  spill joins them, as a split read always did.
- **io_uring: a buffer serves recv after recv where the kernel allows (ADR
  0204).** Every buffer group is registered with `IOU_PBUF_RING_INC` (6.12):
  the kernel consumes a buffer as far as each completion took and posts it
  with `IORING_CQE_F_BUF_MORE` while it keeps the rest. `uring.took` hands
  out a completion's bytes from `bufOff[bid]`, where the buffer's previous
  completion stopped, and `takeBuf` neither counts a `BUF_MORE` buffer out of
  `inRing` nor queues it: only its completion without the flag does, and
  `addBuf` resets its count when it goes back. The first ring the kernel
  refuses the flag (`EINVAL`, before 6.12; `failStage` `pbuf_inc` plays it)
  registers its groups plainly, the loop's later rings do not ask
  (`uLoop.incRefused`), and the row reads absent. Starvation, growth,
  groups and their slack are ADR 0203's, a conservative bound here. 50 000
  connections bursting on one ring: 512 buffers and no ENOBUFS, against
  50 801 and 49 489 a buffer a recv; the burst's last echo +16 % at the
  median on two CPUs (`TestConformance_noStarvationAtScale` and
  `TestConformance_bufferFaults` run both modes; `TestURingPbufInc_bufMore`
  watches each buffer through `loopHooks.bufSeen`).
- **io_uring: a recv may fill several buffers, on request (ADR 0205).**
  With `uring-recv-bundle` opted into, recvs carry `IORING_RECVSEND_BUNDLE`
  once `probeBundle` (`uring_bundle_linux.go`) saw, on a ring of its own
  consumed as the serving rings are, one bundle recv of 100 bytes fill two
  64-byte buffers: through 6.14 an incrementally consumed ring hands a
  bundle one buffer, and the row reads absent there. `uring.spans` walks a
  completion's bytes from its bid through the entries after it
  (`pbufGroup.order`, `bufPos`), `takeBuf` counts out and queues each buffer
  but a last one `BUF_MORE` keeps, and `recvBytes` delivers them buffer by
  buffer into `rr.segs`. An entry is written again only once the buffer last
  added there was released (`bufHeld`, `entryFree`): a pending bundle's
  completion names its buffers by entry; ring-order commits and an in-order
  CQ already guarantee it, `giveBack` and `growBufs` hold it anyway
  (`uRing.held` counts the returns put off, ADR 0205 §D3). Measured no gain on 240-byte requests and 25 %
  slower on 8 KiB ones, hence opt-in (`BenchmarkURingRecvBundle`);
  `TestConformance_bufferFaults` opts in, `TestURingRecvBundle_spansBuffers`
  watches each buffer through `bufSeen`'s `first`.
- **io_uring: buffers go back after `OnBatchEnd`, once each.** A CQE's bid is
  queued on `recycle` — on its last completion, under incremental
  consumption — and returned by `returnBuffers` after the batch. A recv
  that ran out (ENOBUFS, no `F_MORE`) waits on `starved`, a bounded queue,
  and is re-armed as long as the kernel holds a buffer (`inRing`, per group),
  not only when this batch returned one. Re-arming only on a batch's own
  returns once starved a connection forever (`TestConformance_bufferFaults`).
  `inRing` counts a buffer out only once its completion is reaped, so the
  CQ's backlog (`ready`) is counted out first, and nothing is re-armed while
  the kernel holds completions back: re-armed on buffers already gone, a
  burst of 50 000 starved 786 384 times (`TestConformance_noStarvationAtScale`,
  ADR 0203 §D2).
- **io_uring: the multishot state machine.** A CQE without `F_MORE` disarms.
  recv is re-armed unless `res == 0` or fatal. accept stops under the reserve
  (`ENFILE` included) and resumes at twice it, and is re-armed by `pace`.
  `SOCK_CLOEXEC` is never set on an accept into a direct descriptor, because
  the kernel refuses the flag with a file slot.
- **io_uring: what is watched, and what is not yet.** The watchdog acts only on
  an observable accept stall: armed, not paused, the listener's queue not
  empty (`TCP_INFO`), no accept CQE for 100 ticks — on this ring, nor on any
  ring of the group for that listener (`listenerSet.accepts`, ADR 0200 §D1).
  With several rings on one listener the kernel hands each connection to one
  waiter, and a ring that loses the race sees no completion while the queue
  moves: judged alone, two healthy rings of three were drained at 140 000
  connections and the third filled its table. A stall re-arms, and after 3
  re-arms the ring drains. A drained ring is never switched to epoll live.
  The recv stall (`SOCKET_URING_OP_SIOCINQ`) and the "CQ tail moved, head not
  reaped" check are not built: this loop always reaps every ring. An
  io_uring connection is never evicted, because a direct descriptor has no
  process descriptor to ask `SIOCINQ` of, so `unread` answers yes.
- **A close is a shutdown alone, on direct descriptors too (ADR 0200 §D2).**
  Before 6.13 a file removed from the direct table is put only once
  every request issued under the table's older resource nodes has completed
  (`io_rsrc_node_ref_zero` puts nodes in order; 6.13 gave each file its own).
  A multishot recv on an idle connection never completes, so a close alone
  leaves the socket open and its peer never sees the end — Debian's 6.12 and
  Ubuntu's 6.8 both behave this way. `shutdown(SHUT_RDWR)` does not wait for
  the put: it sends the FIN, and `inet_shutdown` wakes every waiter of the
  socket whatever its state, so the recv ends on zero and a send waiting for
  room on `EPIPE`. So every close, the peer finishing first included, is
  `IORING_OP_SHUTDOWN` (34) on the fixed slot, counted and never
  `CQE_SKIP_SUCCESS`, and `close_direct` once the count of operations in
  flight reaches zero. No cancel follows it: the `FD|FD_FIXED|ALL` cancel
  hard-linked behind it before walked every armed request of the ring
  (`io_poll_file_find`) and the io-wq's pending works at each close, and the
  shutdown runs in io-wq, so a drain of n connections cost n² — 249 s at
  60 000, the process unkillable. A slot still waiting on an operation 1 s
  after its shutdown (`closeBackstop`) has its recv and send cancelled by
  user_data, one hash bucket. Without the shutdown on a peer-first close the
  socket would sit in CLOSE_WAIT, its peer in FIN_WAIT2.
- **A CQ the kernel held back is flushed with GETEVENTS.** A secondary is
  entered only to submit; the kernel moves an overflowed CQ (`NODROP`) into
  the ring only on its wait path, so `reapAll` enters with GETEVENTS and
  `min_complete` zero (`flushOverflow`). A drain's thousands of shutdowns
  completing at once overflowed a secondary, and its closes waited forever.
- **The release check decides the mode and the sweep.** On the self-test's
  ring the payload's recv is armed first, as a keep-alive's is. A second
  connection is closed the loop's way and its peer must read the end within
  200 ms: that is `+direct`. Only if it fails does the loop serve on
  **process descriptors** (`+fds`: no `IOSQE_FIXED_FILE`, bound by
  `RLIMIT_NOFILE`, `close(2)` at count zero, EMFILE backoff, eviction). A
  third connection is closed without the shutdown: where its peer reads no
  end, the put is deferred and the rings sweep (`sweepOn`).
- **The sweep bounds the deferred puts** (`uRing.sweep`, only with
  `sweepOn`, so never on 6.13+). A closed socket stays allocated — about
  **3.6 KiB of kernel slab** each, measured from `/proc/slabinfo` on 6.8.0-147
  and 6.12.113 (TCP 2368/2432 + sock_inode_cache 832 + filp 192–256 + dentry
  192), no descriptor — until every recv armed before its close completes;
  with churn and one old keep-alive that is unbounded. A pass starts after
  max(8192, table/4) closes, or every 10 s with at least one, walks the
  table in at most 1 s (128 slots a tick at least), and cancels by user_data
  each recv armed before the pass and before a close (`armEpoch` < the
  pass's `closes`); `recvCancelled` re-arms it under the current node and the
  older nodes drain. Pinned sockets stay under that threshold plus one second
  of closes — 16 384 on a 65 536-entry table, about 58 MiB, plus the second —
  for at most four cancels and four re-arms per close. Bytes arriving between
  cancel and re-arm wait in the socket. `TestConformance_sweepReleases`
  counts `/proc/net/sockstat`'s TCP `alloc` (254 pinned after 256 closes on
  6.8 and 6.12, all released by one pass); `TestConformance_directCloseEOF`
  reads the end within 200 ms with a keeper armed first, server close and
  peer first.
- **Rings an unprivileged process can afford.** Without `CAP_IPC_LOCK` the
  kernel charges each ring's SQ and CQ to `RLIMIT_MEMLOCK` (8 MiB by
  default). A serving ring asks for 1024 SQEs and a 4096-entry CQ, the
  probe's rings for 16. The buffer ring is the kernel's
  (`IOU_PBUF_RING_MMAP`, 6.4), user pages before that. With 4096 SQEs and
  16384 CQEs, CI's non-root lane ran out after a few groups (`ENOMEM` at setup).
- **Ubuntu 6.8.0-147 refuses every well-formed buffer ring.** Its
  `io_register_pbuf_ring` inverts the reserved-field check (`if
  (!memchr_inv(reg.resv, 0, ...)) return -EINVAL`), so a zero `resv` is
  refused. `registerBufRing` retries once with one `resv` byte set: that kernel
  accepts it, and a correct kernel refuses it again with the same EINVAL.
- **io_uring: a loop's rings fill in order (ADR 0203 §D1).** Only ring 0
  accepts at start. A ring is full (`fillFull`) once its free entries fall to
  `fillMargin` (an eighth of the table, twice the accept reserve at least) or
  when it is passed over (the watchdog drained it, or the primary a test keeps
  idle), and not full again only past twice the margin. Ring *i* arms accepts
  only while every ring before it is full (`opens`); one that loses the right
  is closed (`fillClosed`), cancels its accepts once and keeps its connections. `pace`
  steps the rings in order each batch, not while the group drains. Spread
  over three rings, 10,000 connections paid each batch two more submits and
  the fan eventfd's wakes: 6.9 µs a request on one vCPU.
  `TestURingFill_inOrder` pins the hysteresis,
  `TestConformance_ringsFillInOrder` the order, `BenchmarkURingRings_fill`
  measures one ring against three.
- **Serving accepts are single-shot.** A multishot accept into the direct
  table keeps accepting the whole backlog before the loop sees the reserve
  reached. Past the last entry the kernel accepts, cannot install, and drops
  the connection. At most 32 single-shot accepts are in flight per listener,
  never more than the free entries above the reserve. The probe keeps a
  multishot accept.
- **A cancel the slot never asked for re-arms.** An open connection whose recv
  ends with `-ECANCELED` re-arms it, and one whose send was cancelled sends
  the same flight again.
- **The probe is staged, and any failure is epoll.** setup with every flag the
  switches allow, given up newest first on `EINVAL` (ADR 0189 §D3), features
  (`EXT_ARG`, `NODROP`, `SINGLE_MMAP`), `REGISTER_PROBE` (accept, recv, send,
  cancel, close, poll, read, nop, shutdown), `RING_FDS` registered and
  unregistered, `SQ_REWIND`'s partial submit, the buffer ring and the sparse
  table, then the loopback self-test, all on rings of 16 closed before
  serving, on the loop's locked thread. Under auto, a failure starts epoll
  with the reason in `State` and on the start line. An explicit io_uring
  fails with `LoopBackendUnavailable` naming the stage.
- **Capabilities: classes, switches, report (ADR 0189).** Class A-kernel
  (multishot recv, the buffer ring, its incremental consumption, direct
  descriptors, `EPOLLEXCLUSIVE`, `TCP_NODELAY`) is on once probed; class
  A-code (`DEFER_TASKRUN`, `NO_SQARRAY`, `RING_FDS`, `SQ_REWIND`, receive
  bundles, busy-poll) only on `LoopValue.Capabilities`;
  class B is declared and refused. `Config.Disabled` is never probed:
  `uring-pbuf` off rules io_uring out (auto runs epoll, not degraded),
  `uring-multishot-recv` off makes every recv single-shot (`singleShot`, the
  state machine re-arms), `uring-direct` off puts the self-test and the
  connections in the process table, `uring-pbuf-inc` off registers the
  buffer rings without `IOU_PBUF_RING_INC`, `epoll-exclusive` off registers the
  listeners plainly (probed on a throwaway epoll and eventfd otherwise).
  `State().Capabilities` holds one row per `loopCaps` entry, server appends
  its `tcp-nodelay` row.
- **busy-poll (#979, ADR 0189 amended 2026-11-11).** Class A-code. Its stage runs
  after the receive bundle's: io_uring registers a zero window
  (`IORING_REGISTER_NAPI`, 27) on a throwaway ring (`EINVAL` before 6.9), epoll
  sets zero parameters (`EPIOCSPARAMS`) on a throwaway instance (`ENOTTY` before
  6.9); `failStage: "busy_poll"` plays both. Opted into, io_uring registers
  `LoopValue.BusyPoll` (50 µs) with `prefer_busy_poll` on ring 0 only — the ring the
  thread waits on, the only one whose `io_uring_enter` wait polls — and never
  unregisters (the ring's close frees it); epoll sets it on each loop's instance,
  with `BusyPollBudget`. The row's reason says what it polls: the NAPI ids the
  kernel gave the sockets, none on loopback or a veth without GRO, so on CI's lanes
  it is on and polls nothing. Switched off, no call is issued (`hooks.busyPoll`
  counts them). `TestBusyPoll_*`, `TestURing_registerNAPI` (the kernel writes the
  previous window back), `TestBusyPoll_epollParams` (`EPIOCGPARAMS` reads it).
- **uring-zcrx (#980, ADR 0189 amended 2026-11-11).** Class B, refused as an opt-in,
  and its row names its stage: `kernel: IORING_OP_RECV_ZC unsupported (before
  6.15)`, `available: no interface configured`, or the register's answer for the
  queue `LoopValue.ZCRX` names, registered on a throwaway ring (which restarts the
  NIC queue twice) — loop 0 only. Nothing receives through it: the area and refill
  design is the ADR's. The suite's kernels lack the opcode, so `TestZCRX_played`
  plays the later stages (`hooks.zcrxKernel`, `zcrxErrno`) and
  `TestZCRX_registerRefused` runs the real call only to see it refused.
- **`Shards(0)`: a listener per loop (#856, ADR 0186 and ADR 0196 amended).**
  `server.Shards(0)` on a loop group binds one `SO_REUSEPORT` listener per loop
  (`Loops`, read once by `resolveLanes` before the bind; `:0` draws the port once,
  `pinnedAddr`), and `Listener.Loop` hands each to its one loop (zero is every loop,
  the former shared listener). epoll registers only its own shard; on io_uring only
  the owning loop's rings arm accepts on it, the other rings of the other loops never
  do (`ownedBy`, `doneOutside`), so each loop's accept queue is its own and the
  four-tuple hash spreads the connections with no contention. Any other count is
  refused (`shardsRefusal`, a Unix address too), `KITSUNIUM_NET_DISABLE=reuseport-loops`
  falls back to one shared listener (`reuseport-loops`, class A-code, reads disabled),
  and the portable backend, `Rings` apart, serves one listener: it has no loop that
  owns one. The drain is ADR 0196 §D3's, each listener with one taker (`takersOfShards`:
  its loop's rings), and a loop whose watchdog gave up closes its own shards
  (`giveUpShards`: nobody else takes from their queues, and the kernel routes around
  a closed one). `TestConformance_shards`, `TestConformance_drainTakesQueue/tcp+shards`.
  `reuseport-cbpf` is the datagram group's Steer's: a loop group never reads it (#857).
- **A held address is refused before the shards; `ShareAddress` joins it (#1330, ADR
  0029 amended 2026-11-12).** The listeners are server's to bind, so the probe is
  too: before `Shards(0)`'s per-loop listeners, `bindLoopAddress` binds the address
  once plainly and closes it, and an address another socket holds fails Start with
  `AddressInUse` instead of the loops' shards joining it. A group given
  `server.ShareAddress()` skips the probe and binds every listener with
  `SO_REUSEPORT`, the shared one of the portable backend included, so another
  process that shares too joins it for a progressive restart (ADR 0196 §D4); the
  loop reads nothing new: `shardRow` still reads `SO_REUSEPORT` back on the
  listeners a loop owns. `TestShards_probeRefusesAHeldPort/loop/…` (server).
- **Steering rows (#857, ADR 0189 amended).** Three class A-code rows and one
  class B. `loop-affinity`: `LoopValue.Affinity` names one CPU a loop and is its
  opt-in; each loop's thread, once locked, is pinned (`loop.pin`), and a CPU the
  kernel refuses (EINVAL offline or outside the set, EPERM under a filter) leaves
  that loop unpinned and serving, the row `missing` — server's capability fault.
  `so-incoming-cpu`: opted into through `Capabilities`, `SO_INCOMING_CPU` (49) set
  and read back on each per-loop listener of `Shards(0)` whose loop Affinity pins,
  so the kernel's reuseport lookup prefers the listener of the CPU that took the
  connection; without Affinity or without per-loop listeners the row says which.
  `tcp-notsent-lowat`: `TCP_NOTSENT_LOWAT` (25) on every TCP listener, inherited
  by each accepted socket, `NotsentLowat` bytes or 128 KiB for a bare opt-in; a
  write then leaves that much unsent in the kernel and at most one segment more
  (sendmsg checks the lowat only as it opens a segment), and `OnWritable` fires
  once the peer drained (`TestConformance_notsentLowat`: 4 MiB to a peer that
  reads nothing, the unsent bytes — tcp_info's `tcpi_notsent_bytes` over
  sock_diag, SIOCOUTQNSD's count, which a direct descriptor answers too — at
  least the lowat and at most 64 KiB past it, there and at every `OnWritable`;
  without the option 1 015 808 bytes queued and `OnWritable` fired with nothing
  read). SIOCOUTQ's count, `/proc/net/tcp`'s tx_queue, adds the bytes sent and
  not yet acknowledged, which the option does not bound; and "under the lowat"
  is not what `OnWritable` sees: the kernel reports the socket writable under it,
  the loop's flush fills it past it again, and `OnWritable` follows the flush
  (141 504 bytes unsent for a lowat of 131 072, measured on epoll). `tcp-fastopen` is declared, class B: nothing sets it. A
  refused option reads `missing`: every one of the four is an opt-in.
  `setsockoptHook` plays a refusing kernel.
- **Listener options (`listener_linux.go`, #971, ADR 0189's amendment).**
  `prepareListeners` runs before any accept, on both native backends: on every
  TCP listener (`tcpListener`: a Unix socket takes none, `TCP_NODELAY`
  included) it sets `TCP_NODELAY` and the three rows below, which the kernel
  copies into each accepted socket, and reads each back into the report
  (`capReport.apply`). `tcp-keepalive` (class A-kernel) is `SO_KEEPALIVE` with
  `Config.KeepAlive` as idle time and interval (zero 15 s, the stdlib's;
  negative sets nothing) and 9 probes; `tcp-user-timeout` (class A-kernel) is
  `TCP_USER_TIMEOUT` 18, declared in-tree, from `Config.UserTimeout`;
  `tcp-defer-accept` (class A-code) is `TCP_DEFER_ACCEPT` from
  `Config.ReadTimeout`, set only when opted in and a bound exists. A row not
  asked for reads `available` (`unprobed` for defer-accept), a refusal
  `absent` (`missing` for an opted-in one), a group without a TCP listener
  `unprobed`. **`LoopPeer` (#974)**: `*loop` implements the sibling port
  (`Peer(ref) (LoopPeerValue, bool)`, found by a type assertion on the `LoopIO`),
  asking the backend through the optional `peerer`: epoll and an io_uring
  group in the process table read `SO_PEERCRED` on the slot's descriptor, so the
  uid, gid and pid of the connecting process come back `Verified`; a TCP
  connection and a stale ref answer false. On direct descriptors a Unix
  connection's credentials are read at accept by `IORING_OP_URING_CMD` with
  `SOCKET_URING_OP_GETSOCKOPT` (6.7, `uring_peer_linux.go`): `askPeer` puts the
  slot in `stPeer` with the command in flight, writing into `uRing.creds` (one
  `peerCell` per direct-descriptor index, heap memory that never moves), and
  `onPeer` opens it — recv armed, `OnOpen` — once the command completed, so the
  peer is known from `OnOpen` on; a command that completes after `closeAll`
  started lets the slot go unopened (`uLoop.stopping`). The self-test proves
  the command on a Unix
  socket of a pair it installs in the direct table (`peerCommand`,
  `installProbe`, `IORING_OP_FILES_UPDATE` at an entry the kernel allocates),
  which must answer this process's pid — a TCP descriptor answered on Fedora's
  6.14 while Unix peers stayed unknown — and the class A-kernel row
  `uring-sock-getsockopt` reports it (`reportSockCmd`): absent or switched off,
  a group with a Unix listener runs io_uring in the process table (`uLoop.unixL`,
  `uring-direct` reads `unprobed`, the stage `self_test+fds`), its TCP
  connections included, and `Peer` asks the socket there.
  `refuseSeqpacket` (#974) refuses a unixpacket listener at
  `Start` with `LoopOptionUnsupported` naming `network=unixpacket`, on every
  backend: both read a connection as a stream into a buffer that cuts a
  longer record with no flag, so a record would reach `OnData` cut. Unix
  streams (a path, or the abstract namespace) are served, pinned by
  `unix_linux_test.go`. Not proved here: a blackholed peer with unsent output reaching
  `OnClose` within `UserTimeout` (a veth lane, root), `Config` fed from
  `TimeoutsValue` and the goroutine and portable engines' options (server).
- **`DEFER_TASKRUN` on the primary only (P3a).** The thread waits on ring 0;
  a deferred secondary would post no completion until entered with
  `GETEVENTS`, which nothing does — `DEFER_TASKRUN` on every ring times out the
  second round trip of `TestCaps_deferPrimaryOnly`. The secondaries keep
  `COOP_TASKRUN` and wake the primary through their eventfd; every submit on
  the primary carries `GETEVENTS` with `min_complete` zero.
- **`SQ_REWIND` fills from SQE 0 (P3d).** Under it the head and tail stay
  zero, `uring.slot` is `sqes[pending]`, and a partial submit moves what is
  left down (`uring.consumed`), links and all. `NO_SQARRAY` (P3b) skips the
  SQ array. A ring registered with its thread (P3c) is entered by its index
  (`enterTarget`) and unregistered in `uring.close` before the descriptor
  closes: a ring still registered outlives its close.
- **A datagram lane is a socket, its thread and its ring (#852, ADR 0192 and
  0189 amended).** `StartPacket` probes on the lane's own locked thread, on the
  ring it will serve with, and any stage that fails closes the descriptor and
  returns `LoopBackendUnavailable` naming it: server serves that socket on its
  batched reader, the row absent and no fault. One multishot RECVMSG holds the
  socket: each completion's buffer is one message, laid out by the kernel as
  `io_uring_recvmsg_out`, then the name and control rooms the msghdr declared,
  then the payload; the buffers go back after `OnBatchEnd`, and a receive the
  kernel ended (ENOBUFS) is armed again over them. A reply is copied at `Reply`
  into mapped memory — the kernel reads the record and the bytes after the call
  returned — and leaves at the batch's end; the arena is reused only once no
  reply is queued or in flight, so a reply refused for room (`LoopOutputFull`)
  is the backpressure of a socket whose sends wait. The handover is ADR 0192
  §D4's: the receive cancelled, the socket read to EAGAIN with `recvmsg(2)`
  into a mapped buffer, served, the replies' completions awaited (bounded), GRO
  off. A handler's panic is not contained here, as on every loop thread.
  `TestPacketLane_echo` (4 000 datagrams once each), `_gsoRuns` (UDP_SEGMENT
  runs counted), `_groSplits` (a GSO train of eleven handed on as eleven),
  `_ceiling`, `_failStage` (each stage, the descriptor closed), `_handover`
  (every datagram once, the lane's then the successor's, GRO off) and `_allocs`
  (a datagram handed on, answered and sent: 0 allocations); they fail, not
  skip, where `requiredBackends` names `iouring`.
- **A handler queues what `Write` refuses.** A line the handler stopped at
  stays in the spill and comes back only with the next bytes. A handler that
  answers a pipelined request behind a refused write must therefore queue it
  itself and drain on `OnWritable`, as the conformance handler does.

## Conformance (one suite, every backend present)

The backend-neutral cases live in `conformance_native_test.go` and
`conformance_drain_native_test.go`, the idle-memory bound in
`memory_native_test.go`: on Linux they run on `iouring` and `epoll`, on darwin
and the BSDs on `kqueue` (ADR 0222), where `conformance_bsd_test.go` requires
kqueue by default — the darwin lane and the BSD guests link no `-X`, and
`test-binaries.sh` links its own `-ldflags` — and pins the probe's stages and
the refusal of Linux's backends. There the drain cases queue 96 connections,
below the default backlog of 128, and Shards(0)'s case stays Linux's.
`conformance_linux_test.go` runs on `iouring` and `epoll`. `requiredBackends`
and `requiredCaps` are linked in, and the suite fails closed on both
(`TestBackendsRequired`, `TestCapsRequired`): unset fails, a backend or a
capability the kernel lacks fails, and a capability no probe of `capProbes`
checks fails too — a lane may not require what nothing verifies. Every class
A capability has a probe (`conformance_caps_linux_test.go`'s `probeCap`: a
group with it opted into, its row `on`); `uring-direct` reads the release
check's stage. The lanes require all of them but `uring-sq-rewind` (7.0).
Bazel links `epoll` and `none`; e2e-cross's lanes `iouring,epoll` and `none`;
each leg of `net-kernel-gate.yml` declares both lists. It covers: partial consume, an
8 MiB response byte-exact behind short sends and EAGAIN, `LoopSpillFull`,
closing slot 0 while 1 lives, slot reuse with a stale `Write`, a table of 2
(epoll refuses, io_uring waits and serves after a close), MaxConns, idle,
`Post` exactly once ×20 000, `Shutdown` racing traffic with no descriptor
leaked, GOGC=1 with forced GC at the high-water mark, EMFILE at RLIMIT 64 and
300 connections past RLIMIT 128 on 3 derived rings (re-executed children),
the foreign-thread `Write` under `loopdebug`, a 2-entry buffer ring of 64-byte
buffers shuffled and 0xAA-filled (thousands of ENOBUFS), every probe stage
failed, `DEFER_TASKRUN`, 10 000 idle connections with zero watchdog fires,
and the drain (`conformance_drain_linux_test.go`, ADR 0196, on a TCP and on a
Unix listener): the loop held at
its batch end, 384 connections queued with their request — more than one
epoll burst and than io_uring's accepts in flight —, then the drain, every one
reading its echo and a clean end, the listener refusing after; 1 280 queued
past epoll's batch; and `TestConformance_drainOutOfDescriptors`, a child
under RLIMIT 64 whose take waits for the drain's closes. The stop's cost
(`conformance_stop_linux_test.go`, ADR 0200): 20 000 idle connections, heard
once, on three rings, drained and closed, every peer reading the end. On
io_uring the verdict is counted, not timed (`conformance_stop_uring_linux_test.go`):
`loopHooks.taken` sees every SQE a ring hands the kernel, and the stop must
issue one shutdown a connection, no cancel by descriptor and no hard link —
the linked cancel fails it at any speed (20 000 of each). The 30 s bound only
calls a stop hung: a linear stop takes 0.2 s under Docker on 7.0 with the race
detector, the quadratic one 3.6 to 5.15 s, too close to tell apart on a
loaded runner. `TestConformance_closeWhileArriving` closes a group while
connections keep arriving: every `OnOpen` hears its `OnClose` before `Close`
returns, and none opens after. io_uring's `closeAll` reaps a bounded second
for what it issued, and an accept completing then used to open and arm the
next one — 15 000 connections opened and never closed in that second, on
direct and process descriptors alike; once `closeAll` set `uLoop.stopBy`, the
end of its wait, `uLoop.stopping` closes such an accept unopened and arms none
(Fedora 6.14 showed it through
`TestUnix_peerUnderLoad` on process descriptors, which now runs with the
socket command switched off and absent too). `watchdog_uring_linux_test.go`
pins the watchdog's per-listener verdict.
`conformance_caps_linux_test.go` and `conformance_caps_uring_linux_test.go`
hold the capabilities (ADR 0189): the report (one row each, no probe defect,
no class B or unasked class A-code on), each class A-kernel switch serving the
exchange, `uring-pbuf` off, the refusals, each class A-code opt-in and three
and four together on 2 loops × 3 rings, `sqOff.array == 0`, the
secondary-only wake under half a tick, the FIN after a close with registered
rings, and the operator's switch over an opt-in. Measured on 6.8.0-147 and
6.12.113 (arm64): every class A capability but `uring-sq-rewind`; on 7.0
(Docker) all of them.
`uring_abi_linux_internal_test.go` freezes the uapi layouts and compares the
encoders with liburing's bytes.

Not built yet: the forced-full CQ of 2, the `IOSQE_ASYNC` send cancelled into
`-EALREADY`, the multishot that never completes, and a fallback with
connections still live. These are named here so nobody believes them done.

## Benchmarks and BENCH.md (#981, ADR 0165)

`BENCH.md` beside this file is the package's report: the envelope (CPU, kernel,
Go, commit, `-benchtime`, `-count`), one row per Benchmark function — the `Core_*`
rows on a fake driver, `Post`, `Wheel`, `Slab` and `Write` (allocs 0 for `Write` and
`Post`), the eight `URing*` rows on io_uring — and the class B rows of `caps.go`,
each with the benchmark its ADR will read or "no row yet" (ADR 0189 §D1). It is
hand-run, as `net/http1`'s is, and `make bench` does not write it:

```
cd internal && GOWORK=off go test -run='^$' -bench=. -benchmem -count=6 -benchtime=300ms ./service/net/loop/
```

`scripts/pre-commit/check-bench-md.sh` finds a benchmarked package by its
`*_bench_test.go` file names, so every Benchmark function lives in a file of that
name (`loop_bench_test.go`, `batch_cost_bench_test.go`, `uring_*_bench_test.go`,
the io_uring ones with their explicit `//go:build linux && (amd64 || ...)` line,
which a `_linux_bench_test.go` name would lose): a benchmark put in an
`_internal_test.go` or `_linux_test.go` file hides from the check again.
`TestBackendsRequired` and `TestCapsRequired` still need their `-ldflags` (above).

## Cost

- **The slab follows its memory limit and gives a burst back (#809, ADR 0186
  §D7 amended).** A loop's budget was 128 MiB whatever the cgroup, and an arena
  stayed mapped until the loop stopped. Now server passes `memlimit.Derive()` (the
  allowance, nothing installed) as `Config.Memory`; 512 MiB and four loops is
  32 MiB a loop. The alloc and free path keeps no count: `shrink` counts each
  arena's chunks on the free stack once a tick, and only in a class holding two
  arenas' worth of free chunks, so `BenchmarkSlab`'s alloc+free is the base's
  code (benchstat geomean +2.7 %, n=10, the noise of a shared VM). A 64 MiB burst
  freed is back to one arena a class within 32 ticks
  (`TestSlab_shrinkAfterBurst`, the mapper double, the ticks driven by hand: the
  loop reads no clock port). An epoll or kqueue loop with no connection slept
  until the next event, so its shrink never ran once a burst's clients had all
  left — the usual end of a burst: such a loop now waits on the tick while
  `slab.reclaimable` (a class with two wholly free arenas) holds, and sleeps
  again once it does not, so it wakes for nothing; io_uring waits on the tick
  anyway (`TestSlab_idleLoopGivesBack`, every native backend: 8 MiB of spills,
  every client gone, back to one arena a class within a second with no event,
  then no batch at all on epoll and kqueue). Under `GOMEMLIMIT` set by the
  operator memlimit reads no allowance, and the budget is the default.
- The LoopIO budget is allocs 0 for `Write`, `SetDeadline`, `Pressure` and a
  refused `Write` with a caller-owned `p` (`TestCore_allocs`).
- An idle connection costs at most **256 B of Go heap** and no mapped byte
  (`TestMemory_idleConnection`, 10 000 connections from a re-executed child,
  race off, its own Bazel target in `tools/alloc-lane-targets.txt`). Measured
  under Docker on Linux 7.0 arm64: 136.4 B per connection with epoll, the test
  handler's own map entry included, and 137.9 B with io_uring (145.8 B each
  before the table grew by fixed steps, ADR 0201 §D2). The kernel's socket memory is not counted
  here; the 50k/150k/250k curve on a real host is measured outside CI.

## Verification

```
# Linux (CI, and Docker on a Mac with --security-opt seccomp=unconfined, which
# io_uring needs; under Docker's default profile auto falls back to epoll):
cd internal && GOWORK=off go test -race -tags loopdebug \
  -ldflags='-X=github.com/kitsunium/sdk/internal/service/net/loop.requiredBackends=iouring,epoll -X=github.com/kitsunium/sdk/internal/service/net/loop.requiredCaps=none' \
  ./service/net/loop/
# without either -X the suite fails closed on Linux, by design; `none` requires nothing.
bazel test //internal/service/net/loop:all
# darwin and the BSDs (the macos-15 lane, the BSD guests): kqueue is required
# by default; -X=…requiredBackends=none lifts it.
cd internal && GOWORK=off go test ./service/net/loop/
```

## Do NOT

- Import `server`, or any member of the family.
- Put a pointer in `connCtl`, or a payload byte in the Go heap on a native
  backend.
- Compact, reallocate or recycle a buffer handed to `OnData` before
  `OnBatchEnd` returns.
- Walk every open slot at each batch: list what the batch touched instead.
- Call `OnClose` from inside another callback.
- Call `setrlimit` or `GOMAXPROCS`: `State` reports both, and the consumer
  decides. `LoopsAuto` leaves a P over; it never adds one.
