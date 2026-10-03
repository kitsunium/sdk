<!-- updated: 2026-10-03T12:00:00Z -->
# internal/service/proc/exec

The keystone spawn primitive of the process-supervision domain (ADR 0016).
`Start` turns an immutable `coreproc.Spec` into a running, supervised process and
returns a `coreproc.Process` handle. This is the only package in the domain that
forks a process; the others (signal, reaper, rlimit, cgroup, systemd/notify) act on a
process that already exists.

## Layering & deps

- Imports: stdlib (`os`, `os/exec`, `os/user`, `syscall`, `context`, `sync`,
  `time`, `strconv`, `strings`, `errors`, `io`, `io/fs`, `path/filepath`, and
  `unsafe` in `joblimits_windows.go`) + `internal/core/proc` +
  `internal/kernel/errs` + `internal/kernel/clock` (the handles' grace timer,
  Unix and Windows) + `internal/service/proc/childwait` (Unix files only) +
  `internal/service/proc/internal/rlim` (the trampoline's `syscall.Rlimit`, the
  constructor `rlimit` shares).
  No `golang.org/x/sys`, no `pkg/*`.
- Returns `coreproc.Process` (the port interface); the concrete `handle` type is
  unexported.
- Every error is a central `coreproc` sentinel — wrapped via the local
  `wrap*` helpers in `wrap.go`, never re-`Define`d.

## File map

| File | Build tag | Role |
|---|---|---|
| `exec.go` | all | package doc + `validateSpec` (empty Path ⇒ `InvalidSpec`) |
| `exec_unix.go` | `unix` | `Start`: validate → check limits → resolve creds → `SysProcAttr` → `os.StartProcess` through `childwait.Spawn` (`forkClaimed`) → post-start attrs; `teardown` on attr failure; `awaitTrampoline` reaps a trampoline that failed before exec, through the claim |
| `exec_other.go` | `!unix && !windows` | `Start` ⇒ `UnsupportedPlatform` (compiles everywhere) |
| `exec_windows.go`, `handle_windows.go`, `joblimits_windows.go`, `cgroup_placement_windows.go` | `windows` | the CreateProcess backend: stdio, a console process group for `Setpgid`, rlimits through a Job Object; the Unix-only fields refused with `UnsupportedPlatform` |
| `lookpath.go` | `unix \|\| windows` | `resolveSpec`: a bare `Spec.Path` searched in the CHILD's PATH (Spec.Env's, else the parent's), os/exec's rules — first executable wins, a relative match is `exec.ErrDot`, none is `exec.ErrNotFound`, both wrapped in `SpawnFailed`; argv[0] keeps the name as written |
| `lookpath_unix.go` / `lookpath_windows.go` | per OS | the candidate spellings (PATHEXT on Windows), what "executable" means (an execute bit / the extension), and how variable names compare (case-insensitive on Windows) |
| `handle_unix.go` | `unix` | the `handle` value: `PID`/`Wait`/`Signal`/`SignalGroup`/`Stop`, once-only reap through `collectExit` (claim first, own wait, `Reclaim` on ECHILD — it takes a `waiter`, the process's own wait and nothing else; `releaser` adds the handle release an aborted spawn needs), exit translation (`exitValue`), stdio-copier join |
| `stdio.go` | `unix \|\| windows` | `buildStdio`: wires `Spec.Stdio` (inherit/null/capture) to `ProcAttr.Files`; capture pipes + copier goroutines joined by `Wait` (100% delivery, no leak) |
| `trampoline_unix.go` | `unix` | the re-exec trampoline (`installTrampoline`, `childHandshakeFD`) — see below |
| `handshake_unix.go` | `unix` | the parent side of the trampoline's status pipe and `handshakeError` (status byte → sentinel) |
| `creds_unix.go` | `unix` | `Spec.User/Group/Groups` → `syscall.Credential` via `os/user` |
| `attrs_unix.go` | `unix` | best-effort `Nice` (setpriority) + `OOMScoreAdj` (procfs); ESRCH detection |
| `zombie_darwin.go` / `zombie_other.go` | `darwin` / `unix && !darwin` | `leaderIsZombie`: on darwin, `getpgid(2)` answers ESRCH for an exited, unreaped child (see §Stop); `false` on every other kernel, which never needs it |
| `limits_unix.go` | `unix` | `checkLimits`: `UnknownResource` for unmapped, `RlimitFailed` for unhonourable |
| `cgroup_placement_linux.go` | `linux` | `validateCgroupPath` (pre-spawn: missing/not-a-cgroup ⇒ `CgroupUnavailable`) + `applyCgroupPlacement` (trampoline writes pid → `cgroup.procs`) |
| `cgroup_placement_other.go` | `unix && !linux` | `validateCgroupPath` rejects a non-empty path with `UnsupportedPlatform`; no cgroup v2 off Linux |
| `limittable_unix.go` | `unix` | `Resource` → `RLIMIT_*` table (stdlib constants only) |
| `limittable_as.go` / `limittable_openbsd.go` | `unix && !openbsd` / `openbsd` | `addPlatformLimits`: `RLIMIT_AS` where the stdlib exports it; nothing on OpenBSD, whose kernel has none, so `ResourceAS` is `UnknownResource` there |
| `maxrss_rss64_unix.go` / `maxrss_rss32_unix.go` | `unix` except / only on `386 \|\| arm \|\| mips \|\| mipsle` | `maxRSSKB`: `Rusage.Maxrss` as `int64`, widened from the `int32` those four 32-bit targets declare |
| `procfile_unix.go` | `unix` | `os.WriteFile` shim for `oom_score_adj` |
| `wrap.go` | all | `wrap{Spawn,Wait,Signal,Stop,Rlimit,CgroupUnavailable,StdioCapture,UnknownUser,UnknownGroup}` — restate each sentinel's exact fields once |

## Spawn semantics

- **Environment.** `Spec.Env == nil` spawns with an **empty** environment, never
  the supervisor's — the port's known-state guarantee. A non-nil slice is used
  verbatim (pass `os.Environ()` to inherit deliberately).
- **argv.** Empty `Spec.Args` defaults argv to `[Path]` — the name as the
  caller wrote it, not the resolved file; otherwise `Args` is the full argv
  (including argv[0]).
- **Finding the executable.** `os.StartProcess` does not search PATH — a bare
  `go` is a file in the current directory to execve, and fails with ENOENT —
  so `resolveSpec` runs before either spawn path (the trampoline execs the
  target itself). A value with a separator is a path and is left as written.
  A bare name is searched in the PATH the CHILD will see: `Spec.Env`'s last
  `PATH=` entry whenever it names one — an explicitly empty `PATH=` searches
  nothing rather than falling back — and the parent's only when it names none,
  including a nil `Spec.Env`, whose child environment is empty. The first executable in PATH
  order wins; a match through a relative entry (`.` or empty) is refused with
  `exec.ErrDot` rather than skipped, as os/exec refuses it, because skipping
  would run a different program from the one the order selects.
- **Topology.** `Setpgid` makes the child a process-group leader (its pid is the
  pgid), so `SignalGroup`/`Stop` reach forked grandchildren. `Setsid` starts a
  new session detached from the controlling tty.
- **Credentials.** User/Group/Groups accept names or numeric ids. A numeric uid
  with no passwd entry is honoured (gid 0); a name that does not resolve is
  `UnknownUser`/`UnknownGroup`.

## Stop: group-aware SIGTERM → SIGKILL escalation

`Stop(ctx, grace, sig)` sends `sig` to the whole group (`kill(-pgid, sig)`),
waits up to `grace` for exit, then escalates to `SIGKILL` on the group. It
returns `nil` once the group is gone, `ctx.Err()` if cancelled first, or
`StopFailed` if the group survives the SIGKILL escalation for a non-ESRCH reason.
A group that vanished (`ESRCH`) at any phase is treated as success. The reap runs
under `sync.Once`, so concurrent `Stop`/`Wait` callers share one wait4 and one
`close(done)`.

**darwin says "gone" differently.** A group whose leader has exited but not
been reaped holds nothing alive, and XNU's `kill(-pgid)` skips zombies, finds
nobody to signal and answers **EPERM**, where Linux and the BSDs count the
zombie and report success. That happens on the graceful signal when the leader
died before `Stop` was called, and on the escalation when it died inside the
grace window before the background reap collected it — and `Stop` used to
report either as `SIGNAL_FAILED`. `zombieGroupRefused` reads that EPERM as
gone only when the leader was still unreaped when the signal left and is dead
now: collected since, or a zombie by `leaderIsZombie` (darwin's `getpgid(2)`
answers ESRCH for one, measured on darwin 25.6, while a live child answers with
its group; the pid cannot be reused while unreaped). An EPERM from a LIVE
member this process may not signal — a leader that changed its credentials —
or one sent after the leader was collected stays `SIGNAL_FAILED`.
`Test_handle_StopOnALeaderThatDiedUnreaped` and
`Test_handle_zombieGroupRefused` pin both sides, with an oracle independent of
the probe (darwin: the group's own EPERM; Linux: `/proc`'s state letter).

The grace window is a timer on the handle's clock — `clock.System` from
`newHandle`, on Unix and on Windows alike — so `Test_handle_graceOnInjectedClock`
closes an hour of grace by advancing a `ManualClock` and pins that neither
`awaitExit` nor `Stop` returns a nanosecond earlier.

## Exit status ownership (ADR 0093)

Two things in the SDK call `wait4` on this package's children: the handle's own
`os.Process.Wait` (`waitid(P_PIDFD)` on Linux ≥ 5.4, `wait4(pid)` elsewhere),
and a running reaper's `childwait.ReapAny` (`wait4(-1)`), which collects ANY
exited child. The reaper runs only when a consumer starts it (pid 1 or a
subreaper); without it the handle's own wait always collects its child. So the
Unix spawn forks through `childwait.Spawn`, which claims the child's exit status
before any sweep can treat it as an orphan's — even a child that exits before
`os.StartProcess` returns — and `collectExit` reads the status from whichever
waiter took it:

1. a sweep already collected it → the status on the claim, and NO wait by pid
   (the pid may belong to another process by now);
2. otherwise the handle's own `os.Process.Wait` — the only path when no reaper
   runs;
3. that wait fails with ECHILD → `Claim.Reclaim` waits for the sweep's hand-off
   and returns the status it stored.

`WAIT_FAILED` therefore means a status nothing in the SDK collected — a child
reaped by code outside it — or a `wait4` fault; never a status a sweep took.

A status taken from the claim also releases the `os.Process` (`releaseProc`,
under a lock `Signal` shares, since `Release` writes the `Pid` field a pid-mode
`Signal` reads): its own `Wait` never completed, so nothing else would free its
pidfd before a garbage collection. Once the leader is reaped — by `Wait` or by a
sweep — nothing is sent to its pid: `Signal` reports it finished
(`os.ErrProcessDone`, as after `Wait`), and `SignalGroup` without a private
group reports `ESRCH` (which `Stop` reads as gone); a private group is still
addressed, since it can outlive its leader. The trampoline's aborted-spawn reap
goes through `collectExit` too. The ledger itself is
`service/proc/childwait/CLAUDE.md`.

## Exit translation

`Wait` returns `coreproc.ExitValue` built by `exitValue` from the wait4 status
word and rusage — read from `*os.ProcessState` after the handle's own wait, or
from the claim when a sweep collected the child:
`WaitStatus.Exited()/ExitStatus()` → `Code` (−1 when signalled),
`WaitStatus.Signaled()/Signal()` → `Signal`/`Signaled`, and the wait4
`*syscall.Rusage` → `UserTime`/`SystemTime` (from `Utime`/`Stime`) and `MaxRSS`
(`ru.Maxrss`, kilobytes).

## Rlimits & Umask: the re-exec trampoline

The Go runtime exposes **no** `SysProcAttr` hook to run `setrlimit(2)` or
`umask(2)` in the child between fork and exec. `Start` therefore honours them via
a **re-exec trampoline** (`trampoline_unix.go`), stdlib-pure and dependency-free:

- When a `Spec` sets a mappable `Rlimits` entry, a non-nil `Umask`, **or a
  non-empty `CgroupPath`**, `Start`
  spawns `os.Executable()` (this binary) with argv `[self, target, argv…]` and a
  sentinel env var carrying the encoded limits. A package-level var initialiser
  (`var _ = installTrampoline()` — the no-init idiom) fires before `main` in that
  child, detects the sentinel, applies the limits via `setrlimit`/`umask`, strips
  the sentinel, then `syscall.Exec`s the real target. The pid is preserved across
  the `execve`, so `Wait`/`Signal`/`Stop` and the stdio pipes all still apply.
- An `Rlimits` key naming a resource with no stdlib `RLIMIT_*` mapping
  (`ResourceNProc`, `ResourceMemLock` — only in `golang.org/x/sys`, banned) is
  rejected with `UnknownResource` **before** the spawn (`checkLimits`).
- A limit the kernel **refuses** (e.g. an invalid soft>hard pair, or raising a
  hard cap unprivileged), or a failed `execve` of the target, is reported through
  a **handshake pipe** (`handshake_unix.go`): the trampoline inherits the pipe
  write end and writes a status byte (`'A'` apply / `'C'` cgroup / `'E'` exec) on
  failure, then arms it close-on-exec so a clean `execve` closes it (the parent
  reads EOF = success). The pipe is appended **after** any `Spec.ExtraFiles`, so
  the extras keep their contracted fd 3.. (socket activation) and the handshake
  lands at fd `3+len(ExtraFiles)`; the parent passes that descriptor to the
  trampoline via `__KITSUNIUM_SDK_PROC_HSFD` (`childHandshakeFD`, default 3 when
  there are no extras). `Start` blocks on that read and surfaces a typed
  `RlimitFailed` (apply) / `CgroupWriteFailed` (cgroup) / `SpawnFailed` (exec) —
  matching the direct-spawn contract instead of a bare 126/127 child exit. This is the same self-pipe + cloexec trick `os/exec`
  uses for its own errpipe. A failure to locate `os.Executable()` likewise
  surfaces `RlimitFailed` before any spawn.
- `Nice` and `OOMScoreAdj` are honoured post-start on the live pid
  (`setpriority(2)` / `/proc/<pid>/oom_score_adj`). A host refusal surfaces
  `RlimitFailed` and the half-configured child is killed + reaped (`teardown`).
- **`CgroupPath` (cgroup v2 pre-exec placement, issue #91).** A non-empty
  `Spec.CgroupPath` rides in its own sentinel env var (`__KITSUNIUM_SDK_PROC_CGROUP`,
  separate from the `;`-delimited limits payload so the path never needs escaping).
  After the rlimit/umask step and **before** `execve`, the trampoline writes its
  own pid to `<CgroupPath>/cgroup.procs` (`applyCgroupPlacement`), so the target
  is a member of the group from its first instruction — closing the unconfined
  window a post-spawn `Group.Add(pid)` leaves open. `Start` validates the path
  up front (`validateCgroupPath`): a missing path / non-cgroup directory ⇒
  `CgroupUnavailable` before any spawn; a write the kernel refuses (not delegated,
  controller off) surfaces `CgroupWriteFailed` through a distinct handshake byte
  (`'C'`). Linux-only — `cgroup_placement_other.go` rejects a non-empty path with
  `UnsupportedPlatform` on every other Unix, never running the target unconfined.

Cross-platform: the trampoline is `//go:build unix` (Linux, Darwin, the BSDs);
the only platform-divergent piece is the `syscall.Rlimit` field type — `int64` on
FreeBSD/DragonFly, `uint64` elsewhere — and the trampoline never names it: it
calls `rlim.Make` (`internal/service/proc/internal/rlim`), the build-tagged
constructor it shares with `rlimit`. Non-Unix targets never reach it
(`exec_other.go` returns `UnsupportedPlatform`).

Footgun: a binary linking this package that is run with the sentinel env var set
will re-exec. `Start` sets it only on the trampoline child and strips it before
the `execve`, so a normal run never has it; do not set it by hand.

**Cost — measured, see `BENCH.md`.** The trampoline is not a micro-optimisation
question: it is **5.8×** a direct spawn (3 654 µs vs 626 µs for `/bin/true`),
because the child it execs first is `os.Executable()` — the SUPERVISOR's binary,
which must be loaded, relocated and fully package-initialised before `setrlimit`
runs. The multiplier therefore scales with the size of the caller's binary, not
the target's (measured against a 5.3 MB binary; a 50 MB service binary pays more).
`Nice` and `OOMScoreAdj` do NOT trigger it — they are applied post-start on the
live pid — so a limit expressible either way is orders of magnitude cheaper as
`Nice`/`OOMScoreAdj`/a post-spawn `cgroup.Group.Add` than as `Rlimits`.

## Performance

`BENCH.md` answers "how much of `Start` is the kernel and how much is SDK
overhead": **0.06 %** is SDK preparation (377 ns of a 626 µs `/bin/true` launch),
and a CPU profile of a launch puts **zero** samples in any preparation function.
Nothing on the direct-spawn path was optimised, because nothing on it is
measurable. The two costs that are worth a caller's attention are the trampoline
above and `StdioCapture`, whose pipe + null-device setup is 4.9 % of a launch —
both descriptor and exec costs, neither of them this package's own code.

## Tests

`exec_unix_external_test.go` (black-box, `unix`) spawns real children through
`/bin/sh`: the refusals checked before any OS work (a cancelled context,
`InvalidSpec`, `UnknownResource`, a cgroup path that is no control group —
`CgroupUnavailable` on Linux, `UnsupportedPlatform` elsewhere), limits read
back from inside the child, trampoline failures arriving typed (`RlimitFailed`
/ `SpawnFailed`), `ExtraFiles` keeping fd 3 through the trampoline, a nil
`Spec.Env` leaking nothing, and the three stdio modes. The handle is pinned
white-box in `handle_unix_internal_test.go`: `Stop` escalates
`SIGTERM`→`SIGKILL` for a child that ignores `SIGTERM`, `SignalGroup` reaches a
grandchild, `Wait` is memoised, a signalled exit reports code −1, and a leader
that died unreaped stops cleanly on darwin too (§Stop);
`creds_unix_internal_test.go` pins `UnknownUser` / `UnknownGroup`.
`exec_windows_test.go` covers the Windows backend (skipped where `cmd.exe` is
not found) and `exec_other_test.go` the `UnsupportedPlatform` stub.
