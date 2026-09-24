# ADR 0093 — a sweep takes the zombie, never the status

- **Status**: Accepted
- **Date**: 2026-09-24
- **Deciders**: SDK maintainers
- **Related**: [ADR 0016](0016-sdk-process-supervision-domain.md) (the `proc` domain, whose `Process` and `Reaper` ports this reconciles), [ADR 0018](0018-sdk-cross-platform-portability.md) (why the mechanism must be stdlib `syscall` on every Unix)

## Context

A process has two kinds of waiter for its children, and the SDK ships both.

- The `Process` handle returned by `service/proc/exec` waits for the one child
  it spawned (`os.Process.Wait`: `waitid(P_PIDFD)` on Linux, `wait4(pid)`
  elsewhere).
- The reaper in `service/proc/reaper` — switched on by a supervisor that runs as
  pid 1 or as a subreaper — answers every SIGCHLD with `wait4(-1, WNOHANG)` until
  `ECHILD`. `-1` means ANY child, the handle's included.

The kernel gives a zombie's status to exactly one wait. When the sweep got there
first, the handle's own wait failed with `ECHILD`, `handle.Wait` wrapped that as
`WAIT_FAILED` with a zero `ExitValue`, and the status — typically an exit 0 —
was gone. The reaper port's own documentation called this safe ("Wait4 is
kernel-serialised, so each child's exit is observed exactly once across whoever
sweeps"): true, and exactly the problem, since the one observer was the wrong
one.

It was found downstream. A supervisor running as pid 1 maps a failed `Wait` to
exit code -1, so under `restart: on-failure` a service that had stopped cleanly
was restarted. Its own log, every time: `exiting with code 0` → `reaper: reaped
zombie process(es) count=1` → `Service failed … exit_code=-1` → `Service
restarting`. Measured there on 120 runs per cell under load: **5 failures with
the reaper on, 0 with it off**.

Reproduced here before any change, by
`internal/service/proc/reaper/handoff_unix_external_test.go` (reaper running,
children running `sh -c 'exit 0'` spawned through `exec.Start`, 1 000 children
per cell, darwin/arm64, `go1.27.1`):

| Wait called… | machine load | before | before, `-race` |
|---|---|---|---|
| at once, one spawner | load average ≈ 47 on 10 cores | 0 / 1000 | 0 / 1000 |
| at once, eight concurrent spawners | load average ≈ 47 | **43 / 1000** | **40 / 1000** |
| after a sweep collected the child | load average ≈ 47 | **1000 / 1000** | **1000 / 1000** |
| at once, one spawner | + 12 CPU burners (load 65–110) | **1 / 1000** | **9 / 1000** |
| at once, eight concurrent spawners | + 12 CPU burners | **175 / 1000** | **231 / 1000** |
| after a sweep collected the child | + 12 CPU burners | **1000 / 1000** | **1000 / 1000** |

The "at once" rows are the race a supervisor meets; they depend on the
scheduler and grow with load. The "after" row removes the scheduler from the
question: once the reaper has taken the zombie, the old code could never recover
the status.

## Decision

### 1. One ledger, one `wait4(-1)`

A new service package, `internal/service/proc/childwait`, owns the question of
who receives a status. It holds, process-wide, a **claim** per child the SDK
spawned, keyed by pid. `childwait.ReapAny` is the only `wait4(-1)` in the SDK:
the reaper's drain loop calls it instead of `syscall.Wait4`. When it collects a
claimed pid it stores the status (`WaitStatus` + `Rusage`) on the claim before it
returns. The reaper's count, errors and lifecycle are unchanged; a claimed child
it collects is still counted, because it did reap it.

### 2. Fork and claim are one step as far as a sweep can tell

A child can exit — and a sweep collect it — before `os.StartProcess` has even
returned to the spawner. `childwait.Spawn` runs the fork under a gate held
SHARED until the pid is claimed. Every hand-off takes the gate EXCLUSIVELY
before it looks the pid up, which waits out every spawn that may have forked
that pid: the zombie just collected is the newest process to have held it, so
the claim then found is that child's, and a claim a recycled pid outlived has
already been replaced by it. Looking up first and gating only on a miss would
give a new child's exit to such a stale claim. A pid still unclaimed is an
orphan's, and its status is dropped — never kept "in case", because a stored
orphan status would be handed to the next child that recycles the pid.

### 3. The owner keeps waiting for its own child

`handle.Wait` now reads its status from whichever waiter took it, in this order:

1. the claim is already filled — a sweep came first: use it, and do NOT wait by
   pid for a process that is gone, whose pid may belong to another process;
2. otherwise its own `os.Process.Wait` — always the case when no reaper runs;
3. that wait fails with `ECHILD` → `Claim.Reclaim`, which takes the lock a sweep
   holds from before its `wait4` until after its hand-off, then reads the claim.
   The kernel hands the zombie over INSIDE the sweep's `wait4`, so the owner's
   `ECHILD` can arrive before the status is stored; taking the lock orders the
   owner after the hand-off.

A status nothing in the SDK collected — the child was reaped by code outside it
— is still `WAIT_FAILED`, and now that is the only thing `WAIT_FAILED` means
here. `handle.Signal` reports a leader a sweep collected as finished
(`os.ErrProcessDone`, exactly as after `Wait`) instead of signalling its pid.

Lock order is `sweeping` → `spawning` → `mu` everywhere; nothing waits for a
process while holding any of them.

### 4. The port says so

`core/proc.Reaper` now states that an implementation must hand a `Process`'s
child status to that `Process`, and `Process.Wait` that it reports the real exit
even when a `Reaper` collected the child first. `pkg/v1/reaper`'s doc names the
guarantee and its boundary (§Consequences).

## Consequences

- **Measured after**, same test, same machine, same 1 000 children per cell:
  **0 / 1000 in every row of the table above, with and without `-race`**,
  including under the twelve CPU burners.
- **Cost.** An idle sweep takes one more uncontended mutex: `ReapOnce` with no
  children went from 166 to 170 ns (median of six), still zero allocations. A
  spawn adds one `Claim` allocation, one map insert and a shared-lock round trip
  beside a fork/exec measured in `exec/BENCH.md` at ~626 µs.
- **Not covered: children spawned outside the SDK.** `os/exec`,
  `syscall.ForkExec` and C libraries never claim anything, so a running reaper
  still takes their status and their own wait still fails with `ECHILD`
  (`os/exec` says `waitid: no child processes`). The SDK cannot route another
  package's wait. A program that runs the reaper in-process must spawn what it
  waits for through `pkg/v1/process`, or keep pid 1 out of its own process (an
  init shim that only reaps and forwards signals). Stated in `pkg/v1/reaper`'s
  doc, in `childwait/CLAUDE.md`, and here.
- **A claim can outlive its child** only when something outside the SDK reaps
  the child before its owner waits (an owner already blocked in `Wait` gets
  `ECHILD` at once and ends it). A newer SDK child that recycles the pid
  replaces that claim before any hand-off (§2); what remains is a stale claim
  receiving the exit of a NON-SDK process that recycled the pid — which needs
  an outside reaper, a pid wrap, and an owner that still has not waited.
- **PID reuse.** The owner reads its claim before waiting by pid, so a child a
  sweep already collected is never waited for by number. A residual window
  remains between that read and the wait itself, on platforms where Go has no
  pidfd; on Linux ≥ 5.4 `os.Process.Wait` uses `waitid(P_PIDFD)`, which cannot
  reach another process. Hitting it requires the kernel to hand the same pid to
  a new child of this process inside that window — with sequential allocation,
  after the whole pid space has wrapped.
- **Merged SIGCHLDs** need nothing new: the reaper already drains until `0` or
  `ECHILD`, and each collected pid is handed over on its own.
- No new error code, no new public symbol, no dependency. `pkg/v1/reaper` and the
  two core ports gain documentation only.

## Breaking changes

None. No exported symbol, signature, error code or `ExitValue` field changes.
The observable difference is the fix itself: a `Wait` that returned
`WAIT_FAILED` because a running reaper collected the child now returns that
child's `ExitValue`.

## Alternatives considered

**(a) The reaper leaves claimed children alone** — peek with
`waitid(P_ALL, …, WEXITED|WNOHANG|WNOWAIT)` and reap only pids nobody claims.
Two independent reasons, either sufficient:

- *It is not reachable.* Read in the pinned toolchain rather than assumed:
  go1.27.1's `syscall` declares no `Waitid` on any GOOS, and `SYS_WAITID` exists
  for linux and darwin only. `golang.org/x/sys` is banned SDK-wide. A raw
  `syscall.Syscall` would cover two of the eight platforms ADR 0018 builds for,
  and on OpenBSD it returns `ENOSYS` for every trap but `SYS_IOCTL`
  (`syscall_openbsd_libc.go`: "OpenBSD 7.5+ no longer supports indirect
  syscalls").
- *It is wrong even where reachable.* `WNOWAIT` with `P_ALL` reports the SAME
  first waitable child every time. One claimed zombie whose owner has not waited
  yet hides every orphan behind it, and when the owner finally consumes it no
  SIGCHLD is sent — the orphans wait for an unrelated signal, or forever if the
  owner never calls `Wait`.

**(b′) The reaper is the only waiter while it runs**, and `Wait` blocks on a
channel it fills. Rejected for what it couples: every `Wait` would depend on the
reaper loop being alive — a `Stop`, a SIGCHLD disposition changed by someone
else, a wedged loop, and `Wait` hangs — and every exit would pay a signal
delivery and a goroutine wake-up before it is seen. Keeping the owner's targeted
wait (§3) costs one `ECHILD` round trip in the minority of cases the sweep wins,
and leaves a process that never starts a reaper — every supervisor that is not
pid 1 — exactly as it was.

## Deferred

- **Children spawned outside the SDK** (§Consequences). Out of reach by
  construction: their waits are not the SDK's to route. A claim API for them
  would not help — `os/exec`'s own `Wait` still issues its own `waitid` and
  still gets `ECHILD`.
- **The PID-reuse window off pidfd** (§Consequences). Closing it means waiting
  through a handle rather than a number on every Unix, which the stdlib does
  not offer outside Linux.

## Verification

- `internal/service/proc/reaper/handoff_unix_external_test.go` — the table
  above; `SDK_REAPER_HANDOFF_RUNS` raises the default of 200 children per case.
- `internal/service/proc/childwait/childwait_internal_test.go` — each race forced
  on a private ledger with a fabricated pid: a sweep collecting a pid while its
  spawn is in flight, an owner reclaiming while the hand-off is in flight, a pid
  reused under a stale claim, a lost status, a nil claim. Each test was run with
  its lock removed and fails.
- `internal/service/proc/childwait/childwait_unix_internal_test.go` — real
  children with exit codes 0, 1, 7 and 42 plus an unclaimed one, drained through
  `ReapAny`: every claim holds its own child's code, the orphan leaves nothing.
