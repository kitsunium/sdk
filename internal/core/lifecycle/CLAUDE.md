# internal/core/lifecycle/

## Purpose

Declares the **ordered start/stop port**: `Start` (bring a component up),
`Stop` (take it down), and `Lifecycle` (the engine that owns an ordered set of
them and drives it in both directions), plus the `ComponentValue` /
`TransitionValue` domain values, the two-valued `Phase`, and the typed
registration sentinels. Admitted by **ADR 0050**. The engine, the shutdown
budget and the opt-in signal / sd_notify wiring are concrete and live in
`internal/service/lifecycle`.

Code range: `0.2.19.*` (ADR 0050).

## Contents

| File | Surface |
|---|---|
| `lifecycle.go` | `Start func(ctx) error`, `Stop func(ctx) error`, `Lifecycle interface { Add(ComponentValue) error; Start(ctx) error; Stop(ctx) error }` |
| `lifecycle_component.go` | `ComponentValue` — `Name` / `Start` / `Stop` |
| `lifecycle_transition.go` | `TransitionValue` — `Name` / `Phase` / `Begun` / `Ended` / `TimedOut` / `Err` |
| `lifecycle_phase.go` | `Phase` + `PhaseStart` / `PhaseStop` + `String()` |
| `codes.go` | `Code*` constants — range 0.2.19.* |
| `errors.go` | `InvalidComponent` / `DuplicateComponent` / `LifecycleRunning` / `ComponentPanicked` (`errs.Define`) |

## Conventions

- **No registry** — one ordering discipline and one engine, so a registry would
  have exactly one entry and would add a way to misconfigure a startup order at
  runtime (`proc` ADR 0016, `resilience` ADR 0026, `scheduler` ADR 0041).
- **Both halves of a component are FUNC types, not interfaces.** The shape
  `internal/core/CLAUDE.md` already admits for `resilience.Operation` and
  `scheduler.Job`. It is the narrowest thing `pkg/v1` can publish, and ADR
  0039's rule is satisfied structurally: a published interface must not grow a
  method, and a func type **cannot**. `TestPortsAreFunctionsNotInterfaces` is
  the executable guard.
- **The Add order IS the dependency order.** No `DependsOn`, no edges, no cycle
  detection: a linear sequence already is a topological order, and the caller
  who wrote the constructors is the only party who knows it. The cost is stated
  in ADR 0050 §D1 — the SDK cannot detect a wrong order, because it has nothing
  to check it against.
- **`Stop` is called only for a component whose `Start` returned nil**, and
  only once. That is what lets a `Stop` assume its own `Start` succeeded, which
  is what makes a double-close impossible on the failure path. A `Start` that
  fails owns what it acquired, exactly as a Go constructor does.
- **Both halves are REQUIRED.** A nil `Stop` is refused at `Add`: "I forgot the
  teardown" and "there is no teardown" are the same nil, and only one of them
  is a defect.
- **`TransitionValue` reports decisions, not just successes.** An abandoned
  stop produces one, with `TimedOut` set — a hook that only saw the calls that
  returned would make a component which never stops look exactly like one that
  stops instantly.
- Registration refusals carry `EX_CONFIG` (78): the same `Add` will be refused
  identically forever. `ComponentPanicked` keeps the default `EX_SOFTWARE`
  (70) — it is a fault in the component, not in the registration.

## Do NOT

- **Add a method to `Lifecycle`, or turn `Start`/`Stop` into interfaces.**
  `pkg/v1/lifecycle` aliases all three, so the shape is published (ADR 0039).
  A new capability gets a sibling.
- **Put a dependency graph here** — or anywhere. ADR 0050 §D1 is a decision,
  not an omission.
- **Relabel a component's error.** `TransitionValue.Err` carries it verbatim so
  the caller's `errors.Is` keeps working; only a *panic* becomes a typed
  sentinel.
- **Import `clock` here.** Budgets are the engine's concern; this package knows
  about instants only because a transition reports two of them.

## Verification

```
bazel test --config=race //internal/core/lifecycle:lifecycle_test
# OR
cd internal/core && GOWORK=off go test -race ./lifecycle
```
