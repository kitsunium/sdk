<!-- updated: 2026-11-08T00:00:00Z -->
# internal/service/proc/exec

The keystone spawn primitive of the process-supervision domain (ADR 0016).
`Start` turns an immutable `coreproc.Spec` into a running, supervised process and
returns a `coreproc.Process` handle. This is the only package in the domain that
forks a process; the others (signal, reaper, rlimit, cgroup, systemd/notify) act on a
process that already exists.

## Layering & deps

- Imports: stdlib (`os`, `os/exec`, `os/user`, `syscall`, `context`, `sync`,
  `time`, `strconv`, `strings`, `errors`, `io`, `io/fs`, `path/filepath`,
  `encoding/binary` and `runtime` for the sandbox, and `unsafe` in the raw
  calls) + `internal/core/proc` +
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
| `doc.go` | all | the package comment — kit writes it from the design (ADR 0167) |
| `exec.go` | all | `validateSpec` (empty Path ⇒ `InvalidSpec`; a `Spec.Env` key beginning `__KITSUNIUM_SDK_PROC_`, any case ⇒ `ReservedEnv`, #726), `reservedEnvKey`, `reservedEnvPrefix` |
| `exec_unix.go` | `unix` | `Start`: validate → check limits → resolve creds → `SysProcAttr` → `os.StartProcess` through `childwait.Spawn` (`forkClaimed`) → post-start attrs; `teardown` on attr failure; `awaitTrampoline` reaps a trampoline that failed before exec, through the claim |
| `exec_other.go` | `!unix && !windows` | `Start` ⇒ `UnsupportedPlatform` (compiles everywhere) |
| `exec_windows.go`, `handle_windows.go`, `joblimits_windows.go`, `cgroup_placement_windows.go`, `ctrlevent_windows.go` | `windows` | the CreateProcess backend: stdio, a console process group for `Setpgid`, rlimits through a Job Object the child is assigned to while suspended (§A confined Windows child is created suspended); the Unix-only fields refused with `UnsupportedPlatform` |
| `handle_pidfd_linux.go` / `handle_procfd_windows.go` / `handle_nopidfd_unix.go` | `linux` (not mips) / `windows` / other `unix` | the `ProcessHandle` sibling port (#928) on the Start handle: `Handle` duplicates the pidfd (`F_DUPFD_CLOEXEC`) or the process HANDLE (`DuplicateHandle`) the runtime holds (`os.Process.WithHandle`); `SignalHandle` is `pidfd_send_signal` (424, cited) — ESRCH once reaped, never a pid; `UnsupportedPlatform` elsewhere |
| `open.go`, `open_linux.go` / `open_windows.go` / `open_other.go`, `foreign_wait.go` | all / `linux` (not mips) / `windows` / rest | `Open(pid)` (#928): a `foreign` handle on a process not spawned here — the pidfd `os.FindProcess` opens (its absence classified by one `pidfd_open`, 434: ESRCH is gone, else `HANDLE_UNAVAILABLE`), a HANDLE `OpenProcess` gives (terminate, synchronize, query); Signal/SignalGroup/SignalHandle through it, Stop watching it (`ppoll` slices / `WaitForSingleObject` slices), Wait and WaitContext refused with `WAIT_FAILED` wrapping ECHILD |
| `attach.go`, `attach_handle.go`, `attach_linux.go` / `attach_bsd.go` / `attach_other.go`, `pidfile.go`, `pidfile_linux.go` / `pidfile_bsd.go` | all, `linux` (not mips) or darwin and the BSDs / `linux` (not mips) / darwin and the BSDs / rest | `Attach` and `AttachPIDFile` (#729): `adopted` — a non-blocking pidfd in the runtime's poller on Linux (`PIDFD_NONBLOCK`, or `fcntl` before 5.10), two kqueue `EVFILT_PROC` knotes on darwin and the BSDs (`attach_bsd.go`) —, its `Wait` returning `STATUS_UNKNOWN` at the end; the methods both share (`attach_handle.go`); `pidWalk`, the PID file read one component at a time from `/` through a held directory (`heldDir`: O_PATH and `/proc/self/fd` on Linux, `os.Root` elsewhere), its trust; the bounds from `/proc` on Linux, from the kernel's process record (`proc/internal/bsdabi`) elsewhere; `UNSUPPORTED_PLATFORM` on illumos, Solaris and Windows |
| `scheduling.go`, `scheduling_linux.go` / `scheduling_freebsd.go` / `scheduling_other.go` | all / `linux` / `freebsd` / rest | `Spec.Scheduling` (#940): `checkScheduling` (an undefined policy or class, or an I/O level past 7, is `INVALID_SPEC`; what the platform cannot apply `UNSUPPORTED_PLATFORM`, before the spawn), `applyScheduling` post-start beside `Nice` — `sched_setscheduler` and `ioprio_set` on Linux, `rtprio(RTP_SET)` for FIFO/RR/Idle on FreeBSD |
| `reclaim.go`, `reclaim_linux.go` / `reclaim_other.go` | all, `linux` / rest | `Reclaim` (#738, S0h): a child this process spawned before an execve, held again — a pidfd and the `childwait` claim, so `Wait` reads its real status; a pid gone, reaped or another process's is `ADOPTION_REFUSED`, below 1 `INVALID_SPEC`; `UnsupportedPlatform` elsewhere |
| `pty_supported.go`, `pty_linux.go` / `pty_darwin.go` / `pty_freebsd.go` / `pty_openbsd.go`, `pty_unsupported.go` / `pty_unsupported_unix.go` | `linux \|\| darwin \|\| freebsd \|\| openbsd`, per kernel / the rest | `StdioPTY` (#934): `openPTY` per kernel (`/dev/ptmx` + `TIOCSPTLCK`/`TIOCGPTN`; `/dev/ptmx` + `TIOCPTYGRANT`/`UNLK`/`GNAME`; `posix_openpt` + `TIOCGPTN`; `/dev/ptm` + `PTMGET`), `buildPTYStdio` (the slave as fd 0–2, the master kept for `TTY`), the `Terminal` sibling port (`TTY`, `Resize` = `TIOCSWINSZ`, `Size` = `TIOCGWINSZ`, every ioctl through `SyscallConn().Control` so the master stays in the poller); `checkPTY` refuses elsewhere |
| `run.go` | all | `Run` (#935): `Start` with the capture replaced by two `headBuffer`s (the first `MaxOutputBytes` kept, the rest accepted and dropped, growth by doubling capped at the ceiling), then `WaitContext`; on ctx's end `Stop(SIGTERM, Grace)` uncancelled, the exit collected, ctx's error returned |
| `env.go` / `samevariable_other.go` | all / `!unix && !windows` | `EnvAllow` / `EnvDeny` (#935): pure filters over `KEY=value` entries, parent order kept, a repeated key keeping its last value at its last place, names compared by `sameVariable` (case-insensitive on Windows); `dedupEnv`, the spawn's own copy of `Spec.Env`: each name once, in linear time (§Spawn semantics); `envKey`, a leading `=` part of the name (Windows' `=C:=C:\dir`) |
| `lookpath.go` | `unix \|\| windows` | `resolveSpec`: a bare `Spec.Path` searched in the CHILD's PATH (Spec.Env's, else the parent's), os/exec's rules — first executable wins, a relative match is `exec.ErrDot`, none is `exec.ErrNotFound`, both wrapped in `SpawnFailed`; argv[0] keeps the name as written |
| `lookpath_unix.go` / `lookpath_windows.go` | per OS | the candidate spellings (PATHEXT on Windows), what "executable" means (an execute bit / the extension), and how variable names compare (case-insensitive on Windows: `sameVariable`, and `variableKey`, the form two names of one variable share, the fold classes `strings.EqualFold` compares by) |
| `handle_unix.go` | `unix` | the `handle` value: `PID`/`Wait`/`Signal`/`SignalGroup`/`Stop`, the SIGKILL escalation resent until the reap (`killUntilReaped`, §Stop), once-only reap through `collectExit` (claim first, own wait, `Reclaim` on ECHILD — it takes a `waiter`, the process's own wait and nothing else; `releaser` adds the handle release an aborted spawn needs), exit translation (`exitValue`), stdio-copier join |
| `stdio.go` | `unix \|\| windows` | `buildStdio`: wires `Spec.Stdio` (inherit/null/capture) to `ProcAttr.Files`; capture pipes + copier goroutines joined by `Wait` (100% delivery, no leak) within `Spec.DrainGrace` after the exit (`drainBounded`, #1088) |
| `trampoline_unix.go` | `unix` | the re-exec trampoline (`installTrampoline`, `childHandshakeFD`) — see below |
| `handshake_unix.go` | `unix` | the parent side of the trampoline's status pipe and `handshakeError` (status byte → sentinel); an `S` report is a sandbox step's (`sandboxFailure`) |
| `sandbox_linux.go` | `linux && (amd64 \|\| arm64)` | `Spec.Sandbox` (#728), the parent's side: `checkSandbox` before any PATH search or fork (`INVALID_SPEC`, `SANDBOX_UNAVAILABLE` naming the `feature`), `LandlockABI`, the Landlock plan trimmed to the ABI (`BestEffort`), `trampolineSelf` (`/proc/self/exe`, no fallback), `attachSandbox` (the clone's namespaces and maps, the credential taken off the clone, the sealed payload after the handshake pipe); `sandboxHost` is the kernel it reads, a test's simulated one through `sandboxHostOverride` |
| `sandbox_payload_linux.go` | same | the payload: a magic, a version, a length that must be the memfd's size, bounded counts and strings, no byte left over; `sealedPayload` (memfd + `F_ADD_SEALS` write/grow/shrink/seal), `readSealedPayload` (every seal or refused) |
| `sandbox_seccomp_linux.go` | same | `checkSeccomp`, `assembleSeccomp`: classic BPF by hand — the arch check (x32 killed on amd64), the floor (`execve`, `exit`, `exit_group`, `rt_sigreturn`, `write` on the handshake descriptor), the named calls, the default |
| `sandbox_stage_linux.go` | same | the second stage, in the child before `main` on its locked main thread: the steps in the kernel's order, raw calls, the filter in the call before `execve` |
| `sandbox_steps_unix.go` | `unix` | `trampolineSandboxEnv`, the step numbers and names, the failure report's layout and `sandboxFailure` |
| `sandbox_unsupported_unix.go` / `sandbox_off.go` | `unix` off Linux amd64/arm64 / everything off Linux amd64/arm64 | `checkSandbox` = `UNSUPPORTED_PLATFORM` before any fork; `LandlockABI` = `UNSUPPORTED_PLATFORM` |
| `sysnum_linux_amd64.go` / `sysnum_linux_arm64.go` | per GOARCH | the numbers the stdlib lacks (`seccomp`, `memfd_create`, `bpf`, `execveat`, `mount_setattr`, `landlock_*`), the audit architecture, and the seccomp name table (x/sys v0.49.0's, copied as data); pinned by `Test_sandboxSyscallTable` |
| `creds_unix.go` | `unix` | `Spec.User/Group/Groups` → `syscall.Credential` via `os/user` |
| `attrs_unix.go` | `unix` | best-effort `Nice` (setpriority) + `OOMScoreAdj` (procfs); ESRCH detection |
| `zombie_darwin.go` / `zombie_other.go` | `darwin` / `unix && !darwin` | `leaderIsZombie`: on darwin, `getpgid(2)` answers ESRCH for an exited, unreaped child (see §Stop); `false` on every other kernel, which never needs it |
| `limits_unix.go` | `unix` | `checkLimits`, before the spawn: `UnknownResource` for a resource with no stdlib `RLIMIT_*` mapping; every mapped one and a `Umask` pass, applied by the trampoline — a limit the kernel then refuses is `RlimitFailed`, through the handshake; an `OOMScoreAdj` off Linux is `UnsupportedPlatform` (#941) |
| (moved) `maxrss*.go` | — | the peak-RSS conversion is `childwait.MaxRSSKB` since #1136, so the reaper reports an orphan's `MaxRSS` in the same KiB; `readRusage` calls it |
| `oomadj_linux.go` / `oomadj_other.go` | `linux` / `unix && !linux` | `oomScoreAdjNative`: whether `/proc/<pid>/oom_score_adj` exists to take the bias (#941, ADR 0018 §Amendment 2026-11-04) |
| `cgroup_placement_linux.go` | `linux` | `validateCgroupPath` (pre-spawn: missing/not-a-cgroup ⇒ `CgroupUnavailable`) + `applyCgroupPlacement` (trampoline writes pid → `cgroup.procs`) |
| `cgroup_clone_linux.go` / `cgroup_clone_other.go` | `linux` / `unix && !linux` | #929: `cgroupByClone` (a `CgroupPath`, a kernel ≥ 5.7 by `uname` — probed once — and no ENOSYS seen since), `attachCgroupFD` (`SysProcAttr.UseCgroupFD`/`CgroupFD` on the validated directory, closed after the fork), `cloneSpawnError` (ENOSYS: remembered, the spawn retried through the trampoline; EACCES/EPERM/EBUSY/EOPNOTSUPP: `CGROUP_WRITE_FAILED`) |
| `cgroup_placement_other.go` | `unix && !linux` | `validateCgroupPath` rejects a non-empty path with `UnsupportedPlatform`; no cgroup v2 off Linux |
| `limittable_unix.go` | `unix` | `Resource` → `RLIMIT_*` table (stdlib constants only) |
| `limittable_as.go` / `limittable_openbsd.go` | `unix && !openbsd` / `openbsd` | `addPlatformLimits`: `RLIMIT_AS` where the stdlib exports it; nothing on OpenBSD, whose kernel has none, so `ResourceAS` is `UnknownResource` there |
| `diewithparent_pdeathsig.go` / `diewithparent_other_unix.go` | `linux \|\| freebsd` / `unix && !linux && !freebsd` | `Spec.DieWithParent` (#931): `SysProcAttr.Pdeathsig` where the kernel has a parent-death signal; `UnsupportedPlatform` before the spawn everywhere else on Unix. Windows binds it through the Job Object (`joblimits_windows.go`, `KILL_ON_JOB_CLOSE`) |
| `seal_unix.go`, `seal_closerange.go` / `seal_walk.go`, `closerange_linux.go` / `closerange_freebsd.go`, `fddir_proc.go` / `fddir_dev.go` | `unix` (split below) | the fd-table seal (#1089): `sealInherited` marks every descriptor ≥ 3 of the supervisor close-on-exec before the fork unless `Spec.FDs` is `FDsInherited`; one `close_range(3, ~0U, CLOSE_RANGE_CLOEXEC)` on Linux (436, mips excluded) and FreeBSD (575), the walk of `fdDir` on ENOSYS/EINVAL (remembered) and on every other Unix |
| `procfile_unix.go` | `unix` | `os.WriteFile` shim for `oom_score_adj` |
| (moved) `usage_*_unix.go` | — | the rusage counters are `childwait.UsageOf` since the lot-4 integration, so the reaper's `ExitEvent` carries an orphan's `Usage` as `Wait` carries an owned child's; `exitValue` and `exitValueFrom` call it |
| `wrap.go` | all | `wrap{Spawn,Wait,Signal,Stop,Rlimit,CgroupUnavailable,StdioCapture,UnknownUser,UnknownGroup}` — restate each sentinel's exact fields once |

## Spawn semantics

- **Environment.** `Spec.Env == nil` spawns with an **empty** environment, never
  the supervisor's — the port's known-state guarantee; pass `os.Environ()` to
  inherit deliberately. A variable `Spec.Env` sets more than once reaches the
  child **once, with its last value, at the place of its last occurrence**
  (`dedupEnv`, in `buildProcAttr` and `spawnEnv`, the same on the direct path
  and through the trampoline, whose sentinel still comes after): that is what
  `os/exec` makes of `append(os.Environ(), "K=v")`, its documented way to
  replace a variable, and what `searchPathOf`, `EnvAllow` and `EnvDeny` read
  already, while a child's `getenv` reads the first entry of a name. Handed on
  as written, a `PATH` searched was not the `PATH` the child read, and a
  replaced `LD_PRELOAD`, `PATH` or `NOTIFY_SOCKET` was not replaced (ADR 0217,
  amendment of 2026-11-08). One map, linear; names compare as the platform
  compares them (`variableKey`); an entry without `=` is kept as written.
  `env_dedup_unix_external_test.go` spawns `/usr/bin/env` on both paths,
  `Test_buildProcAttr` and `Test_spawnEnv` hold the block itself.
- **The spawn protocol's variables are refused (#726).** A `Spec.Env` key that
  begins `__KITSUNIUM_SDK_PROC_` — compared without regard to case, so the
  verdict is the same on every platform — is `ReservedEnv` from
  `validateSpec`, before any fork, on Unix and Windows alike. The trampoline
  appends its own (`reservedEnvKeys`: payload, cgroup placement, handshake
  descriptor, the `PIDEnv` names) AFTER `Spec.Env`, and a child's `getenv` reads the FIRST of two
  equal keys: a caller's key used to replace the SDK's — a caller's
  `__KITSUNIUM_SDK_PROC_CGROUP` made the trampoline write its pid into any
  path it named. `reservedEnvKeys` is the one list; the trampoline strips it
  before the target runs, and `TestReservedEnvKeysListed` parses the
  package's sources (the go_test's `data`) and fails when a constant with the
  prefix is neither listed nor given a refusal case.
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
- **Credentials.** User/Group/Groups accept names or numeric ids; a name that
  does not resolve is `UnknownUser`/`UnknownGroup`. A `syscall.Credential` has
  no unset id, so `applyUserGroup` writes the parent's uid and gid first and
  replaces only what the Spec names: an empty User is the parent's uid, an
  empty Group the user's primary gid — or the parent's gid when no User is set
  (#450; a Group-only or Groups-only Spec used to ask the kernel for uid 0). A
  numeric uid with no passwd entry is honoured only beside an explicit Group:
  alone it is `UnknownUser` (`why` "a numeric uid with no passwd entry needs an
  explicit Group"), never the gid 0 it used to resolve to, which left the child
  in root's group after the uid drop. Supplementary groups are still set (an
  empty `Groups` clears them), so a non-root supervisor asking for any
  credential change meets `setgroups(2)`'s `EPERM` — the kernel's refusal,
  not an identity it did not ask for.

## Stop: group-aware SIGTERM → SIGKILL escalation

`Stop(ctx, grace, sig)` sends `sig` to the whole group (`kill(-pgid, sig)`),
waits up to `grace` for exit, then escalates to `SIGKILL` on the group — at
once when `grace` is not positive, as on Windows (it used to wait on `sig`
alone, without a bound, for a child that ignored it). It
returns `nil` once the group is gone, `ctx.Err()` if cancelled first, or
`StopFailed` if the group survives the SIGKILL escalation for a non-ESRCH reason.
A group that vanished (`ESRCH`) at any phase is treated as success. The reap runs
under `sync.Once`, so concurrent `Stop`/`Wait` callers share one wait4 and one
`close(done)`.

**The SIGKILL is resent until the reap.** NetBSD up to 10.1 discards a signal
posted to a process still inside `execve` (PR kern/58091, fixed in 10.2 and
11.0), and `Start` returns from inside the child's `execve` (§Tests), so the
escalation's SIGKILL — or the `teardown` of a child whose `Nice` or
`OOMScoreAdj` was refused, which kills a child `Start` has just spawned — could
be lost, and the wait for the reap after it never ended. `killUntilReaped`, the
one escalation both use, sends SIGKILL again every `killResendInterval`
(100 ms, on the handle's clock) until the leader is reaped or `ctx` ends the
wait; `teardown` runs it uncancelled, the resend bounding it by the child's
death. A SIGKILL that lands kills within the first interval, so on every other
kernel the reap comes first and nothing is resent: the cost elsewhere is one
timer per escalation. A resend is never sent once the reap is in hand — the
first SIGKILL still is, since a private group can outlive its leader. A
delivery fault other than "gone" ends the escalation with `StopFailed`, and
`teardown` then leaves the child unreaped rather than wait on a child it could
not signal. `Test_handle_killResentWhenLost` drops the first SIGKILL through
the handle's `kill` double, on every OS, and pins that `Stop` with no grace,
the escalation and `teardown` each end on the resend — not a nanosecond before
the interval closes on a `ManualClock`; an escalation that sends SIGKILL once
fails all three on the test's 30-second guard.

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
stays `SIGNAL_FAILED`. `Test_handle_StopOnALeaderThatDiedUnreaped` and
`Test_handle_zombieGroupRefused` pin both sides, with an oracle independent of
the probe (darwin: the group's own EPERM; Linux: `/proc`'s state letter).

**And after the leader is reaped.** XNU's `killpg1` counts only the live
members it may signal and answers EPERM when there is none, so a group whose
leader was collected, and whose forked members died with it, answers EPERM
until init reaps those orphans — they are zombies, and their parent is gone.
`Stop` on a handle already waited, or an escalation right after the
background reap took the leader, reported `SIGNAL_FAILED` for that dead group
whenever the child had forked (macos-arm64, `Test_handle_signalGroupAllowGone`
when its fixture forked a `sleep`). `zombieGroupRefused` now reads that EPERM
too, through `groupDrains`: `kill(-pgid, 0)` every `drainPoll` (10 ms, on the
handle's clock) up to `drainPolls` (100) times — ESRCH once init has reaped
them is gone, a member that answers is not, and an EPERM that lasts the
second is a live member this process may not signal, whose refusal stands.
The rule lives in `SignalGroup` itself, which reports such a group ESRCH —
the answer Linux and the BSDs give once a group is reaped — so `Stop`'s
`processGone` and any caller read it as gone. `zombiesRefused` is false on
every other kernel, where neither the probe nor the read before the kill is
made. `Test_handle_reapedLeadersOrphans` runs `SignalGroup`, the escalation
and `Stop` on such a group on every OS, `Test_handle_zombieGroupRefused` the
rule (a group that emptied is gone on darwin; one with a live orphan is not),
and `Test_handle_groupDrains` the watch through a `kill(2)` double. A test
that puts a handle on a `ManualClock` is put back on `clock.System` by
`startChild`'s cleanup, so its SIGKILL never waits on a clock nobody advances.

The grace window is a timer on the handle's clock — `clock.System` from
`newHandle`, on Unix and on Windows alike — so `Test_handle_graceOnInjectedClock`
closes an hour of grace by advancing a `ManualClock` and pins that neither
`awaitExit` nor `Stop` returns a nanosecond earlier.

## A child on a pseudo-terminal (#934)

`Stdio: StdioPTY` gives the child one pseudo-terminal: the slave becomes fd 0,
1 and 2 and, through `SysProcAttr{Setsid, Setctty, Ctty: 0}`, its controlling
terminal in a new session (so `Setpgid` is dropped — `setpgid` fails for a
session leader — and the handle treats the child as a group leader: it is
one). `Spec.PTY` sizes it before the child's first instruction. The parent
closes its slave after the spawn; the master is `Terminal.TTY()`, **owned by
the caller**, who reads the child's output from it, writes its input to it and
closes it. `Resize` is `TIOCSWINSZ` (the kernel sends the foreground group
SIGWINCH), `Size` `TIOCGWINSZ`; a child without a terminal answers
`PTY_FAILED` (0.2.6.26). Every ioctl goes through `SyscallConn().Control`:
`File.Fd()` would set the master blocking, and a `Read` on it could then no
longer be interrupted by `Close`. FreeBSD's `posix_openpt` descriptor is made
non-blocking before `os.NewFile` for the same reason.

Supported on Linux, darwin, FreeBSD and OpenBSD. OpenBSD's ptm(4) opens both
sides in one `PTMGET` ioctl (`pty_openbsd.go`, its ABI hand-written and pinned
by `Test_ptmget_layout`); since 7.5 pinsyscalls refuses an ioctl issued outside
libc, and `syscall.Syscall(SYS_IOCTL, …)` is the one call the standard library
reroutes to libc's `ioctl` there (Go issue 63900) — so `ioctlFile` is the path,
never `RawSyscall`, which answers ENOSYS. The kernel opens the two descriptors
without close-on-exec: they are marked under `syscall.ForkLock` before a
concurrent spawn can inherit them, and the master made non-blocking. NetBSD and
DragonFly are unmeasured; illumos and Solaris unlock a terminal with libc's
STREAMS pushes; Windows needs a ConPTY attribute `os.StartProcess` cannot
carry — a later direct `CreateProcessW` backend. All refuse `StdioPTY` with
`UNSUPPORTED_PLATFORM` before spawning (`Test_Start_ptyRefused`).
`Test_Start_onAPseudoTerminal`: `stty size` prints the Spec's 30 100, `test -t
0` holds, input written to the master is read by the child;
`Test_Terminal_Resize`: the child's WINCH trap printed `50 120` 47 ms after
`Resize` on this VM.

## ProcessHandle and Open: signals that cannot reach a reused pid (#928)

A pid is a number the kernel hands to the next process once the last one is
reaped; `kill(pid)` after that reaches whoever holds it. The runtime already
keeps a pidfd per child on Linux ≥ 5.4 (and a HANDLE on Windows) and signals
through it, but nothing exposed it. The `ProcessHandle` sibling port (ADR 0039)
does:

- `Handle()` — a duplicate the caller owns: pollable (readable once the
  process exits, before any reap: `Test_Handle_pollableExit` measured ≈ 0.3 ms
  from the child's last instruction), passable to another process. False once
  the child is reaped, on a kernel that gave the runtime none, and off Linux
  and Windows.
- `SignalHandle(sig)` — `pidfd_send_signal` through the runtime's pidfd. After
  the reap it answers `SIGNAL_FAILED` wrapping ESRCH without a syscall;
  `Test_SignalHandle_reapedChildren` reaps 2 000 children and signals each
  through its handle and through a pidfd duplicated before the reap: ESRCH
  every time. A kernel without pidfd is `HANDLE_UNAVAILABLE` (0.2.6.25); other
  platforms `UNSUPPORTED_PLATFORM`.
- `Open(pid)` — the primitive #729's adoption builds on, for a process this
  one did not spawn: Linux holds a pidfd, Windows a HANDLE with terminate and
  synchronize rights (closed by a cleanup when the handle is collected).
  `Signal`, `SignalGroup` (the process alone: a group by number is not
  race-free) and `Stop` (signal, watch the handle for the exit within the
  grace, SIGKILL, watch until ctx ends) act through it; `Wait`/`WaitContext`
  are `WAIT_FAILED` wrapping ECHILD — no exit status is promised for a
  non-child. A pid below 1 is `INVALID_SPEC`. `Test_Open` runs it on a child
  of the test (the only process a test may signal).

FreeBSD's `pdfork` is not reachable through `ForkExec`, so the BSDs, darwin,
illumos, Solaris and Linux on mips answer `Handle() = false` and
`UNSUPPORTED_PLATFORM` (`Test_ProcessHandle_refused`).

## Attach and AttachPIDFile: adopting a process from a PID file (#729)

`Open` holds a process and refuses `Wait`; a supervisor adopting a daemon
(`Type=forking`, `MAINPID=`, a hand-over — S0c, S0h) needs the opposite: the
END observed, without a status it cannot read.

- **The handle (`adopted`).** A pidfd opened with `PIDFD_NONBLOCK` (Linux
  5.10) — before 5.10 the kernel refuses the flag with EINVAL and the pidfd is
  reopened plain and made non-blocking by `fcntl` (`openPollablePidfd`,
  `TestOpenPollablePidfdFallback`) — then `os.NewFile`, which registers it
  with the runtime's poller. The first `Wait`, `WaitContext` or `Stop` starts
  ONE goroutine parked in `RawConn.Read` until the pidfd turns readable — the
  process has exited, reaped or not — and closes `done`; a pidfd the poller
  refused is watched in `ppoll` slices instead. `Wait` then returns
  `STATUS_UNKNOWN` (0.2.6.34) with `ExitValue{Code: -1}`, never a zero that
  reads as success. Nothing calls `wait4`: a child of this process attached
  here keeps its status for its spawner or the reaper (`childwait` is not
  touched — `TestAttachObservesTheEnd` reads the real SIGKILL from the
  spawner's handle after the adopted `Wait` returned). Signals go through the
  pidfd only; once the process is reaped they answer ESRCH, never a stranger.
- **The bounds.** `checkScope` reads `/proc/<pid>/cgroup` (the `0::` line:
  at or below `AttachValue.Cgroup`) and `/proc/<pid>/status` (real AND
  effective uid among `AttachValue.UIDs`) while the pidfd pins the process,
  then proves the reads concerned it: a pidfd still unreadable afterwards
  means the process never ended in between, so its pid was not yet another's.
  A process that ended meanwhile is `SIGNAL_FAILED` (ESRCH), like a pid gone.
- **The PID file (`pidWalk`).** An absolute clean path, walked from `/` one
  component at a time, each directory held `O_PATH` (search permission only)
  and examined through `/proc/self/fd/<dir>/<name>`, a descent proven to be
  the entry examined (dev and inode). `.` stays, `..` climbs, a symbolic link
  on the way is followed (40 at most) — an absolute target restarts at the
  root. The last component is held `O_PATH|O_NOFOLLOW` and examined before any
  read: a link is `PID_FILE_REFUSED` ("a symbolic link"), a directory, FIFO,
  socket or device "not a regular file" — none is ever opened for reading —,
  then a regular file is reopened through its position and read: one decimal
  PID, blanks around it, ≤ 64 bytes, no sign (`parsePID`).
- **Trust.** The file is trustworthy when every entry the walk went through —
  directories, links, the file — belongs to root or this process's euid, every
  directory is closed to group and others unless sticky, and the file is
  writable by neither (`entryTrusted`, `TestPIDWalkTrust`, whose walk starts at
  the test's own directory so the verdicts hold whatever the machine's
  ancestors). A trustworthy file's PID is adopted (systemd's root-owned PID
  file); any other needs a bound, and an `AttachValue` that bounds nothing is
  `ADOPTION_REFUSED` (0.2.6.33). A bound given is checked whatever the file.
  PID 1 and this process are `ADOPTION_REFUSED` always.
- **On darwin and the BSDs** (`attach_bsd.go`, ADR 0219 amended 2026-11-08)
  the handle is two kqueue `EVFILT_PROC` knotes (`NOTE_EXIT`), each on its own
  close-on-exec kqueue: the kernel attaches a knote to the process that had the
  pid at registration and to no later one. The watcher blocks in `kevent(2)` on
  the first — a kqueue is not a descriptor the poller takes, so a waited handle
  holds one thread until the end (EINTR, the runtime's own signals, resumes the
  wait); the second is only polled, under `checking`, before each signal, so no
  wait consumes the event a signal must be refused on. No kernel handle signals
  a process these kernels did not let this one fork: `Signal` and `SignalGroup`
  go by `kill(2)` once that poll is silent, and answer ESRCH, sending nothing,
  once it fired (`TestDeliverRefusesAReusedPID` points the handle at a second
  child of the test, standing in for a process that took the pid: refused, the
  stranger alive). The window between the poll and `kill(2)` is the residual
  one. `SignalHandle` is `UNSUPPORTED_PLATFORM`, `Handle` reports none.
- **Their bounds.** A `Cgroup` bound is `ADOPTION_REFUSED` (no cgroup). A
  users bound reads the kernel's process record (`readRecord`: `__sysctl`
  through `syscall.Syscall6`, OpenBSD's libc stub included, decoded by
  `proc/internal/bsdabi`) before and after the knotes' registration: one start
  time or ESRCH ("the pid changed hands"), then the knote's silence proves the
  read concerned the process held. The start time is NOT compared before a
  signal: FreeBSD and OpenBSD add the boot time on every read, a stepped clock
  would make a live process read as another, and `Stop` would take that ESRCH
  for "already gone".
- **A child still inside `execve(2)`.** `os.StartProcess` returns once execve
  has closed the child's close-on-exec descriptors, before execve is done, and
  NetBSD answers the record of a process still inside it with no start time
  (`fill_kproc2` reads it as a zombie while execve holds `p_reflock`). On
  NetBSD 10.1 a child signalled in that window was not ended: its knote never
  fired in 10 s, and a `SIGKILL` left a `sleep 30` its whole 30 s (#1379). The
  BSD tests that signal a child start it with `startRunningSleeper` — one
  execve, `/bin/sleep`, returned once its record carries a start. The kernel
  line that drops the signal is not identified.
- **Their PID file walk** is the same `pidWalk` over `os.Root` directories
  (`pidfile_bsd.go`): `openat` `O_NOFOLLOW`, `fstatat`, `readlinkat`, a
  descent proven by device and inode; the file examined by `fstatat`, opened
  `O_NONBLOCK`, proven the inode examined. No `O_PATH` there: each directory
  needs read permission, not search permission only.
- **Never run** on the authoring host (Linux): the `attach_bsd_*_test.go`
  suites run on the e2e-cross lanes (macOS, FreeBSD, OpenBSD, NetBSD);
  DragonFly is compiled only. `TestPIDWalkTrust` and the other walk tests
  (`pidfile_internal_test.go`) are shared: they ran here over the Linux holder.
- **illumos, Solaris and Windows** stay `UNSUPPORTED_PLATFORM` before any read
  (`TestAttachUnsupported`).

## WaitContext, Run and the environment filters (#935)

- **`WaitContext(ctx)`** — the `ContextWaiter` sibling port (ADR 0039) on both
  handles. It races `settled` — closed by `Wait`'s Once once the exit AND the
  drained capture are in hand, so it never returns half an outcome — against
  `ctx`, driving the reap through `reapInBackground`. A ctx that ends first
  returns its error and leaves the child running; a later `Wait` or
  `WaitContext` returns the memoised exit (`TestWaitContext`).
- **`Run(ctx, spec, RunValue)`** — a bomb-safe one-shot: stdout and stderr go
  to `headBuffer`s that keep the first `MaxOutputBytes` bytes each and report
  every later write as accepted, so the copier keeps draining and the child
  never blocks on a full pipe. `Stdin` is kept. `MaxOutputBytes <= 0` is
  `INVALID_SPEC` before any spawn (ADR 0031). The buffer grows by doubling
  capped at the ceiling: `append`'s own growth (a quarter at a time past 256
  bytes) allocated 5.5 MiB for a 1 MiB head. `TestRunBoundsTheOutput`: a child
  writing 64 MiB into a 1 MiB ceiling exits 0, `Truncated`, exactly 1 MiB kept,
  under 4 MiB allocated. On ctx's end: `Stop(SIGTERM, Grace)` on a context
  that cannot be cancelled (the SIGKILL resend bounds it), then the exit; the
  error is ctx's, joined with a failed stop's (`TestRunCancelledStopsTheChild`).
- **`EnvAllow` / `EnvDeny`** — the filter every consumer rewrote, with no
  policy on the SDK's reserved keys (`#726` owns those).

## Wait's copier join is bounded by the drain grace (#1088)

The capture copiers end at EOF, when every holder of the pipe's write end is
gone — and the leader is not always the only holder: a daemonising service,
or `sh -c 'sleep 60 & exit 0'`, leaves a process with fd 1 or 2. `Wait` used
to join the copiers without a bound inside its `sync.Once`, so it never
returned while that process lived, every later `Wait` blocked on the same
Once, and `Stop` returned nil while a copier, `reapInBackground` and the read
end were held (base: "Wait still blocked 1.5s after the leader's exit").

`drainBounded` now races the join against `Spec.DrainGrace` on the stdio
state's `drainClk` (`clock.System`; a `ManualClock` in a test — the handle's
own `clk` is left alone because a test cleanup writes it while a background
reap may be inside `Wait`). The zero grace is `defaultDrainGrace`, one second
(ADR 0031: a zero that waits forever would be inert); a negative grace keeps
the unbounded join for a caller who wants every byte of a daemon's orphans.
When the grace closes first, `cutDrain` closes the parent's read ends and the
stdin write end — a pipe's Read returns `os.ErrClosed` at once (Unix pipes are
pollable; Windows cancels a pipe's pending read on close) — the copiers
return, and `Wait` reports the real `ExitValue` with `STDIO_DRAIN_TIMEOUT`
(0.2.6.24), fields `held` (the streams still open: `stdout`, `stderr`,
`stdin` when the feeder had not finished) and `bytes` (delivered). A copier
that read the closed end does not latch a capture failure. A copier that
finishes before the timer changes nothing: 100 % delivery stays the contract
when the leader was the only holder. The stdin feeder stays fire-and-forget:
one blocked inside the caller's reader ends only at that reader's next return.
`TestWait_drainGraceBoundsTheCopierJoin` pins the three rows (held, let go
first, negative grace) and that no goroutine outlives `Wait`;
`TestWindowsDrainGraceBoundsWait` runs `cmd /c start /b` on Windows.

## The child's descriptor table is sealed (#1089)

Go's fork/exec installs the child's stdio, `ExtraFiles` and the handshake
pipe with `dup2` and, by the stdlib's stated convention, leaves every other
descriptor of the supervisor that lacks `FD_CLOEXEC` open in the child: a
systemd activation socket before `listen.Files` flags it, a descriptor the
shell that started the supervisor leaked, an `os.NewFile` over an inherited
fd, a raw `syscall.Dup`. A child that dropped privileges through `Spec.User`
then held a root-opened socket (CWE-403). `Start` now seals the supervisor's
own table before every fork (`sealInherited`, in `spawn`, after the child's
files are assembled): every descriptor from 3 up is marked close-on-exec —
**a flag, nothing closed**, so the supervisor keeps using them, and the
child's copies of `ExtraFiles` keep theirs because `dup2` clears the flag.

| Platform | Mechanism |
|---|---|
| Linux ≥ 5.11 (not mips) | one `close_range(3, ~0U, CLOSE_RANGE_CLOEXEC)` (syscall 436, cited) |
| Linux < 5.11, mips | the walk of `/proc/self/fd` (ENOSYS before 5.9, EINVAL on 5.9/5.10 which lack the flag; the answer is remembered in `closeRangeAbsent`) |
| FreeBSD | `close_range` (575), else the walk of `/dev/fd` — which lists 0–2 alone without `fdescfs` |
| darwin, NetBSD, DragonFly | the walk of `/dev/fd` |
| OpenBSD | the walk of `/dev/fd`, whose static nodes stop at 63 |
| illumos, Solaris | the walk of `/proc/self/fd` |
| Windows | nothing to do: a child gets only the handles it is passed |

`syscall.CloseOnExec` marks each walked descriptor (libc-routed, the
OpenBSD-safe call). A walk that cannot read its directory, or a `close_range`
refused for another reason, is `SPAWN_FAILED` with field `fd`, and nothing is
spawned. The mark is process-wide and one-way: `Spec.FDs = FDsInherited` skips
the seal for its own spawn, and inherits only descriptors opened without
close-on-exec since the last sealed spawn. `TestStart_sealsTheFDTable` holds
a `syscall.Dup` of a pipe end and spawns a probe — the test binary — that
`fstat`s it: present on the base (`base-1089`: "child holds fd 9 = true"),
absent now, present again with `FDsInherited`, `ExtraFiles` still read at fd 3
and 4 with and without the trampoline, and the parent still writes through
it after the spawn. `Test_sealWith` runs the close_range path and the walk
behind a double answering ENOSYS, EINVAL and EPERM.

## DieWithParent: a child that does not outlive its supervisor (#931)

A supervisor killed by SIGKILL, the OOM killer or a crash runs no `Stop`, and
its children used to run on, detached and unsupervised. `Spec.DieWithParent`
names the signal a child gets when that happens:

- **Linux and FreeBSD** set `SysProcAttr.Pdeathsig` (`PR_SET_PDEATHSIG`,
  `procctl(PROC_PDEATHSIG_CTL)`). The stdlib sets it after the credential
  change and, on Linux, signals the child at once when the parent is already
  gone. It survives the trampoline's `execve` (only a set-uid image clears it).
  **Caveat:** the kernel fires it when the forking THREAD exits, not the
  process. The Go runtime keeps its threads, but a goroutine that ends while
  locked to its thread takes the thread with it — a spawn from such a
  goroutine binds the child to that goroutine's life.
- **Windows** creates the Job Object even without `Rlimits`, with
  `JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE`, assigns the suspended child through the
  existing `confine` path, and keeps the job handle open after `Wait`
  (`releaseAfterExit`): the kernel closes it when the supervisor exits, and
  kills what the job still holds. The cost is one handle per such child for
  the supervisor's life.
- **darwin, OpenBSD, NetBSD, DragonFly, illumos, Solaris** have no mechanism
  the stdlib reaches: a non-zero value is `UnsupportedPlatform` before
  anything is spawned (`checkDieWithParent`), never dropped.

`TestStart_dieWithParent` runs a helper — the test binary — that spawns
`/bin/sleep 5` with `DieWithParent = SIGTERM` and a pipe at fd 3; the test
SIGKILLs the helper, its own child, and reads EOF from the pipe within a
second. On the base the read timed out: the sleep outlived its parent.
`TestWindowsDieWithParent` is its Windows twin (the grandchild is observed,
never signalled).

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
(`ru.Maxrss`, normalised to KiB: darwin's bytes divided by 1024 rounding up,
Linux's and the BSDs' kilobytes passed through, 0 on illumos and Solaris).
`TestWait_maxRSSIsKiB` holds the unit on a real child holding 64 MiB: a byte
figure lands far outside its KiB bounds.

The rest of the rusage and the status word are carried too (#1090):
`WaitStatus.CoreDump()` → `CoreDumped` (a SIGSEGV with a core to collect,
told apart from the OOM killer's or `Stop`'s SIGKILL), and `Minflt`, `Majflt`,
`Inblock`, `Oublock`, `Nvcsw`, `Nivcsw` → `Usage` (`childwait.UsageOf`,
widened from the int32 that 386, arm and mips declare by one generic
`counter`), a
`UsageValue` with `Known` set on Linux, darwin and the BSDs, and the zero
value — `Known` false — on illumos and Solaris, which leave the counters
unfilled, and on Windows. It was a `*UsageValue` until #1136: a reaper sweep
converting an orphan's status for `WithOnExit` then allocated once per child,
and a pointer an observer kept would have to be shared or copied; by value it
costs nothing and nothing aliases it (ADR 0217, amendment 2026-11-11). Both paths —
the handle's own wait and a sweep's `StatusValue` — go through `exitValue`,
so `TestWait_coreDumpedAndUsageAgreeOnBothPaths` runs the same SIGSEGV child
through each, with `RLIMIT_CORE` at the hard limit and at zero. The reaper
reads an orphan's status through `childwait.StatusValue.Exit` instead, and
`Test_exitValue_isTheSweepsExit` holds the two readings equal, the core bit and
the counters included. A fault count
is bounded by the largest page one fault maps: with THP set to `always`,
64 MiB of fresh anonymous memory costs a few thousand faults, not 16 384
(`TestWait_minorFaultsOfAChildHolding64MiB` reads the setting).

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
  (`ResourceNProc`, `ResourceMemLock` — absent from `syscall`, and
  `golang.org/x/sys` is banned) is rejected with `UnknownResource` **before** the
  spawn (`checkLimits`). `proc/rlimit` spells both by their Linux generic-ABI
  numbers (`rlimit_linux.go`), so on Linux `rlimit.Apply` honours a limit this
  package refuses — an open disagreement, which moving one `Resource` →
  `RLIMIT_*` table into `proc/internal/rlim` for both would settle.
- A limit the kernel **refuses** (e.g. an invalid soft>hard pair, or raising a
  hard cap unprivileged), or a failed `execve` of the target, is reported through
  a **handshake pipe** (`handshake_unix.go`): the trampoline inherits the pipe
  write end and writes a status byte (`'A'` apply / `'C'` cgroup / `'E'` exec) on
  failure, then arms it close-on-exec so a clean `execve` closes it (the parent
  reads EOF = success). The pipe is appended **after** any `Spec.ExtraFiles`, so
  the extras keep their contracted fd 3.. (socket activation) and the handshake
  lands at fd `3+len(ExtraFiles)`; the parent passes that descriptor to the
  trampoline via `__KITSUNIUM_SDK_PROC_HSFD` (`childHandshakeFD`, default 3 when
  there are no extras). **`Spec.PIDEnv` (#739)** rides in
  `__KITSUNIUM_SDK_PROC_PIDENV` (comma-separated; `checkPIDEnv` refuses an
  empty name, `=`, `,`, NUL — `INVALID_SPEC` — and the protocol's prefix —
  `RESERVED_ENV`): the trampoline sets each name to `os.Getpid()`, which
  `execve` keeps for the target, replacing the same name from `Env`
  (`withPIDEnv`, `TestStartPIDEnv`) — `LISTEN_PID` for `systemd/listen`, which
  no parent knows before the fork. Windows refuses `PIDEnv`. `Start` blocks on that read and surfaces a typed
  `RlimitFailed` (apply) / `CgroupWriteFailed` (cgroup) / `SpawnFailed` (exec) —
  matching the direct-spawn contract instead of a bare 126/127 child exit. This is the same self-pipe + cloexec trick `os/exec`
  uses for its own errpipe. A failure to locate `os.Executable()` likewise
  surfaces `RlimitFailed` before any spawn.
- `Nice`, `OOMScoreAdj` and `Scheduling` are honoured post-start on the live
  pid (`setpriority(2)` / `/proc/<pid>/oom_score_adj` / `sched_setscheduler(2)`
  and `ioprio_set(2)` on Linux, `rtprio(2)` on FreeBSD). A host refusal
  surfaces `RlimitFailed` and the half-configured child is killed + reaped
  (`teardown`). `Scheduling` binds the child's main thread, which every thread
  it creates afterwards inherits; a child that creates threads in the
  microseconds between its exec and the call keeps them on the old policy —
  the window `Nice` has. It is not applied in the trampoline: there the
  credential change has already happened, so a privileged supervisor could
  no longer grant a realtime policy to an unprivileged child. On Windows
  `Nice` is a priority class passed to CreateProcess (`priorityClass`: ≤ −10
  HIGH, < 0 ABOVE_NORMAL, 0 NORMAL, 1–10 BELOW_NORMAL, > 10 IDLE) and so binds
  from the first instruction; `Scheduling` with a policy or I/O class is
  `UnsupportedPlatform` there, as on darwin, OpenBSD, NetBSD, DragonFly,
  illumos and Solaris. FreeBSD has no batch policy and no per-process I/O
  class. `Test_Start_scheduling` reads `/proc/<pid>/stat` field 41 and
  `ioprio_get` back; the process's own OOM bias is `self.SetOOMScoreAdj`.
  `OOMScoreAdj` off Linux never gets that far: there is no procfs file to take
  it, so `checkLimits` refuses it with `UnsupportedPlatform` before the fork
  (#941) — the child used to run unbiased until the write failed.
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

**A cgroup placement alone no longer takes the trampoline (#929).** On
Linux ≥ 5.7 the stdlib clones the child straight into the group (clone3 with
`CLONE_INTO_CGROUP`, through `SysProcAttr.UseCgroupFD`): race-free, one
syscall, no re-exec of the supervisor's binary, no handshake pipe — the #118
hang class gone with it. `needsTrampoline` is now `Rlimits || Umask ||
(CgroupPath && !cgroupByClone)`; `validateCgroupPath` still runs first (the
statfs magic check). The probe reads `uname`'s release once; a kernel or a
seccomp filter that answers the clone with ENOSYS is remembered and the spawn
retried through the trampoline, whose `cgroup.procs` write is the fallback on
any kernel below 5.7. A group that refuses the member (not delegated, a domain
with controllers) is `CGROUP_WRITE_FAILED`, as the trampoline's `'C'` byte
was. When a trampoline runs anyway (Rlimits, Umask), it still writes the pid —
an idempotent no-op for a child the clone placed. `TestStart_clonesIntoTheCgroup`
— the child's `/proc/self/cgroup` read in a subtree the test creates beneath
its own group — runs wherever that group is delegated to the user: a
`systemd-run --user --scope` here (it passes on Linux 6.12), the linux-init
lane (ADR 0177) as root; it skips on a host that delegates nothing.
`Test_needsTrampoline_cgroupByClone`, `Test_releaseClonesIntoCgroup` and
`Test_cloneSpawnError` pin the routing on every kernel.
`BenchmarkStart_BinTrue_CgroupFD` prices the placement: 1.08x of
`Start_BinTrue_Inherit` against the trampoline's 5.7x (BENCH.md §3), on the same
delegated subtree, skipping without one.

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

## The sandbox: a trampoline in two stages (#728)

`Spec.Sandbox` is applied on Linux amd64 and arm64; every other platform
refuses it with `UNSUPPORTED_PLATFORM` before any fork (a witness test on each,
`TestStartRefusesSandboxOffLinux`, `TestWindowsRefusesSandbox`), because a
sandbox asked for and not applied must never run the program.

- **Before the fork** (`checkSandbox`, called by `prepareSpec` before the PATH
  search): the shape (`INVALID_SPEC`: `FDsInherited`, maps without a user
  namespace, `Groups` in one, a root or a mount without a mount namespace, a
  bare `Path` in a root, ambient outside bounding, an inert Landlock or seccomp
  value, an unknown syscall name, the floor denied), then the kernel
  (`SANDBOX_UNAVAILABLE`, `feature` naming it): `/proc/self/exe`, each
  namespace's `/proc/self/ns/<kind>`, `mount_setattr` for a read-only mount,
  `cap_last_cap`, the Landlock ABI (a right newer than it refused unless
  `BestEffort`), seccomp with `SECCOMP_RET_KILL_PROCESS`.
- **Stage one, the clone.** Every namespace but the mount and cgroup ones on
  `Cloneflags`; the mount namespace on `Unshareflags`, which makes the stdlib
  remount `/` `MS_REC|MS_PRIVATE`; the cgroup namespace left to stage two (a
  flag in the payload), because a `Spec.CgroupPath` may be placed by the
  trampoline's write rather than by the clone (a kernel before 5.7, clone3
  filtered): a namespace taken at the clone is then rooted at the
  supervisor's group, the child sees its group beneath it, and a group
  outside it refuses the write (`CGROUP_WRITE_FAILED`,
  `TestSandboxCgroupNamespaceRootedAtTheGroup`); a user namespace's maps (empty: the
  namespace's 0 to the supervisor's euid/egid, one wide) with `setgroups`
  denied. `SysProcAttr.Credential` is cleared: the identity is the stage's.
  The child execs `/proc/self/exe`.
- **Stage two** (`execSandboxed`, reached from `runTrampoline` when
  `__KITSUNIUM_SDK_PROC_SANDBOX` names the payload's descriptor): the payload is
  read from the sealed memfd, then on the main thread — locked by the runtime
  during package initialisation, and by the stage — the steps run in this
  order: `unshare(CLONE_NEWCGROUP)` when asked — the child is in its group by
  then, so the namespace is rooted at it on either path —, mounts (a tmpfs of
  mode 0755, or 1777 for `MountTmpfsSticky`, `PrivateTmp=`'s), `pivot_root` (+ `umount2(MNT_DETACH)`, `chdir(Dir)`), the
  Landlock ruleset and its `O_PATH` rules, `PR_CAPBSET_DROP` (only what is
  present), `PR_SET_SECUREBITS`, `setgroups`/`setresgid`/`setresuid` as raw
  calls on that thread (`PR_SET_KEEPCAPS` first when ambient caps are asked;
  the parent-death signal armed again after, and the parent checked unless in
  a PID namespace), `capset` (permitted and effective cut to the bounding set,
  inheritable = ambient) and the ambient set, `close_range(hsfd+1, ~0,
  CLOEXEC)` (walk of `/proc/self/fd` before 5.11), `no_new_privs` (forced by
  Landlock and seccomp), `landlock_restrict_self`, then `prctl(PR_SET_SECCOMP)`
  and `execve` in the next raw call, the argv and environment converted
  before the filter.
- **Fail closed.** A failed step writes `S`, its number and its errno on the
  handshake pipe and exits 126: `SANDBOX_FAILED` naming the step. An `execve`
  refused after the filter writes `E` through the floor's `write` and exits
  through `exit_group`: `SPAWN_FAILED`.
- **Tests.** `TestSandboxOrderOnOneThread` reads the stage's trace (a
  test-only `traceFD` in the payload: each step and its tid) and the program's
  own `/proc/self/status` (`NoNewPrivs`, `Seccomp`, `CapBnd`, per-thread state
  it would not hold had the execve run on another thread);
  `TestSandboxFailClosedAtEveryStep` injects a failure at each step
  (`failStep`) and checks the witness is never made; each restriction is proved
  by a failure observed in the child (Landlock, seccomp errno and kill, the
  bounding set, read-only and tmpfs mounts, the pivot, the namespaces, the fd
  sweep). Unprivileged user namespaces are needed for the namespace,
  capability and mount tests, which skip on a host refusing them.

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
`InvalidSpec`, `ReservedEnv` with no process run — `TestStartRefusesReservedEnv`
—, `UnknownResource`, a cgroup path that is no control group —
`CgroupUnavailable` on Linux, `UnsupportedPlatform` elsewhere), limits read
back from inside the child, trampoline failures arriving typed (`RlimitFailed`
/ `SpawnFailed`), `ExtraFiles` keeping fd 3 through the trampoline, a nil
`Spec.Env` leaking nothing, and the three stdio modes. The handle is pinned
white-box in `handle_unix_internal_test.go`: `Stop` escalates
`SIGTERM`→`SIGKILL` for a child that ignores `SIGTERM`, `SignalGroup` reaches a
grandchild, `Wait` is memoised, a signalled exit reports code −1, and a leader
that died unreaped stops cleanly on darwin too (§Stop). Every child those
tests spawn comes from `startChild`, which returns only once the shell has
written a marker — code that runs after its `execve` — and appends `:` so the
shell never execs the program's last command in place: `Start` returns from
inside the child's `execve` (the close-on-exec pipe closes there), and NetBSD
up to 10.1 discards a signal posted to a process still in `execve` (PR
kern/58091, fixed in 10.2 and 11.0), so a `SIGKILL` sent at once was lost
and `Test_handle_SignalGroup` / `Test_handle_graceOnInjectedClock` waited out
the child's 30 s on one NetBSD run in four. A caller that signals a child it
has just started on such a kernel can lose the signal the same way; the SDK's
own SIGKILLs — `Stop`'s escalation and `teardown` — are resent until the reap
(§Stop). The
shell therefore stays the leader, so a fixture without a private group runs
`shellOnlyWait` — a read from a FIFO the test made, nothing beneath the
shell — since its cleanup kills the leader's pid alone; and so does a fixture
that needs its group EMPTY once the leader is reaped
(`Test_handle_signalGroupAllowGone`): a forked `sleep` outlives the kill as
an orphaned zombie only init reaps, and darwin answers a zombie-only group
whose leader was collected with `EPERM`, a refusal `Stop` cannot read as gone
(§Stop);
`creds_unix_internal_test.go` pins `UnknownUser` / `UnknownGroup`.
`Test_applyNice` and `Test_applyPostStart` apply every niceness to a
disposable child (`niceChild`: a shell blocked on a pipe the test closes, so
it exits without a signal) and read it back, never to the test process: an
unprivileged process cannot lower its niceness again, and a target computed
from the niceness read earlier became a lowering once the other test, or the
previous run of `go test -count=N`, had raised it further (on Linux, where
niceness is per thread, the read and the write did not even reach the same
thread) — `RLIMIT_FAILED` from the second run on, on every OS.
`exec_windows_test.go` covers the Windows backend (skipped where `cmd.exe` is
not found) and `exec_other_test.go` the `UnsupportedPlatform` stub;
`exec_windows_internal_test.go` pins the order of a confined spawn (below).

### A Setpgid Windows child gets a console event, not a refusal (#933)

`os.Process.Signal` honours `os.Kill` alone on Windows, so `Stop`'s SIGTERM
used to be refused, the whole grace elapsed for nothing and TerminateProcess
followed: no Windows child ever had a graceful shutdown through the port. A
`Setpgid` child leads a console process group (`CREATE_NEW_PROCESS_GROUP`), so
`SignalGroup` — and `Stop`'s first phase, which goes through it — now sends
that group the console control event (`ctrlevent_windows.go`,
`GenerateConsoleCtrlEvent(CTRL_BREAK_EVENT, pid)`) for SIGINT, SIGTERM,
SIGHUP and SIGQUIT alike, as `proc/signal`'s `Relay` does for a group target.
SIGINT is CTRL_BREAK too (2026-11-11): Windows delivers no CTRL_C to one group
— `GenerateConsoleCtrlEvent(CTRL_C_EVENT, pgid)` succeeds and no process of the
group receives it — and a group `CREATE_NEW_PROCESS_GROUP` made starts with
Ctrl-C ignored, so the first mapping, CTRL_C for SIGINT, delivered nothing. A
Go child reads CTRL_BREAK as `os.Interrupt`, the SIGINT a Unix child reads, so
#933's "observed as CTRL_C" is kept as "observed as os.Interrupt", and a
program that tells CTRL_C from CTRL_BREAK with its own console handler sees
CTRL_BREAK. SIGKILL stays TerminateProcess, and a child without `Setpgid`
keeps the leader-only path: CTRL_BREAK to the supervisor's own console group
would reach the supervisor. The supervisor must share a console with the child
— one without a console (a service) is refused, the refusal swallowed by
`Stop` as before. **Never run here:** `TestConsoleEventIsBreakForEveryGroupSignal`
(the mapping), `TestWindowsStopDeliversCtrlBreak` (a helper, the test binary,
exits 3 on `os.Interrupt`; `Stop(5s, SIGTERM)` must return in under a second
with code 3), `TestWindowsSignalGroupInterruptIsObserved` (`SignalGroup(SIGINT)`
acknowledged by the helper, which keeps running until `Stop`'s event) and
`TestWindowsSignalGroupWithoutAGroup` run on the Windows lane of e2e-cross
only, the helpers skipping on a host without a console. The helper role
(`SDK_EXEC_CTRL_HELPER`) is taken out of the inherited environment before it
is set, and a helper starts nothing and ends within 30 s.

### A confined Windows child is created suspended (#304)

A `Spec` with mappable `Rlimits` spawns with `CREATE_SUSPENDED`; `confine`
assigns the child to its Job Object through the handle CreateProcess returned
(`Process.WithHandle`, never a pid reopened), then resumes it —
`os.StartProcess` closes the primary thread's handle, so `resumeSuspended`
finds the thread again in a toolhelp thread snapshot. A failed assignment or
resume kills and reaps the child. The child therefore runs no instruction
outside its job, and cannot exit before it is assigned.

It used to run from CreateProcess on and be assigned afterwards. A process
that has already exited is refused by `AssignProcessToJobObject` with
`ERROR_ACCESS_DENIED`, so a `cmd /c exit 0` that was quick enough made `Start`
return `RLIMIT_FAILED` for a spawn that did nothing wrong
(`TestWindowsRlimitMemoryMapped`, run 38497960840). Measured on windows-latest,
8 spawns in flight: the old order failed 9 of 2 000, then 12, 26 and 14 of
3 000 on three hosts, every one `ERROR_ACCESS_DENIED` from the assignment; the
suspended order failed 0 of 3 000 on the same three hosts.
`Test_confineRunsTheChildOnlyOnceAssigned` holds the suspended child for
500 ms and requires it not to have exited before `confine`;
`Test_anExitedChildIsNotAssigned` pins the Windows refusal the order answers.
The cgroup port's `Assign` (`proc/cgroup`) takes a pid it did not spawn and
keeps its own contract.
