# internal/service/proc/childwait/

## Purpose

The one place in the SDK that decides who receives a child's exit status
(ADR 0093). The kernel hands a zombie's status to exactly ONE wait, and the SDK
has two kinds of waiter for its children: the `Process` handle from
`service/proc/exec`, which waits for the one child it spawned, and the reaper
from `service/proc/reaper`, whose sweep collects ANY exited child. The ledger
makes each status reach the handle that owns it, whichever of the two collected
it, so `Process.Wait` always reports the child's real exit.

**Stdlib-only** (`os`, `sync`, `syscall`), no codes minted, no other SDK import.

## Who owns `wait4`

| Call | Where | Collects |
|---|---|---|
| `ReapAny` → `wait4(-1, WNOHANG)` | this package; called only by the reaper's drain | any exited child of the process |
| `os.Process.Wait` (`waitid(P_PIDFD)` on Linux ≥ 5.4, `wait4(pid)` elsewhere) | `exec`'s `collectExit`, for its own child | that child only |

Nothing else in the SDK calls `wait4`. The reaper is the only collector that
can take a child it did not spawn, so every status it takes for a claimed child
is stored on that child's claim before `ReapAny` returns.

## The reaper is off unless a consumer starts it

`reaper.New()` is idle; only `Start` (or an explicit `ReapOnce`) sweeps. A
consumer starts it when it is pid 1 or a subreaper — the agent does so only as
pid 1. With no sweep running, nothing calls `wait4(-1)`: each handle's own wait
always collects its child, and this package is pure bookkeeping — one `Claim`
allocation and one map insert per spawn, removed by `Release` after the wait.

## Contents

| File | Build tag | Role |
|---|---|---|
| `childwait.go` | (all) | package doc; `Claim`; the process-wide `ledger`; `Spawn`; `Claim.Collected` / `Reclaim` / `Release`; `deliver` / `handOver` / `forget` |
| `childwait_unix.go` | `unix` | `StatusValue` (`syscall.WaitStatus` + `syscall.Rusage`); `ReapAny` |
| `childwait_other.go` | `!unix` | `StatusValue` as an empty struct: no `wait4`, no sweep, a claim is never filled |

## Surface

| Caller | Uses | When |
|---|---|---|
| `exec` (spawn) | `Spawn(start)` | every Unix fork/exec — returns the process AND its claim |
| `exec` (`collectExit`) | `Collected`, then its own `os.Process.Wait`, then `Reclaim` on ECHILD, `Release` otherwise | every `Wait`, and the trampoline's aborted-spawn reap |
| `exec` (`Signal`, `SignalGroup`) | `Collected` | a leader whose status a sweep took is never signalled by pid |
| `reaper` (drain) | `ReapAny()` until 0 or ECHILD | every sweep, loop or `ReapOnce` |

## How a claimed child gets its status

1. **Spawn claims it.** `Spawn` runs the fork under the `spawning` gate held
   SHARED and puts the pid in the map before releasing the gate.
2. **Whoever collects it, the claim ends up right.**
   - The handle's own wait collects it (always the case without a reaper):
     the handle has the status; `Release` drops the claim.
   - A sweep collects it: `ReapAny` holds `sweeping` from before its `wait4`
     until the status is stored on the claim (`deliver` → `handOver`), which
     also retires the claim from the map.
3. **The handle reads the claim first.** A filled claim means the child is
   gone: its status is used and its pid is NEVER waited for or signalled by
   number — the pid may already belong to another process. `exec` releases the
   `os.Process` in that case, since its own `Wait` never completed.
4. **ECHILD from the handle's own wait** means someone else took the child. The
   kernel hands the zombie over INSIDE the sweep's `wait4`, before the sweep has
   stored it, so `Reclaim` takes `sweeping` first — ordering the handle after
   the hand-off — then reads the claim. Still empty: something outside the SDK
   reaped the child and the status is lost (`WAIT_FAILED`), deterministically,
   never a hang.

## A child that exits before it is registered

It can exit — and a sweep collect it — before `os.StartProcess` has returned to
the spawner. Every hand-off therefore takes `spawning` EXCLUSIVELY before it
looks the pid up, which waits out every spawn between fork and claim. The zombie
just collected is the newest process to have held that pid, so the claim then
found is that child's: a claim a recycled pid outlived (its child reaped by
something outside the SDK before its owner waited) has already been replaced by
the newer spawn. A pid still unclaimed then is an orphan's, and its status is
dropped — never stored "in case", or it would be handed to the next child that
recycles the pid.

Several SIGCHLDs merging into one needs nothing: the reaper drains `ReapAny`
until it returns 0 or ECHILD, and each collected pid is handed over on its own.

## Locks

Order is `sweeping` → `spawning` → `mu`, everywhere. `Reclaim` never takes
`spawning`; `Spawn` never takes `sweeping`; nothing waits for a process while
holding any of them.

## Why the handle keeps its own wait

Making the reaper the ONLY waiter while it runs would make every `Wait` depend
on the reaper loop being alive (a `Stop`, a SIGCHLD disposition changed by
someone else, a wedged loop — and `Wait` hangs) and would add a signal delivery
to every exit. Keeping the targeted wait costs one ECHILD round trip in the
cases the sweep wins, and leaves a process with no reaper exactly as simple as
a plain `os.Process.Wait`.

## What it does not cover

Children spawned OUTSIDE the SDK — `os/exec`, `syscall.ForkExec`, a C library —
are never claimed. A running reaper collects them too, and their own `Wait` then
fails with ECHILD (`os/exec` reports `waitid: no child processes`). The SDK
cannot route another package's wait: a program that runs the reaper must spawn
what it waits for through `pkg/v1/process`.

Off pidfd (everything but Linux ≥ 5.4) a window remains between reading an empty
claim and the handle's own `wait4(pid)`: hitting it needs a sweep to take the
child AND the pid to be recycled to another child in between.

## Do NOT

- Call `wait4(-1)` / `syscall.Wait4(-1, …)` anywhere else in the SDK. A second
  collector that does not hand statuses over loses them.
- Wait by pid for, or signal by pid, a child whose claim is `Collected`.
- Keep a status nobody claims.
- Look a collected pid up before taking `spawning` exclusively.
- Hold `spawning` across anything that can wait for a process (the trampoline
  handshake, a teardown `Wait`). `Spawn` releases it as soon as the pid is
  claimed; `exec` awaits its handshake after `Spawn` returns.

## Verification

```sh
bazel test --config=race //internal/service/proc/childwait:childwait_test
# Fallback
cd internal/service && GOWORK=off go test -race -cover ./proc/childwait/...
```

The protocol tests drive a private ledger with a fabricated pid, so each
interleaving (spawn in flight, stale claim on a recycled pid, hand-off in
flight, lost status, nil claim) is forced rather than hoped for; each fails
with its lock removed. The Unix test collects real children with known exit
codes through `ReapAny`. End to end — the reaper running while children that
exit 0 are spawned and waited — is `service/proc/reaper`'s
`handoff_unix_external_test.go`, also part of the `e2e-cross` lane.
