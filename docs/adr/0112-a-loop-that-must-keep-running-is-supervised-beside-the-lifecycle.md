# ADR 0112 — a loop that must keep running is supervised, beside the lifecycle that starts it

- **Status**: Accepted
- **Date**: 2026-09-26
- **Deciders**: SDK maintainers
- **Related**: [ADR 0050](0050-sdk-lifecycle-domain.md) (the ordered start/stop this completes), [ADR 0103](0103-a-bucket-per-caller-one-backoff-curve-and-a-retry-on-the-clock-it-is-given.md) (the one backoff curve it waits), [ADR 0090](0090-a-port-named-in-public-must-be-implementable-in-public.md) (the clock it waits on), [ADR 0043](0043-drain-is-a-signal-not-a-cancellation.md) (a budget cancels and steps aside)

## Context

A `Lifecycle` (ADR 0050) orders what STARTS and what STOPS. Most components own
something that has to keep running in between: a queue consumer, a session
sweeper, a watcher, an outbox. That loop is where a service usually dies
without anybody noticing. It returns an error that nobody reads. It panics and
takes the process down with every other goroutine. Or it returns nil because a
`for` loop hit a `break` it should not have. Then the component is "up" and
does nothing.

The framework built on this SDK (kitsunium/platform, kit) supervises such loops
itself (`kit/loop.go`, `Service.Go`). It runs the function on its own
goroutine, restarts it after an error, a panic or an early return, waits a
backoff from one second to one minute between restarts, resets that backoff
after a run that lasted a minute ("a healthy run"), counts runs, restarts and
errors for its Studio, cancels on stop and waits for the goroutine. A program
that never imports kit wants exactly this, unchanged. The same curve is written
three times in kit (`loop.go`, `mailer.go`, `secret.go`). ADR 0103 published
that curve as `resilience.BackoffValue`, and this change gives it the loop it
was published for.

## Decision

### D1 — a `Supervisor` in the lifecycle domain

`internal/service/lifecycle` gains `NewSupervisor(name string, run
func(ctx) error, cfg SupervisorConfig) (*Supervisor, error)`, with `Start`,
`Stop` and `Component`. It is in `lifecycle` and not in `resilience`:

- **It is a component's other half, not a policy around a call.** Every
  `resilience` runner wraps ONE operation and returns when it returns. A
  supervisor never returns on its own: it owns a goroutine from `Start` to
  `Stop`.
- **It speaks the lifecycle's own contract.** `Component()` returns a
  `ComponentValue` whose `Start` begins the supervision and whose `Stop`
  cancels and joins it. A `Lifecycle` then budgets that join like any other
  `Stop`, and `Supervisor.Stop` reports an overrun with the lifecycle's own
  `STOP_TIMEOUT`.

The name and the function are positional because there is no supervisor
without them. Leaving either out is `SUPERVISOR_MISCONFIGURED` (`0.3.49.7`).
`SupervisorConfig` holds only optional tuning, so its zero value is a working
supervisor.

### D2 — every early end restarts, after a backoff that a healthy run resets

A run that returns before its context ends is a failure, however it ends:

- **with an error**: that error;
- **with nil**: a loop that stopped looping;
- **by panicking**: recovered into `RUN_PANICKED` (`0.3.49.6`). The value and
  the stack travel as fields, never as the origin and never in the Public
  text, which a status page may show.

The next run waits `Backoff.Delay(failures)`. The zero `BackoffValue` would
retry at once, which for a loop means a hot loop, so the zero is CLAMPED
(ADR 0031) to `DefaultRestartBase`–`DefaultRestartMax`: one second, doubling,
up to one minute. A run that lasted `HealthyAfter` (default
`DefaultHealthyRun`, one minute) was working, so its end starts the count
again. Its failure waits the first backoff, not the next one.

A run that ends because the supervision is stopping, and returns its
context's error, is the way out and not a failure. It is reported with a nil
error and `Stopping` set, and is never restarted.

### D3 — the observer is told everything; the supervisor writes nothing

`SupervisorConfig.Observe` receives a `SupervisionEventValue` for each of four
phases:

- a run started;
- a run ended, with its error, duration and `Stopping` flag;
- a restart scheduled, with the consecutive-failure count and the delay;
- the supervision ended.

Calls are serialised on the supervisor's goroutine. A framework builds its
loop view from these events: runs, errors, restarts, the next run time. The
supervisor writes to no logger and no stream (ADR 0030).

### D4 — `Start`'s context gives values, `Stop` gives the end

`Start` returns at once. Every run's context comes from the context `Start`
was given, detached from its cancellation (`context.WithoutCancel`). Its
values reach each run: a trace, a logger, profiling labels. Its cancellation
ends nothing, because a `Lifecycle` hands a component the context of its
STARTUP, which has to be able to end without ending what it started. `Stop`
cancels the running function's context and waits for the supervision to end.
When `Stop`'s own context ends first, it returns `STOP_TIMEOUT` and kills
nothing (ADR 0043's rule: a budget cancels and steps aside). A later `Stop`
waits again. A second `Start` while running is `SUPERVISOR_RUNNING`
(`0.3.49.8`). A `Start` after the supervision ended supervises again.

### D5 — every wait is on the injected clock

The backoff waits on `SupervisorConfig.Clock` (`clock.Timed`, ADR 0090). A test
drives each restart with a `ManualClock` and asserts "not one nanosecond early".
The package's existing audit (`TestPackageNeverWaitsOnTheWallClock`) covers the
new files, production and tests alike.

## Consequences

- kit's `Service.Go` becomes a `Supervisor` whose events feed kit's loop state.
  Its hand-written `supervise` loop, `backoff(n)`, the `healthyRun` constant
  and the panic recovery move out of kit.
- The rest of kit's backoff copies (the declared `Loop` after a failure, the
  secret rotator) call `resilience.BackoffValue{BaseDelay: time.Second,
  MaxDelay: time.Minute}.Delay(n)`, the same curve.
- Any component whose loop must keep running gets restart, backoff, panic
  containment and a join on stop in one line: `app.Add(sup.Component())`.

## Breaking changes

None. The supervisor and its three codes are new. `0.3.49.6`–`0.3.49.8` are
new serials in the block `service/lifecycle` already owns.

## Alternatives considered

- **Put it in `resilience`.** Rejected (D1): a runner there returns when its
  operation returns, and nothing in that domain owns a goroutine.
- **Make cancellation of `Start`'s context stop the supervision.** Rejected
  (D4). Under a `Lifecycle`, that context is a startup deadline. The first
  supervised component would be stopped the moment startup finished.
- **Restart only after an error, and treat an early nil return as done.**
  Rejected: the function's contract is "run until your context ends". A loop
  that returns nil early has stopped doing its job, and a supervision that
  silently ends would be the unnoticed death this exists to prevent.
- **Recover a panicking observer.** Rejected for symmetry with
  `Config.OnTransition`: the observer is the caller's code on the caller's
  terms. It must be short and must not panic.

## Deferred

- **A declared, wake-driven loop** (kit's `Service.Loop`: a period, a deadline,
  a topic, a nudge, bursts coalesced). The waking rules are kit's: they are
  what its diagram draws. What it shares with this supervisor, the backoff
  after a failure, is already the published curve.
- **A restart budget** ("give up after N restarts in M minutes"). Nothing here
  needs it yet, and giving up leaves a component "up" that does nothing, which
  is the failure this exists to avoid.

## Verification

- `internal/service/lifecycle/supervise_external_test.go`:
  - an error, a panic and an early nil return each restart after 1 s, 2 s and
    4 s, not a nanosecond early; the panic is `RUN_PANICKED` and its Public
    text quotes nothing;
  - a healthy run resets the count;
  - a stop during the backoff runs nothing more;
  - a function that ignores its context makes `Stop` return `STOP_TIMEOUT` and
    blocks a second `Start` until it returns;
  - `Start` after `Stop` supervises again, and `Start`'s values reach the run
    while its cancellation ends nothing;
  - `Component()` inside a `Lifecycle`;
  - a custom curve with its ceiling;
  - the two refusals.
- `pkg/v1/lifecycle`: the same through public names.

## References

- `internal/service/lifecycle/supervise.go`, `supervise_config.go`,
  `supervise_event.go`
- kitsunium/platform `docs/adr/0001-the-sdk-holds-the-mechanisms-kit-is-the-framework.md`
  (the map, wave 3)
