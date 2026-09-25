# internal/service/proc/exec

The keystone spawn primitive of the process-supervision domain (ADR 0016).
`Start` turns an immutable `coreproc.Spec` into a running, supervised process and
returns a `coreproc.Process` handle. This is the only package in the domain that
forks a process; the others (signal, reaper, rlimit, cgroup, sdnotify) act on a
process that already exists.

## Layering & deps

- Imports: stdlib (`os`, `os/user`, `syscall`, `context`, `sync`, `time`,
  `strconv`, `errors`) + `internal/core/proc` + `internal/kernel/errs` +
  `internal/service/proc/childwait` (Unix files only). No `golang.org/x/sys`,
  no `pkg/*`.
- Returns `coreproc.Process` (the port interface); the concrete `handle` type is
  unexported.
- Every error is a central `coreproc` sentinel — wrapped via the local
  `wrap*` helpers in `wrap.go`, never re-`Define`d.

## File map

| File | Build tag | Role |
|---|---|---|
| `exec.go` | all | package doc + `validateSpec` (empty Path ⇒ `InvalidSpec`) |
| `exec_unix.go` | `unix` | `Start`: validate → check limits → resolve creds → `SysProcAttr` → `os.StartProcess` through `childwait.Spawn` (`forkClaimed`) → post-start attrs; `teardown` on attr failure |
| `exec_other.go` | `!unix && !windows` | `Start` ⇒ `UnsupportedPlatform` (compiles everywhere) |
| `exec_windows.go`, `handle_windows.go`, `joblimits_windows.go`, `cgroup_placement_windows.go` | `windows` | the CreateProcess backend: stdio, a console process group for `Setpgid`, rlimits through a Job Object; the Unix-only fields refused with `UnsupportedPlatform` |
| `lookpath.go` | `unix \|\| windows` | `resolveSpec`: a bare `Spec.Path` searched in the CHILD's PATH (Spec.Env's, else the parent's), os/exec's rules — first executable wins, a relative match is `exec.ErrDot`, none is `exec.ErrNotFound`, both wrapped in `SpawnFailed`; argv[0] keeps the name as written |
| `lookpath_unix.go` / `lookpath_windows.go` | per OS | the candidate spellings (PATHEXT on Windows), what "executable" means (an execute bit / the extension), and how variable names compare (case-insensitive on Windows) |
| `handle_unix.go` | `unix` | the `handle` value: `PID`/`Wait`/`Signal`/`SignalGroup`/`Stop`, once-only reap through `collectExit` (claim first, own wait, `Reclaim` on ECHILD), exit translation (`exitValue`), stdio-copier join |
| `stdio_unix.go` | `unix` | `buildStdio`: wires `Spec.Stdio` (inherit/null/capture) to `ProcAttr.Files`; capture pipes + copier goroutines joined by `Wait` (100% delivery, no leak) |
| `creds_unix.go` | `unix` | `Spec.User/Group/Groups` → `syscall.Credential` via `os/user` |
| `attrs_unix.go` | `unix` | best-effort `Nice` (setpriority) + `OOMScoreAdj` (procfs); ESRCH detection |
| `limits_unix.go` | `unix` | `checkLimits`: `UnknownResource` for unmapped, `RlimitFailed` for unhonourable |
| `cgroup_placement_linux.go` | `linux` | `validateCgroupPath` (pre-spawn: missing/not-a-cgroup ⇒ `CgroupUnavailable`) + `applyCgroupPlacement` (trampoline writes pid → `cgroup.procs`) |
| `cgroup_placement_other.go` | `unix && !linux` | `validateCgroupPath` rejects a non-empty path with `UnsupportedPlatform`; no cgroup v2 off Linux |
| `limittable_unix.go` | `unix` | `Resource` → `RLIMIT_*` table (stdlib constants only) |
| `procfile_unix.go` | `unix` | `os.WriteFile` shim for `oom_score_adj` |
| `wrap.go` | all | `wrap{Spawn,Wait,Signal,Stop,Rlimit,UnknownUser,UnknownGroup}` — restate each sentinel's exact fields once |

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
  `PATH=` entry when it has one, the parent's otherwise — including a nil
  `Spec.Env`, whose child environment is empty. The first executable in PATH
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
FreeBSD/DragonFly (`rlimit_value_signed.go`), `uint64` elsewhere
(`rlimit_value_default.go`). Non-Unix targets never reach it (`exec_other.go`
returns `UnsupportedPlatform`).

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

`exec_external_test.go` (black-box) covers the ADR acceptance criteria:
group-kill leaves no survivor, `Stop` escalates `SIGTERM`→`SIGKILL` for a child
that ignores `SIGTERM`, `Wait` reports the exact normal/signalled status, plus
the typed-error contracts (`InvalidSpec`, `UnknownUser/Group`,
`UnknownResource`, `RlimitFailed`, context cancellation). Spawn tests are gated
on a `/bin/sh` probe and skip cleanly where the host cannot run them; the
typed-error tests hold on every platform.
