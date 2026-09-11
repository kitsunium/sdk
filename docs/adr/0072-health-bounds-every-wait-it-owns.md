# ADR 0072 — health bounds every wait it owns, including the two that belonged to somebody else

- **Status**: Accepted
- **Date**: 2026-09-12
- **Deciders**: SDK maintainers
- **Amends**: [ADR 0060](0060-sdk-health-domain.md) — the bounds, not the three-probe shape
- **Related**: [ADR 0043](0043-drain-is-a-signal-not-a-cancellation.md) (a cancellation is an announcement, not a kill), [ADR 0050](0050-sdk-lifecycle-domain.md) (a per-component budget nobody else can spend), [ADR 0031](0031-policy-zero-values-are-never-inert.md)

## Context

ADR 0060 made one rule the domain's centre: a probe must not become a place the
process hangs. Every check therefore runs under a budget. Two waits escaped it,
and both have the same shape — the bound existed, but it belonged to a party
that can walk away.

**The sd_notify announcement.** A readiness aggregate is announced to the
supervisor under one lock, which is what keeps two probes from leaving the
supervisor showing the older status. The datagram's write had no deadline. A
unixgram write blocks once the receiver's queue is full, and the receiver is
systemd: paused, stopped, or merely slow, it leaves the writer parked with
nothing this process can do. Under that lock, one stuck supervisor stops every
later probe from answering at all — the readiness the datagram announces made
unobservable by the act of announcing it. Measured: the queue stops a BRAND NEW
sender at 514 small datagrams on Linux, and every sd_notify send is a fresh
socket writing one small datagram.

**The check run.** A probe whose own caller goes away stops waiting and
deliberately cancels nothing (`departed`): that context belongs to one caller
while the run belongs to every probe waiting on it. But the budget lived only
on the waiting side, so a run the LAST caller had left was cancelled by nobody
and its body was never told its time was up. That is the ordinary shape of a
polled endpoint behind a proxy with a shorter timeout of its own: every probe
departs early, and a check that honours its context holds its dependency's
connection for the whole outage while every probe reports a timeout.

## Decision

**The announcement is bounded by the answer it belongs to.** `service/proc/sdnotify`
grows `NotifyContext` (plus `ReadyContext`/`StatusContext`), which hands ctx's
deadline to the kernel as the socket's write deadline and reaches a write
already parked through `context.AfterFunc` — the only way to interrupt a
`net.Conn`. `Notify` is that call on `context.Background`, so its behaviour is
unchanged, and its doc now says it waits as long as the supervisor makes it.
`health` passes the PROBE's context: an announcement must not outlive the
answer it belongs to. Where the probe carries no deadline — an HTTP handler's
context, `context.Background` — the bound is the budget a check would get
(`Config.DefaultTimeout`, else `DefaultCheckTimeout`), because an unreadable
notify socket and an unresponsive dependency are the same kind of wait and the
registry already states how long it tolerates one. That fallback is armed on
the INJECTED clock, never `context.WithTimeout`: this package waits on no wall
clock and its own AST audit enforces it.

**The budget belongs to the run.** `perform` arms it, so it expires whether or
not anyone is left waiting, and cancels the run's context at expiry — the same
announcement `abandoned` makes, meaning the same thing: the SDK cannot kill a
goroutine, so cancelling is all it can do and the body decides what that means.

And the run REMEMBERS that a budget is why it was cancelled. A check that
honours its context answers the announcement by returning `ctx.Err()`, which is
an ordinary failure wearing no timeout; the result the run publishes is what
every probe joining afterwards reads, so without the flag a dependency that was
simply too slow looks like a cancellation. Both sides — the waiter's expiry and
the run's own — go through one `inflight.expire`, and a body that SUCCEEDS
despite the cancellation keeps its success, because a late answer is not a
wrong one.

## Consequences

- A deaf supervisor now costs one budget per announcement instead of the
  process's readiness. The failure reaches `OnNotifyError` and nothing is
  recorded as announced, so the next probe owes the same datagram again.
- A run every caller left is cancelled at its budget. A body that honours its
  context releases its dependency; one that ignores it is unchanged, and still
  costs exactly one goroutine for the whole outage — the run stays outstanding
  and later probes join it.
- A run now arms one timer of its own, which is visible to any test driving the
  manual clock: a check being measured for the first time arms two timers, one
  for the probe and one for the run. `probeUnderClock`'s `waits` counts timers
  and says so.
- That timer also removed a latent flake. The leak guard advanced the clock
  once the PROBE's timer was armed, which could happen before the body's
  goroutine had run — the assertion then read zero calls, reliably outside the
  race lane and never inside it. Waiting for the run's timer proves the body
  has been entered.

## Breaking changes

Behavioural, and only where something was already stuck: a send that used to
block forever now fails, and a run nobody waits for now ends at its budget.
`Notify`'s signature and behaviour are unchanged. No API removed.

## Why not

- **Send outside the lock.** The lock is what makes "what was announced is what
  was delivered" true across concurrent probes; dropping it reintroduces a
  supervisor left showing the older status with nothing to correct it.
- **Make the announcement asynchronous.** A datagram sent from a goroutine
  nobody waits for is a claim the SDK cannot report on, and `OnNotifyError`
  would be called from a goroutine the caller never entered.
- **`context.WithTimeout` for the fallback.** It waits on the wall clock, which
  makes the bound untestable in the one way that matters — and this package
  already fails its build on `time.After`.
- **Cancel the run when the last caller leaves** (a waiter count). It answers
  the same defect but ties the body's lifetime to who happens to be watching:
  two probes arriving a microsecond apart would then decide whether the check
  keeps running. A budget is a property of the check.
- **Kill the goroutine at expiry.** Go cannot, and pretending otherwise is
  ADR 0047 §D9's defect one layer up.

## References

- `internal/service/proc/sdnotify/notify.go` (`NotifyContext`, `boundWrite`),
  `internal/service/health/notify.go` (`sendBudget`),
  `internal/service/health/runner.go` (`boundRun`, `outcome`),
  `internal/service/health/inflight.go` (`expire`).
- `TestNotifyContextStopsWaitingWhenTheSupervisorStopsReading`,
  `TestADeafSupervisorCannotStopTheProbeFromAnswering`,
  `TestAProbeWithNoDeadlineStillBoundsItsAnnouncement`,
  `TestARunAbandonedByEveryCallerStillExpires`,
  `TestAProbeJoiningAnExpiredRunReadsATimeout`.
