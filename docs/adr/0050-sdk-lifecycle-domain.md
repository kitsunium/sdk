# ADR 0050 — ordered start/stop domain (`lifecycle`), and the two shutdown bugs that argue for it

- **Status**: Accepted
- **Date**: 2026-09-10
- **Deciders**: SDK maintainers
- **Related**: [ADR 0043](0043-drain-is-a-signal-not-a-cancellation.md) (a drain is announced, not imposed), [ADR 0047](0047-sdk-net-websocket.md) §D9 (a hijacked connection is the handler's), [ADR 0031](0031-policy-zero-values-are-never-inert.md) (zero values), [ADR 0039](0039-extending-a-published-port-without-breaking-it.md) (published ports), [ADR 0041](0041-sdk-scheduler-domain.md) (the FUNC-port precedent), [ADR 0016](0016-sdk-process-supervision-domain.md) / [ADR 0026](0026-sdk-resilience-domain.md) (no-registry precedents)

## Context

The SDK has every piece of a service's startup and shutdown and no assembly of
them. A consumer wiring a logger, a metrics meter, a cache, a scheduler, a
session store and an HTTP server has six things to bring up in an order that
matters and six to take down in the reverse, plus a shutdown budget, plus — if
they are supervised — a signal and a readiness datagram. Each of those exists
here already. Nothing composes them, so the composition is written at each call
site, in `main`, where it is the least reviewed code in the program.

**Two lifecycle defects were found in this repository during the work that
led here, and they are the argument.** Neither is hypothetical and neither was
caught by a test, because in both cases the defect lived in the ordering of a
teardown rather than in any component:

1. **ADR 0043 — the drain that timed out on every deployment.** `http.Server.
   Shutdown` waits for in-flight requests, and a request that never ends never
   becomes idle. One open stream therefore consumed the caller's **entire**
   shutdown budget and returned `DRAIN_TIMEOUT`, after which `closeLive()`
   severed the socket underneath a handler that had no way to know. Measured
   before the fix: 5 s of a 5 s budget, timeout, every connected client, every
   deployment. Nothing covered it because no test held a request open across a
   shutdown.

2. **ADR 0047 §D9 — the teardown that closed what it had handed away.** The
   engine saw `StateHijacked`, released the `ServeConn`, and then ran its
   deferred `release`, which **closed the socket**. That close is right for
   every request/response handler and fatal for exactly one case: no
   upgrade-based protocol could work at all. Measured before the fix: the
   client read `EOF` and no error was reported anywhere.

Both are the same shape. A teardown ran, it ran in a way that looked
reasonable in isolation, and it destroyed something that was still needed — in
the first case because one participant spent a budget that belonged to all of
them, in the second because cleanup did not distinguish what it owned from what
it had given away. This ADR adds the domain whose job is to make that shape
unwriteable.

**Two justifications that were offered for this domain are not used, because
they do not hold.** The proposal originally argued that `lifecycle` is the
counterpart of a framework's `HttpKernel` — it is not: an `HttpKernel` turns a
request into a response and supervises neither the process nor its goroutines,
so the analogy names the wrong thing. It also pointed at `google/wire` as the
compatible model — that repository is **archived** (last push 2025-08-22), so
it is not a model to follow. A third claim, that consumers each rewrite this
badly, was asserted with no evidence and is dropped. The two defects above are
evidence; the rest was not.

## Decision

**A twelfth-and-then-some core sibling, `lifecycle`, with the standard four
layers and no registry.** `internal/core/lifecycle` (`0.2.19.*`),
`internal/service/lifecycle` (`0.3.49.*`), `pkg/v1/lifecycle`.

### D1 — the order is declared, not inferred; there is no graph

Components come up in the order they were added and go down in the exact
reverse. There is no `DependsOn`, no edge set, no cycle detection and no
parallel start.

A linear sequence **already is** a topological order, and the caller — who
wrote the constructors — is the only party that knows the real one. A graph
would let the SDK re-derive an order the caller already knows, at the cost of a
second way to express it, a class of errors that only exists once edges do
(cycles), and a concurrency model nobody asked for.

The cost is stated rather than hidden: **the SDK cannot tell a caller they
ordered them wrong**, because it has nothing to check the order against. A
graph would not fix that either — it would move the same mistake from the call
order into the edge declarations.

### D2 — no autowiring, ever

No reflection over constructors, no container, no tags that decide what gets
injected where. A convention that resolves dependencies is a *framework*, and
this SDK is a toolbox (ADR 0001). Explicit constructors, called in an order a
reader can see, are the entire mechanism. This is recorded as a decision so the
next proposal meets an answer rather than an omission.

### D3 — the ports are FUNC types (ADR 0039, satisfied structurally)

`Start` and `Stop` are `func(context.Context) error`, the shape ADR 0041
already established for `scheduler.Job`. `pkg/v1/lifecycle` aliases both, so
they are published; a published *interface* must not grow a method, and a func
type **cannot** grow one at all. `TestPortsAreFunctionsNotInterfaces` fails at
compile time if either is "improved" into an interface.

`Lifecycle` itself is a three-method interface (`Add`/`Start`/`Stop`) and is
frozen at that. A future capability lands on a sibling, per ADR 0039.

### D4 — a partial start is unwound, and there is exactly ONE unwind path

If the fourth component of six fails, the three that are up are stopped, in
reverse, **before `Start` returns**. Three details carry the guarantee:

- **A component is recorded as up the instant its `Start` returns nil, before
  the next `Start` is attempted.** There is no window in which a component is
  running and unrecorded, which is the window a `defer` registered after the
  error return leaves open.
- **The unwind calls the same function an ordinary `Stop` calls** —
  `stopEach`, over the same reversed list, with the same per-component budget.
  A second cleanup implementation for the failure path is precisely how the two
  drift until only the one people exercise stays correct.
  `TestTheUnwindAndAnOrdinaryStopProduceTheSameOrder` runs both routes and
  compares.
- **The component that FAILED is not stopped.** Its `Start` returned an error,
  so it never handed back a running thing, and a `Stop` on a half-constructed
  component is how a double-close gets written. A `Start` that fails owns what
  it acquired, exactly as a Go constructor does — and that rule is what lets
  every other `Stop` assume its own `Start` succeeded.

**The unwind does not inherit the cancellation that caused it.** A start most
often fails *because* its context was cancelled; each `Stop` is therefore
called with a context derived by `context.WithoutCancel`, so a well-behaved
`Stop` is not told to abandon the very teardown the unwind exists to perform.
`TestUnwindRunsEvenWhenTheStartContextIsAlreadyCancelled` is the guard.

A **panic** in a `Start` is recovered and becomes `ComponentPanicked`, then
unwinds normally. A panic escaping a `Start` would skip the unwind entirely and
leak every component already up — the failure this domain exists to prevent, on
the one path nobody looks at.

### D5 — the budget is per component, and expiry cuts nothing short

`Config.StopTimeout` is the budget **one** component's `Stop` gets, not a
budget for the whole shutdown. That is ADR 0043's lesson applied one layer up:
a shared budget lets the first participant that will not finish spend the
budget of every participant after it, and they are then severed without ever
being asked.

When a component's budget expires, exactly three things happen and deliberately
nothing more:

1. **The context handed to that `Stop` is cancelled.** That is an
   ANNOUNCEMENT — the one piece of information the component cannot otherwise
   have — and it is what a cooperative `Stop` selects on. It is the same
   instrument, and the same reasoning, as ADR 0043's drain signal.
2. **The engine stops WAITING and moves to the next component**, which gets its
   own full budget.
3. **A typed `StopTimeout` naming the component is collected** into the
   aggregate `Stop` returns, and a `TransitionValue` with `TimedOut` set is
   published to the observation hook.

The goroutine is **not killed** — Go cannot kill one — and **nothing the
component owns is closed on its behalf**. That second half is ADR 0047 §D9:
cleanup that does not distinguish what it owns from what it handed away
destroys live work and reports nothing. A budget that expires must not cut
short what was about to finish; it must say so and step aside.

The price is stated out loud rather than discovered: **a `Stop` of n
components returns in at most n × StopTimeout.** That is what not letting one
component spend everyone's budget costs.

`Stop(ctx)` reads its context for values and **never** for cancellation. The
context that tells a process to shut down is, almost always, one that has just
been cancelled; honouring it here would make every real shutdown a no-op.

### D6 — a non-positive budget CLAMPS (ADR 0031)

`StopTimeout <= 0` clamps to `DefaultStopTimeout` (30 s), which is exported so
the number a reader is pointed at can be read rather than copied out of prose.
It is never read as "stop immediately".

ADR 0031's line is whether the SDK can supply the value without inventing the
caller's requirement. It can here: this is a *field on a struct that does other
work*, so a zero is what an unset field looks like — not an unambiguous request
for a zero deadline the way `NewTimeout(0)` is a request for a deadline. And 30
seconds is the surrounding ecosystem's own floor (Kubernetes'
`terminationGracePeriodSeconds`), so it needs no explanation. Reporting
`STOP_TIMEOUT` for components that were about to succeed, because someone left
a line out, is not a shutdown policy.

The guard asserts the **observable** outcome — a component that finishes just
inside the clamped budget completes with no error — never the field value, so
it survives a change of mechanism.

### D7 — signals and sd_notify are opt-in fields, and nothing is reimplemented

`Run(ctx, lc, RunConfig{})` is start → wait on ctx → stop. `RunConfig.Signals`
subscribes through `internal/service/proc/signal`; `RunConfig.Notify` sends
READY=1 and STOPPING=1 through `internal/service/proc/sdnotify`. Neither
mechanism is rewritten here — this file wires, it does not implement.

Both are opt-in because both are **process-wide, observable side effects**, and
a library that installs them because it was imported fights the caller's own
`main`. It is the same posture as ADR 0030 (no SDK default writes to stdout)
and ADR 0048 (the OTLP HTTP emitter is never registered).

An empty `Signals` installs no handler at all, and the wait then has a nil
channel in its `select` — the right absence, exactly as `corenet.DrainSignal`
returns nil rather than a closed channel: receiving from nil blocks forever, so
the arm simply never fires and no nil check is needed.

A readiness that cannot be delivered takes the components back down, because a
supervisor kills a unit that never reports ready — leaving them up would leave
a process about to be killed with its components still running.

### D8 — the aggregate is an `errors.Join`, side by side and never a relabel

A failed `Start` returns `errors.Join(StartFailed, componentErr[, UnwindFailed,
…])`. `errs.Wrap` would hit the origin-wins rule (CLAUDE.md rule 6) and inherit
the component's own code, so a caller could no longer ask "did startup fail?"
without knowing every code every component might produce. Side by side, both
`errs.HasCode(err, CodeStartFailed)` and the caller's own `errors.Is` answer —
`errs.HasCode` walks `Unwrap() []error` for exactly this.

A broken unwind adds `UnwindFailed` **alongside** the start failure, never
instead of it. A teardown that also fails is a second defect, and reporting
only it sends the operator to debug the wrong component.

### D9 — no registry

The fifth in a row (`proc` ADR 0016, `resilience` ADR 0026, `net` ADR 0029,
`scheduler` ADR 0041, `token` ADR 0042, `session` ADR 0045, `validation` ADR
0046, `cache` ADR 0049). There is one ordering discipline and one engine; a
name→implementation map would have exactly one entry and would add a way to
misconfigure a startup order at runtime.

### D10 — the suite does not sleep, and that is enforced

Every budget is armed on the injected `clock.Timed`, and every case moves a
`ManualClock` past it. Nothing in the package — production **or** test — may
call `time.Sleep`/`After`/`AfterFunc`/`Tick`/`NewTimer`/`NewTicker`;
`TestPackageNeverWaitsOnTheWallClock` parses the package's own sources through
the AST and fails the build on any of them, and
`TestWallClockAuditDetectsAViolation` proves the audit fires by running it
against fixtures.

The temptation here is specific and strong — every timing case in this package
is "wait for a budget to expire", and `time.Sleep(budget)` is the shortest
thing to type. A sleep becomes a tolerance, a tolerance becomes a flake, and
the flake is eventually deleted along with the guard on D5. The rendezvous is
channels: a component signals when its `Stop` has been entered, at which point
the previous component's timer has been retired and the current one's is armed,
so `Advance(budget)` is deterministic. The budget timer is armed **before** the
work starts, not after the goroutine is scheduled, so "when does the budget
begin" has an answer.

## Consequences / Semantics

- A `Lifecycle` returns to its not-started state after a failed `Start`:
  nothing is up, so refusing every later `Add` and `Start` would leave the
  caller holding an object that reports a state it is not in.
- `Stop` is idempotent and is a no-op before `Start`. `defer lc.Stop(ctx)` is
  the shape every caller writes, and it runs on the path where `Start` failed
  and on the path where `Stop` already ran.
- `Add` is refused while started (`LifecycleRunning`, EX_CONFIG): a component
  added mid-flight has a start position it never had and no defensible place in
  the reverse order.
- `Start` and `Stop` are serialised as **whole operations**. A `Stop` arriving
  mid-`Start` would otherwise stop the prefix that is up while `Start` kept
  starting the rest — a torn shutdown no per-field locking prevents.
- A component's `Stop` is required, not optional. "I forgot the teardown" and
  "there is no teardown" are the same `nil`, and only one of them is a defect;
  `func(context.Context) error { return nil }` puts the claim in the diff.
- `TransitionValue.Ended` on an abandoned stop is when the **budget** expired,
  not when the component eventually returned — the engine does not know when
  that was, and a guess would be worse than the fact.

## Breaking changes

None. `lifecycle` is a new domain in this change set; no existing symbol,
shape or behaviour changes. The ADR 0040 v0 licence is not used — said out
loud rather than left silent.

## Why not

- **A dependency DAG with cycle detection and parallel start.** Rejected — D1.
  It re-derives an order the caller already knows, introduces a failure class
  (cycles) that only exists once edges do, and brings a concurrency model
  nobody asked for. A linear order is already topological.
- **Autowiring by reflection.** Rejected — D2. It is a wiring convention, and a
  wiring convention is a framework.
- **One shutdown budget for the whole `Stop`.** Rejected — D5. It is ADR 0043's
  defect with a different noun: the first component that will not finish spends
  everyone's budget, and the rest are severed without being asked.
- **Cancel the caller's context into each `Stop`.** Rejected — D5. The context
  that announces a shutdown is the one that was just cancelled; passing it
  through would hand every component a dead context before it had closed
  anything, which is ADR 0043's rejected option one layer up.
- **Kill or close on budget expiry.** Rejected — D5. Go cannot kill a
  goroutine, and closing what the component owns is ADR 0047 §D9 exactly:
  cleanup that cannot tell what it owns from what it handed away.
- **Refuse a zero `StopTimeout` instead of clamping.** Rejected — D6. It is a
  field on a struct that does other work, not a constructor whose whole content
  is the duration, and 30 s needs no explanation. The refusal side of ADR 0031
  is for values the SDK cannot pick; this is not one.
- **Install signal handling by default.** Rejected — D7. Process-wide side
  effects belong to `main`, not to an import.
- **Wrap the component's error in `StartFailed`.** Rejected — D8. Origin-wins
  would relabel the SDK's verdict with the component's code, so "did startup
  fail?" would stop being answerable.
- **A `Lifecycle` registry.** Rejected — D9. One entry, and a new way to
  misconfigure an order at runtime.

## Deferred

- **A start budget.** `Start` has no SDK-owned deadline: the caller's context
  is the budget. A per-component start budget is defensible and is deliberately
  not shipped, because a start deadline is an operational decision the SDK
  cannot make and a `Start` that ignores its context could not be bounded by
  one anyway. If it lands, it lands as a second field with its own clamp, not
  by reusing `StopTimeout`.
- **Health/readiness beyond sd_notify.** `Run` reports READY=1 once every
  `Start` has returned. "Started" and "ready to serve" are not the same claim,
  and the difference belongs to a health domain, not to an ordering one.
- **Restart and supervision trees.** A component that fails at runtime — as
  opposed to at start — is not this domain's subject. Restart policy is
  `resilience`'s vocabulary, and joining the two would give a startup order a
  runtime failure model it has no way to reason about.
- **Wiring `Run` to the `net` server's drain signal.** `corenet.DrainSignal`
  and a lifecycle shutdown answer the same operator intent from two directions.
  Bridging them is real work with its own ordering question and is recorded
  rather than folded in.

## References

- Impl: `internal/core/lifecycle/{lifecycle.go,lifecycle_component.go,lifecycle_phase.go,lifecycle_transition.go,codes.go,errors.go}`,
  `internal/service/lifecycle/{lifecycle.go,config.go,start.go,stop.go,run.go,codes.go,errors.go}`,
  `pkg/v1/lifecycle/lifecycle.go`
- Guards: `internal/service/lifecycle/{start_external_test.go,stop_external_test.go,run_external_test.go,nosleep_external_test.go}`
- ADR 0043 — the drain that timed out on every deployment (motivation 1)
- ADR 0047 §D9 — the teardown that closed what it had handed away (motivation 2)
- ADR 0031 — the clamp/refuse line applied in D6
- ADR 0039 / ADR 0041 — the FUNC-port shape applied in D3
