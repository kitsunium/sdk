# internal/core/scheduler/

## Purpose

Declares the **time-driven execution port**: `Job` (what runs), `Schedule`
(when), and `Scheduler` (the engine that owns the pairing and fires it), plus
the `EntryValue` / `ResultValue` domain values and the typed sentinels. The
12th core sibling, admitted by **ADR 0041**. The cron parser and the engine are
concrete and live in `internal/service/scheduler`.

Code range: `0.2.12.*` (ADR 0041).

## Contents

| File | Surface |
|---|---|
| `scheduler.go` | `Job func(ctx) error`, `Schedule func(after time.Time) (time.Time, bool)`, `Scheduler interface { Add(EntryValue) error; Run(ctx) error }` |
| `scheduler_entry.go` | `EntryValue` — `Name` / `Schedule` / `Job` / `AllowOverlap` |
| `scheduler_result.go` | `ResultValue` — `Name` / `Scheduled` / `Started` / `Finished` / `Missed` / `Skipped` / `Err` |
| `codes.go` | `Code*` constants — range 0.2.12.* |
| `errors.go` | `InvalidEntry` / `DuplicateJob` / `SchedulerRunning` / `JobPanicked` (`errs.Define`) |

## Conventions

- **No registry** — one canonical engine and one canonical cron dialect, so a
  registry would be over-abstraction (`proc` ADR 0016, `resilience` ADR 0026).
- **Both ports are FUNC types, not interfaces.** The shape
  `internal/core/CLAUDE.md` already admits for `resilience.Operation`. It is the
  narrowest thing `pkg/v1` can publish, and ADR 0039's rule is satisfied
  structurally: a published interface must not grow a method, and a func type
  **cannot**. `TestPortsAreFunctionsNotInterfaces` is the executable guard —
  turning either into an interface fails it at compile time.
- **`Schedule` MUST be pure and strictly increasing.** The same `after` always
  yields the same answer, and the answer is always strictly after the argument.
  The engine calls it repeatedly, several times in one pass when fires were
  missed, and a non-advancing `Schedule` would spin that walk forever — which
  the engine defends against by disarming the entry, but the contract is here.
- **Cron vocabulary does not appear here.** The port knows about instants. The
  parser's own failure modes own `0.3.43.*` in `service/scheduler`, so this
  package never has to know what an expression is.
- **`ResultValue` reports decisions, not just runs.** A skipped fire produces
  one too, with zero `Started`/`Finished`. A scheduler that reported only runs
  would make a permanently-skipping entry look like a healthy one — and, in the
  test suite, would leave "this fire did not run" provable only by sleeping.
- Registration refusals carry `EX_CONFIG` (78): the same `Add` will be refused
  identically forever, so they are permanent, not transient. `JobPanicked`
  keeps the default `EX_SOFTWARE` (70) — it is a fault in the job, not in the
  registration.

## Do NOT

- **Add a method to `Scheduler`, or turn `Job`/`Schedule` into interfaces.**
  `pkg/v1/scheduler` aliases all three, so the shape is published (ADR 0039).
- **Put cron — or any dialect — here.** Expressions are a service concern.
- **Relabel a job's error.** `ResultValue.Err` carries the job's own error
  verbatim so the caller's `errors.Is` keeps working; only a *panic* becomes a
  typed sentinel.

## Verification

```
bazel test --config=race //internal/core/scheduler:scheduler_test
# OR
cd internal/core && GOWORK=off go test -race ./scheduler
```
