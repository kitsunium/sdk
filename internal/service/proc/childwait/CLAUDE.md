# internal/service/proc/childwait/

## Purpose

The one place in the SDK that decides who receives a child's exit status
(ADR 0093). A process has two kinds of waiter for its children: the `Process`
handle from `service/proc/exec`, which waits for the one child it spawned, and
the reaper from `service/proc/reaper`, which answers every SIGCHLD with
`wait4(-1)` and so collects ANY child. The kernel gives a zombie's status to
exactly one wait. Before this package, when the sweep got there first the
handle's own wait failed with ECHILD and a child that exited 0 was reported as
`WAIT_FAILED` — measured downstream at 5 of 120 clean exits under load, each
one read as a failure and restarted.

The ledger makes the status reach its owner whoever collects it. **Stdlib-only**
(`os`, `sync`, `syscall`), no codes minted, no other SDK import.

## Contents

| File | Build tag | Role |
|---|---|---|
| `childwait.go` | (all) | package doc; `Claim`; the process-wide `ledger`; `Spawn`; `Claim.Collected` / `Reclaim` / `Release`; `deliver` / `handOver` / `forget` |
| `childwait_unix.go` | `unix` | `StatusValue` (`syscall.WaitStatus` + `syscall.Rusage`); `ReapAny` — the only `wait4(-1)` in the SDK |
| `childwait_other.go` | `!unix` | `StatusValue` as an empty struct: no wait4, no sweep, a claim is never filled |

## Surface

| Caller | Uses | When |
|---|---|---|
| `service/proc/exec` (spawn) | `Spawn(start)` | every Unix fork/exec — returns the process AND its claim |
| `service/proc/exec` (handle) | `Claim.Collected`, then its own `os.Process.Wait`, then `Claim.Reclaim` on ECHILD, `Claim.Release` otherwise | every `Wait` |
| `service/proc/exec` (handle) | `Claim.Collected` | every `Signal`: a collected leader is never signalled by pid |
| `service/proc/reaper` | `ReapAny()` in its drain loop | every sweep, loop or `ReapOnce` |

## The protocol — three races, each closed by one lock

1. **A child can exit before it is claimed.** `Spawn` holds `spawning` SHARED
   from before the fork until the pid is in the map. Every hand-off takes
   `spawning` EXCLUSIVELY before its lookup, which waits out every spawn that
   may have forked the pid — so the claim it finds is the NEWEST child's, and a
   claim a recycled pid outlived (its child reaped by something outside the SDK
   before its owner waited) has already been replaced. Looking up first and
   gating only on a miss would hand a new child's exit to that stale claim.
   A pid still unclaimed then is an orphan's, and its status is dropped.
   Nothing unclaimed is ever stored "in case": a stored orphan status would be
   handed to the next child that recycles the pid.
2. **The owner's ECHILD arrives before the hand-off.** The kernel hands the
   zombie over INSIDE the sweep's `wait4`, before that sweep has returned to
   store it. `ReapAny` holds `sweeping` from before its `wait4` until after the
   hand-off, and `Reclaim` takes `sweeping` first — so an owner whose wait saw
   ECHILD is ordered after the hand-off it is looking for.
3. **Several SIGCHLDs merge into one.** Nothing here depends on counting
   signals: the reaper drains `ReapAny` until it returns 0 or ECHILD, and each
   collected pid is handed over on its own.

Lock order is `sweeping` → `spawning` → `mu`, everywhere. `Reclaim` never takes
`spawning`; `Spawn` never takes `sweeping`; nothing waits for a process while
holding any of them.

## Why the owner still waits for its own child

The alternative was to make the reaper the ONLY waiter while it runs and have
`Wait` block on a channel. It was rejected: every `Wait` would then depend on
the reaper loop being alive (a `Stop`, a SIGCHLD disposition changed by someone
else, a wedged loop — and `Wait` hangs), and every exit would pay a signal
delivery before it is seen. Keeping the targeted wait costs one ECHILD round
trip in the minority of cases the sweep wins, and keeps a process that never
starts a reaper — every supervisor that is not pid 1 — exactly as it was:
`Spawn` adds one `Claim` allocation and one map insert beside a fork/exec, and
the owner's own wait always succeeds.

## Do NOT

- Call `wait4(-1)` / `syscall.Wait4(-1, …)` anywhere else in the SDK. A second
  unclaimed collector is exactly the defect this package closes.
- Wait by pid for a child whose claim is already `Collected`: its pid may belong
  to another process by now. Read the claim first.
- Keep a status nobody claims. Drop it — see race 1.
- Hold `spawning` across anything that can wait for a process (the trampoline
  handshake, a teardown `Wait`). `Spawn` releases it as soon as the pid is
  claimed; `exec` awaits its handshake after `Spawn` returns.

## What it does not cover

Children spawned OUTSIDE the SDK — `os/exec`, `syscall.ForkExec`, a C library —
are never claimed. A running reaper still collects them, and their own `Wait`
still fails with ECHILD (`os/exec` reports `waitid: no child processes`). The
SDK cannot route another package's wait; a program that runs the reaper must
spawn what it waits for through `pkg/v1/process`. ADR 0093 §Consequences.

## Verification

```sh
bazel test --config=race //internal/service/proc/childwait:childwait_test
# Fallback
cd internal/service && GOWORK=off go test -race -cover ./proc/childwait/...
```

The protocol tests drive a private ledger with a fabricated pid, so each
interleaving is forced rather than hoped for, and each was checked to fail with
its lock removed. The race as a user meets it — a reaper running while
children that exit 0 are spawned and waited — is
`service/proc/reaper`'s `TestWaitGetsTheExitWhileTheReaperRuns`.
