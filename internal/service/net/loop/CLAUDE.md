<!-- updated: 2026-10-25T00:00:00Z -->
# internal/service/net/loop/

## Purpose

The network domain's event-loop engine (ADR 0186): the native backends a
plaintext stream group runs on when a `corenet.LoopHandler` serves it:
io_uring on `linux && (amd64 || arm64 || riscv64 || loong64)` after a staged
probe of the running kernel, and epoll on every Linux architecture and after
any probe failure. The
portable backend is not here: it needs the netpoller and `net.Conn`, so it lives
in `server` (`loop_portable.go`). Nothing is public yet:
`server.Group.HandleLoop` publishes the engine once every `linux-init` lane and
the Ubuntu lane name both Linux backends (§D9 step 11). Until then
`server`'s unexported `handleLoop` and this package's tests are the only way in.

## Contents

| File | What |
|---|---|
| `group.go` | `Listener` (the one hand-off: a duplicated descriptor), `Config`, `Group` (`Shutdown`, `Close`, `State`, `Rejected`, `Live`) and `Start`, which resolves the sizing, starts the backend and writes the one start line |
| `sizing.go` | `resolve`: the defaults, the `LoopNoDescriptors` refusal of a zero `RLIMIT_NOFILE`, and the one ring formula (explicit, else ⌈max(MaxConns, SoftConns) / min(RLIMIT_NOFILE, IORING_MAX_FIXED_FILES)⌉, else 1) |
| `conn.go` | `connCtl`, one slot's pointer-free control state; its states, flags and close reasons |
| `core.go` | the backend-neutral core: `ring` (slot table, free list, LRU, dirty list, timers), `loop` (the `corenet.LoopIO` implementation), `driver` (what a backend does: flush, shutdown, unread), the spill, the double-buffered output, the close and eviction rules |
| `slab.go` | the mmap'd slab: power-of-two size classes from 2 KiB, arenas mapped lazily, a global budget that sheds load by name |
| `wheel.go` | the hashed timer wheel, index-linked, one timer per slot, 10 ms ticks |
| `postq.go` | the `Post` queue: preallocated, mutex, `pending` CAS false→true wakes once per drain |
| `stats.go` | the per-ring counters (plain on the loop, atomic for `State`, published at the end of each batch) and the batch-duration histogram behind `BatchP99` |
| `epoll_linux.go` | the epoll backend: one thread, one epoll instance, one ring per loop; `EPOLLEXCLUSIVE` listeners; a 2 MiB mmap'd read arena per loop |
| `uring_abi_linux.go` | the io_uring uapi in-tree (no `x/sys`, ADR 0018): syscall numbers 425–427, flags, opcodes, `sqe`/`cqe`/`uringBuf`/`uringParams`/… layouts, `fixedFd` (0-based) and `targetIndex` (index + 1), `FILE_INDEX_ALLOC` raw `0xFFFFFFFF`; one `Syscall6` per waiting enter, `RawSyscall6` to submit only |
| `uring_ring_linux.go` | one io_uring: `SETUP_CLAMP`+`CQSIZE` with the granted sizes read back, the single mapping, the SQ/CQ views, the bounded submit (EINTR again; EBUSY/EAGAIN counted and left to the next batch), the provided-buffer ring and its tail published by one 32-bit store of entry 0's bid and the tail, the sparse table, `REGISTER_PROBE`, the SQE encoders |
| `uring_loop_linux.go` | the io_uring loop: one locked thread owns every ring; the primary waits (EXT_ARG, the tick is ETIME), the secondaries signal it through one registered eventfd it polls multishot, the `Post` eventfd is another one; connection slot apart from kernel index (`slotOf`), user_data = op \| gen(24) \| slot; the multishot state machine; the unlinked cancel → count → `close_direct`; ENOBUFS re-arms; accept paused under the reserve and resumed at twice it; the accept watchdog |
| `uring_probe_linux.go` | the last probe stage: a loopback multishot accept into a direct descriptor and a multishot recv through the buffer ring, on a ring of its own, then the release check (shutdown→cancel→close must show the peer the end; cancel→close alone tells whether puts are deferred) |
| `uring_none_linux.go` | the Linux architectures outside io_uring's wave 1: `LoopBackendUnavailable`, epoll serves |
| `group_linux.go`, `group_other.go` | backend choice, `RLIMIT_NOFILE`, `mmap`; elsewhere, `UnsupportedPlatform` |
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
- **io_uring: buffers go back after `OnBatchEnd`, once each.** A CQE's bid is
  queued on `recycle` and returned by `returnBuffers` after the batch. A recv
  that ran out (ENOBUFS, no `F_MORE`) waits on `starved`, a bounded queue,
  and is re-armed as long as the kernel holds a buffer (`inRing`), not only
  when this batch returned one. Re-arming only on a batch's own returns once
  starved a connection forever (`TestConformance_bufferFaults`).
- **io_uring: the multishot state machine.** A CQE without `F_MORE` disarms.
  recv is re-armed unless `res == 0` or fatal. accept stops under the reserve
  (`ENFILE` included) and resumes at twice it, and is re-armed by `pace`.
  `SOCK_CLOEXEC` is never set on an accept into a direct descriptor, because
  the kernel refuses the flag with a file slot.
- **io_uring: what is watched, and what is not yet.** The watchdog acts only on
  an observable accept stall: armed, not paused, the listener's queue not
  empty (`TCP_INFO`), no accept CQE for 100 ticks. It then re-arms, and after
  3 re-arms the ring drains. A drained ring is never switched to epoll live.
  The recv stall (`SOCKET_URING_OP_SIOCINQ`) and the "CQ tail moved, head not
  reaped" check are not built: this loop always reaps every ring. An
  io_uring connection is never evicted, because a direct descriptor has no
  process descriptor to ask `SIOCINQ` of, so `unread` answers yes.
- **A close is a shutdown first, then direct descriptors on 6.8 and 6.12
  too.** Before 6.13 a file removed from the direct table is put only once
  every request issued under the table's older resource nodes has completed
  (`io_rsrc_node_ref_zero` puts nodes in order; 6.13 gave each file its own).
  A multishot recv on an idle connection never completes, so a close alone
  leaves the socket open and its peer never sees the end — Debian's 6.12 and
  Ubuntu's 6.8 both behave this way. `shutdown(SHUT_RDWR)` does not wait for
  the put: it sends the FIN and ends the connection's own recv on zero. So
  every close, the peer finishing first included, is `IORING_OP_SHUTDOWN`
  (34) on the fixed slot, the `FD|FD_FIXED|ALL` cancel hard-linked behind it
  (unlinked, it could cancel the shutdown, which 6.8 runs in io-wq), and
  `close_direct` once the count of operations in flight reaches zero. The
  shutdown is counted and never `CQE_SKIP_SUCCESS`. Without the shutdown on a
  peer-first close the socket would sit in CLOSE_WAIT, its peer in FIN_WAIT2.
- **The release check decides the mode and the sweep.** On the self-test's
  ring the payload's recv is armed first, as a keep-alive's is. A second
  connection is closed the loop's way and its peer must read the end within
  200 ms: that is `+direct`. Only if it fails does the loop serve on
  **process descriptors** (`+fds`: no `IOSQE_FIXED_FILE`, bound by
  `RLIMIT_NOFILE`, `close(2)` at count zero, EMFILE backoff, eviction). A
  third connection is closed without the shutdown: where its peer reads no
  end, the put is deferred and the rings sweep (`sweepOn`).
- **The sweep bounds the deferred puts** (`uRing.sweep`, only with
  `sweepOn`, so never on 6.13+). A closed socket stays allocated —
  struct file, inode and `tcp_sock`, an **unmeasured estimate of 2 to 4 KiB of
  kernel memory each**, no descriptor — until every recv armed before its
  close completes; with churn and one old keep-alive that is unbounded. After
  `sweepCloses` (8192) closes, or `sweepEvery` (10 s) with at least one, a
  pass walks the table `sweepScan` (128) slots a tick and cancels by
  user_data each recv armed before the pass and before a close
  (`armEpoch` < the pass's `closes`); `recvCancelled` re-arms it under the
  current node and the older nodes drain. Pinned sockets stay under 8192
  plus one pass's closes (a 65 536-entry table is walked in about 5 s), at
  most 12 800 cancels a second. Bytes arriving between cancel and re-arm wait
  in the socket. `TestConformance_sweepReleases` counts `/proc/net/sockstat`'s
  TCP `alloc` before and after a pass; `TestConformance_directCloseEOF` reads
  the end within 200 ms with a keeper armed first, server close and peer
  first, on 6.8, 6.12 and 6.13+.
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
- **Serving accepts are single-shot.** A multishot accept into the direct
  table keeps accepting the whole backlog before the loop sees the reserve
  reached. Past the last entry the kernel accepts, cannot install, and drops
  the connection. At most 32 single-shot accepts are in flight per listener,
  never more than the free entries above the reserve. The probe keeps a
  multishot accept.
- **A cancel the slot never asked for re-arms.** An open connection whose recv
  ends with `-ECANCELED` re-arms it, and one whose send was cancelled sends
  the same flight again.
- **The probe is staged, and any failure is epoll.** setup with
  `COOP_TASKRUN|SINGLE_ISSUER|SUBMIT_ALL` (EINVAL: again without
  `SINGLE_ISSUER`), features (`EXT_ARG`, `NODROP`, `SINGLE_MMAP`),
  `REGISTER_PROBE` (accept, recv, send, cancel, close, poll, read, nop), the
  buffer ring and the sparse table, then the loopback self-test. Under auto, a
  failure starts epoll with the reason in `State` and on the start line. An
  explicit io_uring fails with `LoopBackendUnavailable` naming the stage.
  `DEFER_TASKRUN` is a test hook (`loopHooks.deferTaskrun`) that passes the
  suite; making it the default is still deferred.
- **A handler queues what `Write` refuses.** A line the handler stopped at
  stays in the spill and comes back only with the next bytes. A handler that
  answers a pipelined request behind a refused write must therefore queue it
  itself and drain on `OnWritable`, as the conformance handler does.

## Conformance (one suite, every backend present)

`conformance_linux_test.go` runs on `iouring` and `epoll`. `requiredBackends`
is linked in, and the suite fails closed. It covers: partial consume, an
8 MiB response byte-exact behind short sends and EAGAIN, `LoopSpillFull`,
closing slot 0 while 1 lives, slot reuse with a stale `Write`, a table of 2
(epoll refuses, io_uring waits and serves after a close), MaxConns, idle,
`Post` exactly once ×20 000, `Shutdown` racing traffic with no descriptor
leaked, GOGC=1 with forced GC at the high-water mark, EMFILE at RLIMIT 64 and
300 connections past RLIMIT 128 on 3 derived rings (re-executed children),
the foreign-thread `Write` under `loopdebug`, a 2-entry buffer ring of 64-byte
buffers shuffled and 0xAA-filled (thousands of ENOBUFS), every probe stage
failed, `DEFER_TASKRUN`, and 10 000 idle connections with zero watchdog fires.
`uring_abi_linux_internal_test.go` freezes the uapi layouts and compares the
encoders with liburing's bytes.

Not built yet: the forced-full CQ of 2, the `IOSQE_ASYNC` send cancelled into
`-EALREADY`, the multishot that never completes, and a fallback with
connections still live. These are named here so nobody believes them done.

## Cost

- The LoopIO budget is allocs 0 for `Write`, `SetDeadline`, `Pressure` and a
  refused `Write` with a caller-owned `p` (`TestCore_allocs`).
- An idle connection costs at most **256 B of Go heap** and no mapped byte
  (`TestMemory_idleConnection`, 10 000 connections from a re-executed child,
  race off, its own Bazel target in `tools/alloc-lane-targets.txt`). Measured
  under Docker on Linux 7.0 arm64: 145.8 B per connection with epoll, the test
  handler's own map entry included, and 145.8 B with io_uring. The kernel's socket memory is not counted
  here; the 50k/150k/250k curve on a real host is measured outside CI.

## Verification

```
# Linux (CI, and Docker on a Mac with --security-opt seccomp=unconfined, which
# io_uring needs; under Docker's default profile auto falls back to epoll):
cd internal && GOWORK=off go test -race -tags loopdebug \
  -ldflags=-X=github.com/kitsunium/sdk/internal/service/net/loop.requiredBackends=iouring,epoll \
  ./service/net/loop/
# without the -X the suite fails closed on Linux, by design; `none` requires nothing.
bazel test //internal/service/net/loop:all
```

## Do NOT

- Import `server`, or any member of the family.
- Put a pointer in `connCtl`, or a payload byte in the Go heap on a native
  backend.
- Compact, reallocate or recycle a buffer handed to `OnData` before
  `OnBatchEnd` returns.
- Call `OnClose` from inside another callback.
- Call `setrlimit` or `GOMAXPROCS`: `State` reports both, and the consumer
  decides.
