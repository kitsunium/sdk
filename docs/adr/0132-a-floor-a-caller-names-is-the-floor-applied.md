# ADR 0132 — a floor a caller names is the floor applied

- **Status**: Accepted
- **Date**: 2026-09-26
- **Deciders**: SDK maintainers
- **Related**: [ADR 0012](0012-logger-writer-registry.md) (the writer configuration and its `MinLevel`), [ADR 0039](0039-extending-a-published-port-without-breaking-it.md) (the frozen `Sink` port)

## Context

One logger, two destinations at two floors: a framework writes to the
terminal at the application's level and, in the same pipeline, hands every
record — Debug included — to an in-process viewer. `SinkConfig.MinLevel` is the
floor of the whole pipeline, so the pipeline runs at Debug and the terminal's
branch needs a floor of its own. The framework wrote that gate itself: a
`Sink` embedding the wrapped sink, dropping records below its level.

The SDK already has the gate — `internal/service/writer/levelgate`, a Sink
decorator whose drop is a successful no-op and costs no allocation — but its
constructor carries the writer configuration's reading of `Info`: a writer
config's zero `MinLevel` means "inherit the handler's level", so
`levelgate.New(sink, Info)` returns the sink UNWRAPPED. Measured through the
facade with that constructor: a branch gated at Info received
`[debug info warn error]`. Info is the floor an application's terminal most
often has.

## Decision

- `levelgate.Floor(inner, min)` — the same `gateSink`, without the inherit
  reading: the floor it is given is the floor it applies, Info included. A nil
  `inner` yields nil, which the fan-out skips and `NewWithSink` refuses,
  rather than a gate that fails on its first record. `New` keeps its reading,
  which a writer configuration's zero value depends on.
- `logger.LevelGate(sink, min) Sink` in `pkg/v1/logger`, beside `Multi`, over
  `Floor`. A drop reports every byte accepted and no error, so a fan-out never
  counts it as a failed write; `Flush` and `Close` reach the sink unchanged.

The floor is fixed. `levelgate`'s own record measured a `Leveler`-driven floor
at 1.19–1.23× per record through the port for a capability nobody asked for;
a caller who changes its floor builds the logger again.

## Consequences

- The framework's `levelGate` type goes; its tee is
  `logger.Multi(logger.LevelGate(terminal, level), viewer)`.
- The writer factories are unchanged.

## Breaking changes

None. `Floor` and `LevelGate` are new; `New` is unchanged.

## Alternatives considered

- **Change `New` to gate at Info.** Every writer configuration's zero
  `MinLevel` would start dropping Debug records the handler lets through — a
  silent change to what an existing configuration means.
- **A floor option on `Multi`.** `Multi` is a fan-out; a floor per branch is a
  property of the branch, and composing the two keeps each one thing.
- **`route` middleware.** It answers a record no route takes with `NoMatch`, an
  error a fan-out aggregates as a failure.

## Deferred

None.

## Verification

- `internal/service/writer/levelgate/gate_external_test.go` —
  `TestFloorAppliesEveryFloorInfoIncluded`, whose first row is what `New` gets
  wrong; nil in, nil out; `Flush` and `Close` delegated.
- `pkg/v1/logger/levelgate_external_test.go` —
  `TestLevelGateTeesOneLoggerAtTwoFloors`, the framework's own composition,
  confirmed to fail with `New` in place of `Floor`; the drop reports success; a
  nil gate refused downstream.
