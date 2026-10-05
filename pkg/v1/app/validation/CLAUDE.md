# pkg/v1/app/validation/

## Purpose

Public facade for the SDK's value-checking domain (ADR 0046). Aliases the
`Constraint` port and the two domain values, and re-exports the constraints,
the combinators, the struct-tag front end, the path grammar and the sentinels.
Stdlib-only → dep-light; cross-OS portable.

## Surface

| Symbol | Notes |
|---|---|
| `Constraint[T]` | alias onto `core/app/validation`. A FUNC type, so it cannot grow a method (ADR 0039) |
| `Violation` / `Report` | short aliases for `ViolationValue` / `ReportValue`; `Report`'s zero value passes |
| `StructConfig` | alias onto `service/app/validation` — `StopAtFirst` |
| `RootPath` / `JoinField` / `JoinIndex` | the path grammar — `user.addresses[2].zip` |
| `All` / `First` / `Check` / `Must` | composition, the `error` bridge, and the `regexp.MustCompile` idiom |
| `Field` / `Each` | reflection-free descent; both refuse a nil accessor |
| `Required` / `AtLeast` / `AtMost` / `Between` | presence and ordered bounds |
| `Length` / `Count` / `Unbounded` | string runes and slice elements |
| `OneOf` / `Matches` | set membership and RE2 pattern |
| `Struct` | the struct-tag front end, plan cached per `(type, mode)` |
| `Failed` / `Misconfigured` | core sentinels (`0.2.15.*`) |
| `InvalidRule` / `UnsupportedTarget` | tag-compiler sentinels (`0.3.47.*`) |
| `CodeRequired` / `CodeOutOfRange` / `CodeLengthOutOfRange` / `CodeNotInSet` / `CodePatternMismatch` | violation identities (`0.3.47.*`) — route with `errs.HasCode` instead of matching `Violation.Rule` |

## Conventions

- **Type aliases, not new types**; every function is a thin delegation.
- **A violation says where.** One grammar, documented in the package doc and
  re-exported as `JoinField` / `JoinIndex`. Member names come from the `json`
  tag when the field has one — because `config.Load` decodes every format
  through a JSON round trip, so the json name is the key the operator wrote.
- **Collect everything by default.** `First` and `StructConfig.StopAtFirst` are
  the opt-in, and both really stop.
- **Neither `Violation` nor `Report` is an `error`.** `Report.Err()` converts on
  demand and returns a genuine nil interface when nothing is wrong; that error
  carries the count and the paths, never the messages and never the values.
- **No constraint is legitimate; a broken constraint is not.** A type with no
  `validate` tag compiles and passes. An inverted interval, an empty `OneOf`
  set, an uncompilable pattern and an unhonourable tag are refused at
  construction (ADR 0031).
- **This package feeds `config.Validator`, it does not replace it.** The
  five-line bridge is in the package doc and is covered by
  `TestConfigLoadSurfacesAValidationFailure`.
- README written by `tools/genindex` from `docs/api` (ADR 0167); the package
  comment is in `doc.go`, which kit writes from `design/app/validation.yaml`: edit the
  design, run `kit gen`, then `make api` and `make docs-readme`.

## Do NOT

- Reimplement a constraint here — the facade is aliases + delegations.
- Use `Must` on a bound that comes from configuration. There the refusal is a
  real runtime condition and must be returned; `Must` is for source literals at
  package initialisation.
- Expect a `pattern` rule in a struct tag. The comma separates rules, and a
  silently truncated regexp is the failure the domain exists to prevent —
  compose `Matches` in code and hand it to `All` alongside the tag validator.

## Generated

`facade_gen.go` is kit's (ADR 0166): every alias, re-exported constant and variable, and forwarder of this package is declared in the `facade:` of `design/app/validation.yaml`, doc comments included, and kit writes it. Where this file names another file as holding one of them, read `facade_gen.go`: the hand-written files keep the declarations of their own, and `doc.go` — kit's too (ADR 0167) — the package comment. Change a re-export, or its doc comment, in the design and run `kit gen` (then `make api` and `make docs-readme`); `make api-check` fails on a `facade_gen.go` edited by hand.

## Verification

```
bazel test --config=race //pkg/v1/app/validation:validation_test
# OR
cd pkg && GOWORK=off go test -race ./v1/app/validation
```
