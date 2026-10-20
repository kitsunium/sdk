<!-- updated: 2026-10-18T00:00:00Z -->
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
| `doc.go` | all | the package comment — kit writes it from the design (ADR 0167) |
| `exec.go` | all | `validateSpec` (empty Path ⇒ `InvalidSpec`) |
| `exec_unix.go` | `unix` | `Start`: validate → check limits → resolve creds → `SysProcAttr` → `os.StartProcess` through `childwait.Spawn` (`forkClaimed`) → post-start attrs; `teardown` on attr failure; `awaitTrampoline` reaps a trampoline that failed before exec, through the claim |
| `exec_other.go` | `!unix && !windows` | `Start` ⇒ `UnsupportedPlatform` (compiles everywhere) |
| `exec_windows.go`, `handle_windows.go`, `joblimits_windows.go`, `cgroup_placement_windows.go` | `windows` | the CreateProcess backend: stdio, a console process group for `Setpgid`, rlimits through a Job Object the child is assigned to while suspended (§A confined Windows child is created suspended); the Unix-only fields refused with `UnsupportedPlatform` |
| `lookpath.go` | `unix \|\| windows` | `resolveSpec`: a bare `Spec.Path` searched in the CHILD's PATH (Spec.Env's, else the parent's), os/exec's rules — first executable wins, a relative match is `exec.ErrDot`, none is `exec.ErrNotFound`, both wrapped in `SpawnFailed`; argv[0] keeps the name as written |
| `lookpath_unix.go` / `lookpath_windows.go` | per OS | the candidate spellings (PATHEXT on Windows), what "executable" means (an execute bit / the extension), and how variable names compare (case-insensitive on Windows) |
| `handle_unix.go` | `unix` | the `handle` value: `PID`/`Wait`/`Signal`/`SignalGroup`/`Stop`, the SIGKILL escalation resent until the reap (`killUntilReaped`, §Stop), once-only reap through `collectExit` (claim first, own wait, `Reclaim` on ECHILD — it takes a `waiter`, the process's own wait and nothing else; `releaser` adds the handle release an aborted spawn needs), exit translation (`exitValue`), stdio-copier join |
| `stdio.go` | `unix \|\| windows` | `buildStdio`: wires `Spec.Stdio` (inherit/null/capture) to `ProcAttr.Files`; capture pipes + copier goroutines joined by `Wait` (100% delivery, no leak) |
| `trampoline_unix.go` | `unix` | the re-exec trampoline (`installTrampoline`, `childHandshakeFD`) — see below |
| `handshake_unix.go` | `unix` | the parent side of the trampoline's status pipe and `handshakeError` (status byte → sentinel) |
| `creds_unix.go` | `unix` | `Spec.User/Group/Groups` → `syscall.Credential` via `os/user` |
| `attrs_unix.go` | `unix` | best-effort `Nice` (setpriority) + `OOMScoreAdj` (procfs); ESRCH detection |
| `zombie_darwin.go` / `zombie_other.go` | `darwin` / `unix && !darwin` | `leaderIsZombie`: on darwin, `getpgid(2)` answers ESRCH for an exited, unreaped child (see §Stop); `false` on every other kernel, which never needs it |
| `limits_unix.go` | `unix` | `checkLimits`, before the spawn: `UnknownResource` for a resource with no stdlib `RLIMIT_*` mapping; every mapped one and a `Umask` pass, applied by the trampoline — a limit the kernel then refuses is `RlimitFailed`, through the handshake |
| `cgroup_placement_linux.go` | `linux` | `validateCgroupPath` (pre-spawn: missing/not-a-cgroup ⇒ `CgroupUnavailable`) + `applyCgroupPlacement` (trampoline writes pid → `cgroup.procs`) |
| `cgroup_placement_other.go` | `unix && !linux` | `validateCgroupPath` rejects a non-empty path with `UnsupportedPlatform`; no cgroup v2 off Linux |
| `limittable_unix.go` | `unix` | `Resource` → `RLIMIT_*` table (stdlib constants only) |
| `limittable_as.go` / `limittable_openbsd.go` | `unix && !openbsd` / `openbsd` | `addPlatformLimits`: `RLIMIT_AS` where the stdlib exports it; nothing on OpenBSD, whose kernel has none, so `ResourceAS` is `UnknownResource` there |
| `maxrss_rss64_unix.go` / `maxrss_rss32_unix.go` | `unix` except / only on `386 \|\| arm \|\| mips \|\| mipsle` | `maxRSSKB`: `Rusage.Maxrss` as `int64`, widened from the `int32` those four 32-bit targets declare, then normalised to KiB by `peakKiB` |
| `maxrss.go` | all | `peakKiB(raw, unitsPerKiB)`: passes a kilobyte figure through, divides a byte figure by 1024 rounding up; platform-neutral so its table test runs on every GOOS |
| `maxrss_unit_darwin.go` / `maxrss_unit_kib_unix.go` | `darwin` (ios included) / `unix && !darwin` | `rssUnitsPerKiB`: 1024 on darwin, whose `getrusage(2)` reports **bytes** (issue #357); 1 on Linux and the BSDs, which report kilobytes. illumos and Solaris define the field in pages and leave it 0 |
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
