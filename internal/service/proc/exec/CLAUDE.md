<!-- updated: 2026-11-15T00:00:00Z -->
# internal/service/proc/exec

The keystone spawn primitive of the process-supervision domain (ADR 0016).
`Start` turns an immutable `coreproc.Spec` into a running, supervised process and
returns a `coreproc.Process` handle. This is the only package in the domain that
forks a process; the others (signal, reaper, rlimit, cgroup, systemd/notify) act on a
process that already exists.

> Reference moved to `internal/service/proc/exec/NOTES.md` (ADR 0253), under the same section titles: File map, Stop: group-aware SIGTERM → SIGKILL escalation, A child on a pseudo-terminal (#934), ProcessHandle and Open: signals that cannot reach a reused pid (#928), Attach and AttachPIDFile: adopting a process from a PID file (#729), The child's descriptor table is sealed (#1089), DieWithParent: a child that does not outlive its supervisor (#931), Exit translation, Rlimits & Umask: the re-exec trampoline, The sandbox: a trampoline in two stages (#728), Tests.

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

## Performance

`BENCH.md` answers "how much of `Start` is the kernel and how much is SDK
overhead": **0.06 %** is SDK preparation (377 ns of a 626 µs `/bin/true` launch),
and a CPU profile of a launch puts **zero** samples in any preparation function.
Nothing on the direct-spawn path was optimised, because nothing on it is
measurable. The two costs that are worth a caller's attention are the trampoline
above and `StdioCapture`, whose pipe + null-device setup is 4.9 % of a launch —
both descriptor and exec costs, neither of them this package's own code.
