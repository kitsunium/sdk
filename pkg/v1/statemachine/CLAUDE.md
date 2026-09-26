# pkg/v1/statemachine/

## Purpose

Public facade over `internal/core/statemachine` and
`internal/service/statemachine` (ADR 0120): declare a state machine over your
entities, hand it your store, and it moves them — on the events you fire, and
by itself on timers, deadlines and guards, from a loop that sleeps until the
next transition due.

## Surface

| Symbol | Kind | Notes |
|---|---|---|
| `Define(state)` | func | a `*Definition` over entities whose state `state` points at |
| `New(ctx, def, cfg)` | func | a `*Machine`; checks every problem at once, reads the journal and the store once |
| `ParseTrigger(name)` | func | a trigger from its name |
| `Definition` | alias | `= svc.MachineSpec` — `Initial`, `On`, `After`, `At`, `When`, `OnEnter`, `OnTransition`, `Problems`, `States`, `Transitions`, `InitialState`, `Can` |
| `Machine` | alias | `= svc.StateMachine` — `Start`, `Fire`, `Changed`, `Deleted`, `Run`, `Step`, `Census`, `Record`, `Records` |
| `Config` | alias | `Store` (required), `Journal`, `Clock`, `Actor`, `Observe`, `Report`, `OnLoop`, `MinGap`, `Backoff`, `MaxHistory` — passed by pointer |
| `Store`, `Journal` | alias | the two ports, frozen at five and three methods (core) |
| `Record`, `Step`, `Trigger` + `Trigger*` | alias / const | what the machine keeps per entity (core) |
| `Transition`, `Change`, `Firing` | alias | a declared transition, a stored one, one the loop is about to fire |
| `Wake` + `Wake*`, `LoopEvent`, `LoopEventKind` + `Loop*` | alias / const | what `Config.OnLoop` is told |
| `CreateEvent`, `DefaultMinGap`, `DefaultMaxHistory` | const | |
| `Code*` (22) and the sentinels | const / var | `0.2.56.1`, `0.3.88.1`–`0.3.88.21` |

## Why-this-shape

- **The names differ from the engine's** — `Definition` and `Machine` alias
  `MachineSpec` and `StateMachine` — because the linter's role rule cannot see
  a two-parameter generic's constructor; the public names are the ones a reader
  expects (`secret.Policy = PolicySpec` is the precedent).
- **`Config` is a pointer.** It is 128 bytes; a nil one reads as the zero
  configuration, which names no store and is refused with `StoreMissing`.
- **The package doc says what it does NOT do** — no replay, no durable
  workflow runtime, and a write that bypasses the machine can race with a
  transition — because a reader who assumes otherwise builds the wrong thing.
- **Method references are plain text in the doc.** gomarkdoc cannot resolve a
  method of a generic alias, and a bracketed link would render as brackets.

## README is generated

`README.md` is produced by `gomarkdoc` from the package doc comment in
`statemachine.go` (ADR 0008). Regenerate with `make docs-readme`; do not
hand-edit it.

## Verification

```sh
bazel test --config=race //pkg/v1/statemachine:statemachine_test
cd pkg && GOWORK=off go test -race ./v1/statemachine/
```
