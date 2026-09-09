# validation

Package `validation` declares the SDK's value-checking port: `Constraint[T]`,
the `ViolationValue` it reports when a value is not acceptable, the
`ReportValue` that collects them, and the path grammar (`RootPath`,
`JoinField`, `JoinIndex`) that makes a violation say **where** —
`user.addresses[2].zip`.

`Constraint` is a function port rather than an interface, so it cannot grow a
method and break a downstream implementer (ADR 0039).

A violation is a value, not an error, and a report is not an error either: its
zero value is a passing report, and `ReportValue.Err()` converts on demand,
returning a genuine nil when nothing is wrong.

This package is the engine's contract; `internal/core/config.Validator` is the
self-check contract a config struct implements *using* it. The concrete
constraints, the combinators and the struct-tag front end live in
`internal/service/validation`; facade: `pkg/v1/validation`. ADR 0046. See
`CLAUDE.md`.
