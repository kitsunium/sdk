# internal/core/validation/

## Purpose

Declares the **value-checking port**: `Constraint[T]` (what checks), plus the
two domain values it produces — `ViolationValue` (one located failure) and
`ReportValue` (all of them) — and the path grammar every violation speaks. The
15th core sibling, admitted by **ADR 0046**. The concrete constraints, the
combinators and the struct-tag front end live in `internal/service/validation`.

Code range: `0.2.15.*` (ADR 0046).

## Contents

| File | Surface |
|---|---|
| `validation.go` | `Constraint[T any] func(path string, value T) ReportValue` |
| `violation.go` | `ViolationValue` — `Path` / `Rule` / `Message` / `Code` |
| `report.go` | `ReportValue []ViolationValue` + `OK` / `First` / `Paths` / `Err` |
| `path.go` | `RootPath`, `JoinField`, `JoinIndex` — the path grammar |
| `codes.go` | `Code*` constants — range 0.2.15.* |
| `errors.go` | `ValidationFailed` / `ConstraintMisconfigured` (`errs.Define`) |

## Conventions

- **No registry.** There is one engine and one rule vocabulary, so a registry
  would be over-abstraction — the `proc` (ADR 0016), `resilience` (ADR 0026),
  `net` (ADR 0029), `scheduler` (ADR 0041) and `token` (ADR 0042) precedents.
- **The port is a FUNC type, not an interface.** ADR 0039's rule is that a
  published port must not grow a method, because `pkg/v1/validation.Constraint`
  aliases it and Go interfaces are structural. A func type satisfies that rule
  *structurally* — it cannot grow one at all. `TestPortIsAFunctionNotAnInterface`
  is the executable guard.
- **A violation says WHERE.** The path grammar is the domain's reason to exist:
  members joined by `.`, elements by `[n]`, so `user.addresses[2].zip`. Build
  child paths with `JoinField` / `JoinIndex`, never by concatenation, or the
  grammar stops being one. `RootPath` (`""`) is the value as a whole — what a
  cross-field rule reports.
- **The member name is the caller's, not the type's.** Core has no opinion; the
  service tag compiler derives it from the `json` tag because `config.Load`
  decodes every format through a JSON round trip, and the programmatic `Field`
  takes it as an argument.
- **A violation is not an error, and a report is not one either.** A validation
  normally produces several violations; `errors.Join` of five renders five
  bracket headers and buries the paths. `ReportValue` deliberately does NOT
  implement `error` — a value type that did would make `if err != nil` true for
  a *passing* validation. `Err()` converts on demand and returns a genuine nil
  interface.
- **`ReportValue` is a slice so its zero value passes.** Nil is an OK report,
  composition is `append`, and the accepting path allocates nothing.
- **`Err()` carries locations, never messages or values.** Fields: `violations`
  (count), `rule` (the first), `paths` (the list, rune-clipped at 240). The
  full report is the report; the error is the interop shape for `error`-typed
  contracts.
- **`ValidationFailed` is HTTP 422, not 500.** A rejected value is not a server
  fault. `ConstraintMisconfigured` carries `EX_CONFIG` (78) — the same
  arguments will be refused identically forever.

## Relationship to `internal/core/config.Validator`

They are different things and both are needed. `config.Validator` is the
CONTRACT (`Validate() error`) a decoded config struct implements so that
`config.Load` can ask it one question. This package is the ENGINE that answers
it. A config struct implements the contract by running its constraints and
returning `ReportValue.Err()` — see `pkg/v1/validation`'s package doc for the
five-line bridge. Neither replaces the other, and this package does not import
`config` (it is a sibling, not a dependency).

## Do NOT

- **Turn `Constraint` into an interface, or add a method to it.**
  `pkg/v1/validation` aliases it, so the shape is published (ADR 0039).
- **Make `ReportValue` implement `error`.** The typed-nil trap is the whole
  reason `Err()` exists.
- **Put a concrete rule here.** `required`, `min`, `oneof` and the tag dialect
  are service concerns; the port knows about paths and violations, not rules.
- **Echo a validated VALUE into a `Message` or a field.** A validation message
  is designed to reach an end user; the value may be a password.

## Verification

```
bazel test --config=race //internal/core/validation:validation_test
# OR
cd internal/core && GOWORK=off go test -race ./validation
```
