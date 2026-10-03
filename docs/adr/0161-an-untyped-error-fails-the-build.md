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
3. **No file is exempt.** The four `errors.New` values of
   `internal/kernel/errs/validate.go` that the table counts as the bootstrap
   turned out to be dead when they were converted: unexported, named by
   nothing but a `_ = []error{...}` pin, and replaced since ADR 0005 by the
   `*Error` values `newValidationError` builds as a struct literal — which
   needs neither `Define` nor the standard library. They were removed rather
   than exempted, so the check holds every production file to the ban; an
   exemption, should one ever be needed, needs an ADR amending this one.
4. **What the `errors` package does besides constructing stays allowed**:
   `errors.Is`, `errors.As`, `errors.AsType`, `errors.Unwrap`, `errors.Join`
   over SDK errors, and `errors.ErrUnsupported`. They inspect or combine typed
   errors; they mint none.
5. **The eighteen sites are converted** before the check is switched on: the
   registries panic with the typed conflict codes they already own
   (`DUPLICATE_REGISTRATION`), the poll watcher's causes become typed, and the
   framework's failures become `0.4.*` sentinels.
6. **The mechanism is SDK002, run over the SDK's own tree.** Of the two shapes
   considered — an AST test beside the registry audit in
   `//internal/kernel/errs:errs_test`, or `tools/sdkguard`'s SDK002 — the one
   that ships is the second: `make guard`, which `make lint-check` runs (so
   `make lint` and CI's lint gate do), adds a pass `-rules=SDK002` over
   `internal/`, `pkg/`, `third-party/` and `framework/`. Choosing the rule by
   identifier runs it whatever its level, so SDK002 stays a convention for
   consumers and sdkguard's rule table is unchanged. The same target refuses,
   fail-closed, any `//sdkguard:allow SDK002` directive in those trees, which
   pins the exemption list of §3 to empty. Rule 2 of the root `CLAUDE.md`
   names this mechanism.

## Consequences / Semantics

- **Implemented by the reorganisation series**, in its first step: the
  conversions of §5, then the check of §6. The registries of `core/codec`,
  `core/crypto` and `core/writer` wrap a `DUPLICATE_REGISTRATION` sentinel on
  the code each already owned (`0.2.2.1`, `0.2.4.1`, `0.2.3.1`); the poll
  watcher carries its refusals as text through a `withCause` helper;
  `framework/internal/kit` gains six codes in its range (`0.4.2.70`–`0.4.2.75`,
  re-exported by its facade) and `framework/kit/storetest` a range of its own,
  `0.4.4.*`, starting with `0.4.4.1` `ROLLED_BACK`.
- **sdkguard's shadow heuristic was narrowed on the way.** It counted a struct
  field or an interface method named `errors`, `fmt`, `logger`, `os`, `log` or
  `slog` as shadowing the package of that name, which silenced every rule for
  the whole file: a file declaring `type r struct{ errors []error }` let
  `errors.New` through with exit 0. A member is no longer a shadow; this
  narrows ADR 0033 §Deferred's "lexical binding identity" without closing it.
- **What the ban covers is the two constructors.** The framework's sentinels
  are built with `pkg/v1/errs.New` and its product wire error, `kit.Error`, by
  neither `Define` nor `Wrap`; neither is an untyped error, so neither fails
  the gate, and rule 2's "goes through `errs.Define` or `errs.Wrap`" reads as
  "is a typed SDK error" there.
- A new untyped error fails the gate at its line, in the pull request that
  writes it.
- The framework is held to the same rule as the SDK, which ADR 0147 §6 already
  required in words ("the framework's own failures are `errs` sentinels").

## Breaking changes

None in the published surface. The registries' panics carry a typed code where
they carried a wrapped string; a program that recovered one and compared its
text was relying on a message, which `errs` never promised.

For consumers of `tools/sdkguard`: a file holding a struct field or an
interface method named like a guarded package is now analysed, so it may report
findings it used to hide — every one of them a true positive.

## Alternatives considered

- **Correct rule 2 to say the ban is a convention.** It would make the
  documentation true and the rule optional, and the eighteen sites show what
  an optional rule becomes.
- **An allowlist per site.** Every exception would be argued once and kept
  forever; the bootstrap is the only call that cannot be typed.
- **A `golangci-lint` rule.** CI does not run `golangci-lint` (ADR 0004), so it
  would be a suggestion again.

## Deferred

- What an AST rule cannot see: a method value (`f := errors.New`), a call
  through another module's wrapper, and a file that declares a local or a
  parameter named `errors` or `fmt`, which still disables SDK002 for that file
  (no production file does today).

## References

- `internal/kernel/errs/registry_external_test.go`,
  `internal/kernel/errs/registry_ownership_external_test.go` — the two audits
  that exist.
- `tools/sdkguard/rules.go` (SDK002, `LevelConvention`), `tools/sdkguard/analyze.go`
  (`memberFields`), and the `guard` target of the `Makefile` — the invariant
  pass, the `-rules=SDK002` pass and the fail-closed directive scan.
