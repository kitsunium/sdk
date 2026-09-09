# internal/service/lifecycle/

## Purpose

The concrete `core/lifecycle.Lifecycle`: ordered bring-up, reverse teardown,
the **partial-start unwind**, the **per-component shutdown budget**, and `Run`
— the opt-in bridge to the signal and `sd_notify` machinery `internal/service/
proc` already ships. **ADR 0050.**

Code range: `0.3.49.*` (run outcomes). Registration refusals come from
`core/lifecycle` (`0.2.19.*`).

## Contents

| File | Surface |
|---|---|
| `lifecycle.go` | the unexported engine, `New(Config) core/lifecycle.Lifecycle`, `Add`, the shared state, `guard` (panic recovery) and `emit` |
| `config.go` | `Config` — `Clock` / `StopTimeout` / `OnTransition`; `DefaultStopTimeout` (30s) and the two resolvers that apply the documented fallbacks |
| `start.go` | `Start`, `callStart`, `abort`, `joinStartFailure` — the bring-up sequence and the unwind trigger |
| `stop.go` | `Stop`, `stopEach` (**the single unwind path**), `callStop`, `stopped`, `abandoned` |
| `run.go` | `RunConfig`, `Run`, `waitForStop`, `announce` — opt-in signals + sd_notify |
| `codes.go` / `errors.go` | `StartFailed` / `StopFailed` / `StopTimeout` / `UnwindFailed` / `ReadinessFailed` |

## How the partial-start cleanup is guaranteed

Three mechanics, none of which a hand-rolled `defer` chain has:

1. `markStarted` records a component **the instant its `Start` returns nil,
   before the next `Start` is attempted**. There is no window in which a
   component is up and unrecorded — which is exactly the window a `defer`
   registered after an error return leaves open.
2. `takeStarted` reverses **in one place**. The reverse order is the domain's
   central promise, so there is exactly one line that can get it wrong.
3. `abort` calls `stopEach` — the same function `Stop` calls, over the same
   reversed list, with the same per-component budget. There is no second
   cleanup implementation for the failure path, which is how the two would
   drift until only the exercised one stayed correct.
   `TestTheUnwindAndAnOrdinaryStopProduceTheSameOrder` runs both routes.

The component that FAILED is never stopped, and the unwind's contexts are
derived with `context.WithoutCancel` so a start aborted by a cancelled context
still gets a real cleanup.

## What an expired budget does

Exactly three things, and deliberately nothing more:

- cancels the context handed to that component's `Stop` — an **announcement**,
  the same instrument as ADR 0043's drain signal;
- stops **waiting** and moves to the next component, which gets its own full
  budget;
- collects a typed `StopTimeout` and publishes a `TransitionValue` with
  `TimedOut` set.

It does **not** kill the goroutine (Go cannot) and does **not** close anything
the component owns (ADR 0047 §D9 — cleanup that cannot tell what it owns from
what it handed away destroys live work and reports nothing). `Stop` returns in
at most `n × StopTimeout`; that bound is the price of not letting one component
spend everyone else's budget, and it is stated rather than discovered.

## Conventions

- **Every wait goes through `clock.Timed`, never package `time`.**
  `TestPackageNeverWaitsOnTheWallClock` parses this package's own sources —
  production AND tests — through the AST and fails the build on
  `time.Sleep`/`After`/`AfterFunc`/`Tick`/`NewTimer`/`NewTicker`.
  `TestWallClockAuditDetectsAViolation` proves the audit fires. The go_test
  target therefore carries `data = ["//:audit_sources"]`.
- **The budget timer is armed BEFORE the work starts**, not after the goroutine
  is scheduled, so "when does the budget begin" has an answer.
- **Errors aggregate with `errors.Join`, side by side.** `errs.Wrap` would hit
  origin-wins (rule 6) and relabel the SDK's verdict with the component's own
  code. `errs.HasCode` walks `Unwrap() []error`, so both questions answer.
- **`Start` and `Stop` are serialised as whole operations** by `opMu`. A `Stop`
  arriving mid-`Start` would otherwise stop the prefix that is up while `Start`
  kept starting the rest.
- **`Stop` ignores its context's cancellation.** The context that announces a
  shutdown is the one that was just cancelled.
- **`Run`'s zero `RunConfig` wires nothing.** Signals and sd_notify are
  process-wide side effects and belong to the caller's `main` (ADR 0030's
  posture). An empty `Signals` leaves a nil channel in the select — the right
  absence, as `corenet.DrainSignal` is.

## Test suite

Deterministic end to end: `clock.ManualClock` for every budget, channel
rendezvous for every ordering. `harness_external_test.go` holds the recorder
and the component factories; `blocking` returns an `entered`/`release` pair,
and receiving on `entered` proves the previous component's timer is retired and
the current one's is armed, which is what makes `Advance(budget)` exact.

`run_unix_external_test.go` is `//go:build unix` because raising a signal at
one's own process is (`syscall.Kill` has no Windows equivalent — the same line
`internal/service/proc/signal` draws, ADR 0018). Its compensating coverage:
the wiring still COMPILES everywhere, and `Run`'s context path is covered by
the portable `run_external_test.go`. It arms the SIGUSR1 disposition through
the **stdlib** first, so a raise cannot terminate the binary and so the safety
net is not the mechanism under test.

## Do NOT

- **Write a second cleanup path for the failure case.** Everything goes through
  `stopEach`. That is the whole guarantee.
- **Stop the component whose `Start` failed.** It never handed back a running
  thing; a `Stop` on it is a double close on a half-constructed object.
- **Close, sever or kill anything when a budget expires.** Cancel the context
  and step aside.
- **Sleep.** Not in production, not in a test. The audit will say so.
- **Reimplement signal handling or sd_notify here.** `internal/service/proc/
  {signal,sdnotify}` own those; this file wires them.
- **Read `Stop`'s context for cancellation.** It carries values only.

## Verification

```
bazel test --config=race //internal/service/lifecycle:lifecycle_test
# OR
cd internal/service && GOWORK=off go test -race ./lifecycle
```
