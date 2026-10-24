<!-- updated: 2026-10-24T12:00:00Z -->
# internal/service/net/loop/

## Purpose

The network domain's event-loop engine (ADR 0186): the native backends a
plaintext stream group runs on when a `corenet.LoopHandler` serves it. epoll
runs on every Linux architecture; io_uring is still to come, on
`linux && (amd64 || arm64 || riscv64 || loong64)` after a staged probe. The
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
- **A handler queues what `Write` refuses.** A line the handler stopped at
  stays in the spill and comes back only with the next bytes. A handler that
  answers a pipelined request behind a refused write must therefore queue it
  itself and drain on `OnWritable`, as the conformance handler does.

## Cost

- The LoopIO budget is allocs 0 for `Write`, `SetDeadline`, `Pressure` and a
  refused `Write` with a caller-owned `p` (`TestCore_allocs`).
- An idle connection costs at most **256 B of Go heap** and no mapped byte
  (`TestMemory_idleConnection`, 10 000 connections from a re-executed child,
  race off, its own Bazel target in `tools/alloc-lane-targets.txt`). Measured
  under Docker on Linux 7.0 arm64: 145.8 B per connection with epoll, the test
  handler's own map entry included. The kernel's socket memory is not counted
  here; the 50k/150k/250k curve on a real host is measured outside CI.

## Verification

```
# Linux (CI, and Docker on a Mac):
cd internal && GOWORK=off go test -race -tags loopdebug \
  -ldflags=-X=github.com/kitsunium/sdk/internal/service/net/loop.requiredBackends=epoll \
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
