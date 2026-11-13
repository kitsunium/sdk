# ADR 0222 — a loop runs on kqueue on darwin and the BSDs, beside epoll

- **Status**: Accepted
- **Date**: 2026-11-11
- **Deciders**: SDK maintainers (kitsunium/sdk#976, lot L2)
- **Related**: [ADR 0186](0186-a-plaintext-group-may-run-on-an-event-loop-and-its-http-is-a-profile-that-refuses.md) (§D5 deferred kqueue; this ADR resolves it), [ADR 0188](0188-the-operators-switches-only-restrict-and-state-says-what-the-server-runs-without.md) (the lattice and min(code, environment)), [ADR 0189](0189-a-kernel-capability-is-probed-reported-and-switched-off-by-name.md) (rows, classes, staged probes), [ADR 0196](0196-a-drain-stops-the-routing-first-takes-the-listen-queue-then-closes-and-loopsauto-reads-gomaxprocs-once.md) (the drain's take), [ADR 0190](0190-a-merge-gate-judges-the-ratio-of-two-engines-measured-side-by-side.md) (the paired gate), [ADR 0018](0018-sdk-cross-platform-portability.md) (constants cited, no `x/sys`, `UnsupportedPlatform`)

## Context

ADR 0186 §D5 left kqueue deferred, "in its own measured change". Off Linux
every loop group ran the portable backend: a reader and a writer goroutine
per connection feeding one handler goroutine per lane, heap buffers, and an
explicit native backend refused with `UnsupportedPlatform`. The core the
native backends share — the slot table, the slab, the wheel, the spill, the
dirty list, the drain's `listenerSet` — is backend-neutral by construction,
so a darwin or BSD backend is one driver and its probe. The operator's lattice
had no token for it, so `KITSUNIUM_NET_ENGINE` and `KITSUNIUM_NET_DISABLE`
could not name it either.

## Decision

### D1 — the value and the token

`LoopBackendKqueue` is appended to the backends (value 4, after portable, so
no constant moves; `String` "kqueue"). The engine token `kqueue` joins the
registry's switchable engines at epoll's rank: `iouring > epoll = kqueue >
portable > goroutine`. epoll and kqueue are the readiness poller of their
platforms, never both on one, and are switched off apart:
`KITSUNIUM_NET_DISABLE=epoll` leaves kqueue, `=kqueue` leaves epoll.
`KITSUNIUM_NET_ENGINE=kqueue` caps at that rank on every platform (epoll on
Linux). `Override.Applied` names, at that rank, the platform's own poller.
min(code, environment) is unchanged (ADR 0188).

### D2 — where it runs, and what is refused

`kqueue_bsd.go` builds on darwin, FreeBSD, OpenBSD, NetBSD and DragonFly, on
package `syscall` alone (`Kqueue`, `Kevent`, `Kevent_t`, `SetKevent` exist on
every one of them and every architecture they have; the struct's layout and
`Udata`'s type differ, so `Udata` is never used). kqueue runs there when the
code names it, `LoopValue.Backend = LoopBackendKqueue`; `auto` there stays on
the portable backend until D6's gate holds (amended below). An explicit
kqueue never falls back: a probe that refuses is `LoopBackendUnavailable`.
io_uring and epoll are refused there with
`UnsupportedPlatform`, and kqueue is refused with it on Linux and everywhere
else, by server and by the leaf. No new error code: the reservation
`0.2.11.92`–`0.2.11.95` is left unused.

### D3 — the driver mirrors epoll's, level-triggered

One locked thread, one kqueue, one ring per loop, a 2 MiB mmap'd read arena,
the slab outside the heap: epoll's shape. Filters are level-triggered, as
epoll's registrations are: a read takes one chunk and an arena may be spent,
so a readiness left pending must fire again. `EV_CLEAR` is therefore not a
capability row — it would need a read to `EAGAIN`, which the loop's
one-read-per-readiness fairness refuses. Each connection registers
`EVFILT_READ` and a disabled `EVFILT_WRITE` at accept, so every later change
is an enable or a disable that cannot fail for want of a filter. kqueue names
a descriptor, never a slot: a descriptor-to-slot table stands for epoll's
tag and generation, and a batch serves its connections' events before its
listeners' accepts, so no event of the batch can name a connection accepted
in it. `EV_EOF` on a connection that no longer reads fails it only with the
socket error the kernel put in `fflags`; a peer's plain end lets its output
finish.

The wake is a non-blocking pipe on every BSD, not `EVFILT_USER`: OpenBSD and
NetBSD's package `syscall` have no `EVFILT_USER`, and one path is one path to
test. darwin has no `accept4` and no `pipe2`: close-on-exec is set under the
fork lock, as the standard library does there. darwin sets `SO_NOSIGPIPE` on
each accepted socket. `unread` is a one-byte `MSG_PEEK`: package `syscall`
has no `FIONREAD` there and OpenBSD takes raw system calls only through its
libc. `loopdebug`'s thread check is compiled out there — no thread
identifier in package `syscall`, and OpenBSD's pinned system calls refuse a
raw one.

### D4 — the probe, staged

`kqueue` (the call), `kqueue-wake` (the pipe registered, a byte written,
reported) and `kqueue-socket` (a socket pair's read readiness, its byte read
back, its peer's close reported `EV_EOF`), on throwaway descriptors. A Unix
socket pair rather than a loopback accept: a FreeBSD jail may have no
loopback address, and the readiness and the end the loop serves on are the
same. `ProbeStage` names the last stage passed; an explicit kqueue whose
probe fails is `LoopBackendUnavailable` naming the stage.

### D5 — the rows

Every io_uring and epoll row reads unprobed, "backend kqueue". `tcp-nodelay`
and `tcp-keepalive` are set on each TCP listener before any accept, which
darwin and the BSDs copy to each socket a listener accepts; the keepalive's
idle time, interval and count where the platform names them (darwin's
`TCP_KEEPALIVE`, `TCP_KEEPINTVL`, `TCP_KEEPCNT`; FreeBSD's, NetBSD's and
DragonFly's `TCP_KEEPIDLE` family; OpenBSD has only the system's).
`tcp-user-timeout`, `tcp-defer-accept` and `so-incoming-cpu` are Linux's and
read absent — a class A-code one the code opted into is missing, the fault
State reports; `tcp-notsent-lowat` is not built on kqueue and reads the same.
`loop-affinity` is missing when `Affinity` names a CPU: package `syscall` has
no thread-affinity call there. `reuseport-loops` reads `SO_REUSEPORT` (0x200)
back from each per-loop listener. A file body (`LoopFileWriter`) is refused
with `LoopFileRefused`, and `LoopPeer` answers false: neither is built here.

### D6 — the gate before auto is trusted further

The backend-neutral conformance cases run on kqueue (the cases moved to
`conformance_native_test.go` and `conformance_drain_native_test.go`, and the
idle-memory bound to `memory_native_test.go`); on darwin and the BSDs kqueue
is required by default wherever the binary runs. The drain cases queue below
the default listen backlog of 128 there, and Shards(0)'s case stays
Linux's. The paired gate of ADR 0190 — `BenchmarkLoadL1/pair=goroutine:loop`
on the macos-15 lane, the ratio's interval in BENCH.md, held at P6's
threshold — is the next step; until its numbers exist, BENCH.md says kqueue
is unmeasured, and kqueue is not `auto`'s choice (amended below).

## Amended 2026-11-11 — kqueue is opt-in until its gate holds

The backend has never run on darwin or a BSD, and D6's gate — the paired
`goroutine:loop` bench on the macos-15 lane, its ratio's interval held at
P6's threshold by `tools/netgate` — does not exist yet. A backend that has
never run does not become the users' default: `server`'s plan for `auto` on
darwin and the BSDs is the portable backend (`kqueuePlan`), whatever the
environment says, and kqueue runs only when the code names it,
`LoopValue.Backend = LoopBackendKqueue`, as any other opt-in. The operator's
tokens keep their ADR 0188 meaning — they only lower: `KITSUNIUM_NET_ENGINE`
caps an explicit kqueue, `KITSUNIUM_NET_DISABLE=kqueue` switches it off, and
neither raises `auto` to it. The probe, the refusals and State are unchanged.
The leaf's own `auto` still probes kqueue: `loop`'s conformance suite tests
it explicitly and keeps requiring kqueue on darwin and the BSDs (D6); `server`
never hands it `auto` there. `TestOperatorPlan_kqueueIsOptIn` and
`checkKqueuePlan` pin the plan on any platform, Linux included; `auto` becomes
kqueue in the change that lands the gate's numbers.

## Consequences

- A darwin or BSD consumer of `HandleLoop` runs on the portable backend by
  default and on kqueue when it names `LoopBackendKqueue`; an operator lowers
  that with `KITSUNIUM_NET_ENGINE=portable` or `KITSUNIUM_NET_DISABLE=kqueue`.
- The code has never run on darwin or a BSD in this change: the darwin lane
  and the FreeBSD, OpenBSD and NetBSD guests are its first run; DragonFly only
  compiles.
- IOCP and illumos event ports stay rejected (ADR 0186 §D5).

## Amendment 2026-11-13 — the conformance ran, the gate's lane is built (kitsunium/sdk#976)

The suites of `internal/service/net/loop` ran on kqueue: e2e-cross on `main` at
`a7eb89f0d` (run 43326746134) passed the package on macos-15 (arm64), FreeBSD,
OpenBSD and NetBSD — the guests' logs show its groups starting on
`backend=kqueue`, probe `kqueue-socket` —, race off everywhere, so
`TestMemory_idleConnection` ran too. D6's
gate now has its lane: net-kernel-gate's `bench-darwin` job runs `BenchmarkLoadL1`
on macos-15, where its pairs are `goroutine:loop-kqueue` and
`loop-kqueue:loop-kqueue` (`load_gate_hooks_darwin_test.go`), ten rounds, and
`tools/netgate -only kqueue,cal-L1-kqueue` judges them, reported. `auto` stays on
the portable backend until those rows hold, in the change that cites them.

## Amendment 2026-11-13 (second) — `auto` takes kqueue on darwin, not on the BSDs (kitsunium/sdk#976)

D6's gate held on macos-15. The `bench-darwin` job of net-kernel-gate run
43358351908 (#1368, commit `e8b003d3f`; Darwin 24.6.0, Apple M1 (Virtual),
3 CPUs) ran `BenchmarkLoadL1` ten paired rounds, and its
`netgate-darwin-report.json` reads `goroutine:loop-kqueue` at a `cpu-ns/req`
ratio interval of 0.3313–0.3995 (median 0.3767), against P6's 0.7: pass. The
calibration row, `loop-kqueue:loop-kqueue`, read 0.8946–1.068 (median
0.9867) and does not hold its ±5 %: the virtual runner is noisier than
Linux's. Its widest deviation from 1 would move the kqueue row's upper bound
to 0.4466, still under 0.7, so the verdict does not depend on it. The rows
are in `internal/service/net/server/BENCH.md` (edition 2026-11-13).

So, as criterion 1 of #976 amended reads — `auto` prefers kqueue in the change
that cites criterion 3's rows —, `server`'s plan for `auto` on **darwin** is
kqueue, with its fallback: a probe that refuses kqueue runs the group on the
portable backend, reported, as `auto` falls back on Linux (`kqueuePlan`,
`kqueueAuto`). On **FreeBSD, OpenBSD, NetBSD and DragonFly** no row has been
measured — the bench lane is macOS's alone —, so `auto` stays on the portable
backend there and kqueue runs when the code names it, as the amendment of
2026-11-11 decided for every platform of the family. A BSD's `auto` moves in
the change that cites a row measured on that BSD.

This changes a default: a darwin group whose code leaves `LoopValue.Backend`
at `auto` ran portable and now runs kqueue. The pull request carries the
`net:default-change` label and its `Override:` block names the way back,
`KITSUNIUM_NET_ENGINE=portable` (`KITSUNIUM_NET_DISABLE=kqueue` does the same
for kqueue alone); the operator's tokens keep their ADR 0188 meaning and only
lower. `TestKqueueAuto` and `TestOperatorPlan_kqueueAuto` pin the plan on any
platform, darwin's and FreeBSD's side by side; `TestLoopKqueue_autoAndTheOperator`
and `TestLoopGroup_serves` start `auto` on the darwin lane (kqueue) and the BSD
guests (portable).
