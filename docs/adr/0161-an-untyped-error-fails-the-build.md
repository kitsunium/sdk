# ADR 0161 — an untyped error fails the build

- **Status**: Accepted
- **Date**: 2026-10-03
- **Deciders**: SDK maintainers
- **Amends**: [ADR 0002](0002-sdk-errors-package.md) (the ban it rests on gains its enforcement), [ADR 0033](0033-consumer-rule-enforcement.md) (SDK002 stays a convention for consumers; the SDK's own tree gets an invariant)
- **Related**: [ADR 0005](0005-sdk-error-codes-dotted-quad.md), [ADR 0019](0019-pkg-errs-public-construction.md) (the ban's premise, restated), [ADR 0020](0020-errs-audit-dual-reason-derivation.md) / [ADR 0035](0035-pp-range-ownership-enforcement.md) (the audits that do exist), [ADR 0147](0147-the-framework-is-a-module-of-the-sdk-above-pkg.md) (the framework's sentinels), [ADR 0154](0154-the-sdks-principles-are-one-charter-and-an-incidents-rule-lives-with-its-code.md) (principles 6 and 26), [ADR 0160](0160-every-service-has-a-core-and-a-code-keeps-its-value-when-it-moves.md) (where codes are declared)

## Context

Rule 2 of the root `CLAUDE.md` reads: "`fmt.Errorf` / `errors.New` are banned
in production code. The AST audit (`//internal/kernel/errs:errs_test`, run as
part of `make test`) fails the build on violations." ADR 0002 rests on that
ban and ADR 0019 restates it as the premise of the public error API.

The audit it describes does not exist. `registry_external_test.go` audits
`errs.Define` call sites — a literal `Public`, a `Reason` derived from the
variable or the code constant, unique values — and `registry_ownership_external_test.go`
audits range ownership; neither looks at any other call. `tools/sdkguard`
detects untyped errors as SDK002, but SDK002 is a **convention** and
`make guard` runs `-level=invariant` over the SDK's tree, deliberately, because
the `errs` package cannot build its own bootstrap failures through itself.

Measured on this tree, production files under `internal/`, `pkg/`,
`framework/` and `third-party/` hold 22 `fmt.Errorf` / `errors.New` calls:

| Where | Calls | What they are |
|---|---|---|
| `internal/kernel/errs/validate.go` | 4 | the bootstrap: `Define`'s own argument validation, which cannot use `Define` |
| `internal/core/{codec,crypto,writer}/registry*.go` | 8 | `fmt.Errorf("%w")` around a duplicate registration, then panicked |
| `internal/service/config/poll_watcher.go` | 2 | local causes carried into a field |
| `framework/internal/kit`, `framework/kit/storetest` | 8 | failures with no code |

Eighteen violations, and nothing failed. A rule the documentation calls
enforced and nothing checks is the case principle 26 of the charter exists
for: worse than an unwritten rule, because a reviewer trusts it.

## Decision

1. **The ban is checked at build time, over source** (ADR 0033's doctrine), in
   a gate the required CI job runs: a `fmt.Errorf` or `errors.New` call in a
   production file under `internal/`, `pkg/`, `framework/` or `third-party/`
   fails it, naming the file and the line.
2. **The check reads every production file of those trees**, not only the
   packages `//:audit_sources` lists — that set is the `Define` emitters, and a
   package that defines nothing can still construct an untyped error.
3. **One file is exempt: `internal/kernel/errs/validate.go`.** A second
   exemption needs an ADR amending this one.
4. **What the `errors` package does besides constructing stays allowed**:
   `errors.Is`, `errors.As`, `errors.AsType`, `errors.Unwrap`, `errors.Join`
   over SDK errors, and `errors.ErrUnsupported`. They inspect or combine typed
   errors; they mint none.
5. **The eighteen sites are converted** before the check is switched on: the
   registries panic with the typed conflict codes they already own
   (`DUPLICATE_REGISTRATION`), the poll watcher's causes become typed, and the
   framework's failures become `0.4.*` sentinels.
6. **The mechanism is either of two shapes, and rule 2 names the one that
   ships**: an AST test beside the registry audit in
   `//internal/kernel/errs:errs_test` (the shape rule 2 already describes), or
   SDK002 run as an invariant over the SDK's own tree with the exemption of §3.
   SDK002 stays a convention for consumers.

## Consequences / Semantics

- **Implemented by the reorganisation series**, in its first step: the
  conversions of §5, then the check. This record changes no code; until the
  check lands, rule 2's sentence about the audit describes this record's
  target, not the tree.
- A new untyped error fails the gate at its line, in the pull request that
  writes it.
- The framework is held to the same rule as the SDK, which ADR 0147 §6 already
  required in words ("the framework's own failures are `errs` sentinels").

## Breaking changes

None in the published surface. The registries' panics carry a typed code where
they carried a wrapped string; a program that recovered one and compared its
text was relying on a message, which `errs` never promised.

## Alternatives considered

- **Correct rule 2 to say the ban is a convention.** It would make the
  documentation true and the rule optional, and the eighteen sites show what
  an optional rule becomes.
- **An allowlist per site.** Every exception would be argued once and kept
  forever; the bootstrap is the only call that cannot be typed.
- **A `golangci-lint` rule.** CI does not run `golangci-lint` (ADR 0004), so it
  would be a suggestion again.

## Deferred

None.

## References

- `internal/kernel/errs/registry_external_test.go`,
  `internal/kernel/errs/registry_ownership_external_test.go` — the two audits
  that exist.
- `tools/sdkguard/rules.go` (SDK002, `LevelConvention`), the `guard` target of
  the `Makefile` (`-level=invariant`).
