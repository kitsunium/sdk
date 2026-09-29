# ADR 0144 — illumos and Solaris supervise, on the word of their own kernels

- **Status**: Accepted; implemented in `pkg/v1/proc/capability.go`, `internal/service/proc/childwait/waitany_solaris.go`, `internal/service/proc/reaper/timersweep_solaris.go`, the `solarish` job of `.github/workflows/e2e-cross.yml` and two `cross-build` cells of `.github/workflows/bazel-ci.yml`.
- **Date**: 2026-09-29
- **Deciders**: kitsunium maintainers
- **Amends**: [ADR 0018](0018-sdk-cross-platform-portability.md) (both bars cover two more GOOS: `illumos` and `solaris`)
- **Related**: [ADR 0016](0016-sdk-process-supervision-domain.md) (the proc domain; `golang.org/x/sys` is banned), [ADR 0093](0093-a-sweep-takes-the-zombie-never-the-status.md) (a sweep hands a claimed child's status over), [ADR 0094](0094-a-test-compiles-where-its-package-does.md) (a test compiles where its package does), [ADR 0137](0137-a-lane-that-loops-over-modules-reads-the-census.md) (the lanes loop over the module census)

## Context

A supervisor built on this SDK was ported to illumos and Oracle Solaris. It
preflights with `proc.MissingCapabilities(CapProcessSpawn, CapReaper)` and
refused to start on both: the capability matrix named neither GOOS. The proc
implementations are tagged `unix`, which selects both, so they had always
compiled there, but neither bar had ever looked: `cross-build` had no cell for
them and `e2e-cross` no leg. `runtime.GOOS` is `illumos` on the first and
`solaris` on the second, although the `solaris` build tag selects both.

Measured before deciding: the platform-sensitive suites, `pkg`'s `./v1/proc`
suite and the conformance program, cross-compiled as for the BSD legs and run
as root in an OmniOS r151054 guest and an Oracle Solaris 11.4 guest (amd64).
Both kernels gave the same answers:

- conformance: 55 pass, 0 fail, 5 unsupported — the five a BSD reports too
  (cgroup lifecycle and placement, `SetChildSubreaper`, orphan adoption, the
  sd_notify listener);
- every suite passed except `proc/childwait` (2 tests) and `proc/reaper`
  (3 tests), each failing on every retry: no sweep ever collected a child;
- `entitlement`'s tests did not compile: `syscall.Mkfifo` does not exist there.

The sweeps failed for two reasons, both below the SDK:

1. **`wait4(-1)` does not mean "any child" there.** libc keeps SunOS 4
   semantics (illumos-gate `usr/src/lib/libc/port/gen/waitpid.c`, "Emulate
   undocumented 4.x semantics"): a negative pid is the process group `-pid`,
   and `0` is every child. `wait4(-1)` asked for process group 1 and answered
   `ECHILD` beside live children. `waitpid(-1)` keeps the POSIX meaning, but the
   stdlib exports only `Wait4` on these kernels. The stdlib wrapper also hands
   back libc's 32-bit `pid_t` without sign extension — the `-1` of a failed call
   reads `4294967295` (go1.27.1 `runtime/syscall_solaris.go`, `int(call.r1)`) —
   and reports `errno` whatever the call returned.
2. **A child's exit posts no SIGCHLD there.** The Go runtime forks every child
   with `forkx(FORK_NOSIGCHLD)` (go1.27.1 `src/syscall/exec_libc.go`), so a
   reaper loop woken by SIGCHLD alone never runs for the children its own
   process spawned. Orphans re-parented to the process still signal: the
   kernel clears the flag when it re-parents (illumos-gate
   `usr/src/uts/common/os/exit.c`).

## Decision

1. **Both bars cover `illumos/amd64` and `solaris/amd64`.** Two `cross-build`
   cells (and the same two in `scripts/cross-platform-audit.sh`), and a
   `solarish` job in `e2e-cross.yml` with one leg per GOOS — OmniOS r151054 for
   `illumos`, Oracle Solaris 11.4 for `solaris` — booted by `vmactions`, since
   `cross-platform-actions` ships no image for either. The legs run the BSD
   legs' binaries plus `pkg`'s `./v1/proc`, whose `TestSupported` asserts the
   matrix for the GOOS it runs on: the row this ADR adds is checked on the
   kernel it describes. Both legs gate like the BSD legs; each binary gets the
   BSD lane's three attempts.
2. **"Any child" is a per-kernel argument.** `childwait` passes
   `anyChildPID` to `wait4`: `-1` on Linux, darwin and the BSDs
   (`waitany_unix.go`), `0` on illumos and Solaris (`waitany_solaris.go`). And
   it reads every result as POSIX states it, on every Unix: `int32(pid) == -1`
   is a failure with its errno, anything else carries no error — which
   restores the stdlib's zero-extended `-1` there and changes nothing where the
   wrapper was already right.
3. **The reaper also sweeps on a timer where exits post no SIGCHLD.**
   `timerSweepEvery` is one second on illumos and Solaris
   (`timersweep_solaris.go`) and zero elsewhere (`timersweep_unix.go`); the loop
   selects on a ticker only when it is positive. An idle sweep is one `wait4`
   answering `ECHILD` and allocates nothing (`BENCH.md`), so the timer bounds
   how long a zombie lasts without a cost worth weighing.
4. **The matrix names both GOOS in every row the legs pass**: ProcessSpawn,
   SignalRelay, Reaper, Rlimit, UmaskNiceOOM, SdNotify, SocketActivation.
   Cgroup stays unsupported: neither kernel has cgroups, and resource controls
   (projects, `rctl`) are not a backend. `SetChildSubreaper` returns
   `UnsupportedPlatform` there, as on darwin, OpenBSD and NetBSD; the OOM knob
   fails and reports, as on the BSDs.
5. **The FIFO test creates its pipe with `makeFifo`**: `mkfifo(2)` where the
   stdlib has it, `mknod(2)` of an `S_IFIFO` node on illumos and Solaris — which
   is how their libc defines `mkfifo(3C)`.

## Consequences / Semantics

- `cross-build` runs twelve GOOS/GOARCH cells, not ten. `e2e-cross` gains two
  legs: 3m17s (OmniOS) and 3m01s (Solaris) in the first green run, of which
  about 1m45s and 1m30s boot the guest and about 50 s run every suite.
- A consumer that preflights ProcessSpawn and Reaper starts on both kernels.
- Under a running reaper, a zombie lasts at most about a second on illumos
  and Solaris, where it lasted until the next SIGCHLD elsewhere. A child a
  `Process` spawned is still collected by its own `Wait` (`wait4(pid)`, which
  both libcs read as POSIX does); the sweep reaches it only when its owner has
  not waited yet, and hands the status over as ADR 0093 describes.
- `lock` and `session` still refuse both GOOS at construction: the stdlib has
  no `syscall.Flock` there. `lock`'s suite runs in the legs and asserts the
  refusal.

## Breaking changes

None. `Supported` answers true for more (capability, GOOS) pairs; no API
changes.

## Alternatives considered

### Why not `waitpid(-1)` or `waitid(P_ALL)`

Either keeps the POSIX meaning on these kernels, and neither is exported by the
stdlib there. Reaching libc directly means `golang.org/x/sys` or a hand-rolled
`//go:cgo_import_dynamic` trampoline — the first is banned (ADR 0016), the
second is runtime plumbing the SDK does not own. `wait4(0)` is what these libcs
document for "every child".

### Why not document the missing SIGCHLD instead of adding a timer

The matrix would then mark a Reaper that does not reap in the background, and
the hand-off suite's "late" case — Wait held back until a sweep collected the
child — would have to be skipped on exactly the kernels where its premise
changed. The timer keeps the promise `pkg/v1/reaper` makes (reap children as
they exit) at the price of one idle `wait4` a second.

### Why not one leg for both kernels

They are two GOOS values, the matrix names both, and they diverge below libc:
Solaris's libc is not illumos's, and only a run on each says that `wait4(0)`
and the timer behave on both.

## Deferred

- The 15-minute cap on the legs rests on one green run per kernel; measure
  before moving it.
- A native resource-control backend (Solaris projects and `rctl`) for Cgroup,
  and a lock backend over `fcntl` locks for `lock` and `session`, on both
  kernels.

## References

- illumos-gate `usr/src/lib/libc/port/gen/waitpid.c` — `wait4`'s SunOS 4 pid semantics
- illumos-gate `usr/src/uts/common/os/exit.c` — `waitid`, and the re-parenting that clears `CLDNOSIGCHLD`
- go1.27.1 `src/syscall/exec_libc.go` — `forkx(0x1) // FORK_NOSIGCHLD`; `src/runtime/syscall_solaris.go` — `syscall_wait4`
- `forkx(2)`, `wait4(3C)`, `waitpid(3C)` — illumos and Oracle Solaris 11.4 manual pages
