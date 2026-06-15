# ADR 0020 — errs audit accepts two Reason derivations (var-name OR Code-const); close the coverage gap

**Status**: Accepted
**Date**: 2026-06-15
**Deciders**: kitsunium maintainers
**Supersedes**: —
**Amends**: ADR 0005 §Semantics (the `reason == screamingSnake(varName)` audit rule), ADR 0006 (formalises the namespaced-reason style it introduced)
**Related**: ADR 0004 (the audit runs as a Bazel test over the `//:audit_sources` filegroup)

## Context

Every SDK error sentinel is `var X = errs.Define(CodeX, "REASON", public, private)`. The
AST audit (`internal/kernel/errs/registry_external_test.go`, `//internal/kernel/errs:errs_test`)
enforces three invariants across the tree: every Define `public` is a string literal,
every Code is unique, and `reason == screamingSnake(varName)`.

Two reason-naming styles exist in the codebase, and **no single audit rule satisfied both**:

| Style | Example | Reason mirrors… |
|---|---|---|
| **Bare** | `MarshalFailed = errs.Define(CodeJSONMarshalFailed, "MARSHAL_FAILED", …)` | the **var** (`MarshalFailed`→`MARSHAL_FAILED`); the package prefix lives only on the Code const |
| **Namespaced** (ADR 0006) | `Full = errs.Define(CodeRingFull, "RING_FULL", …)` | the **Code const** minus `Code` (`CodeRingFull`→`RING_FULL`); the short var `Full` does **not** |

`reason == screamingSnake(varName)` passes the bare style and **fails** the namespaced one
(`Full`→`FULL` ≠ `RING_FULL`). Because the audit under Bazel only sees files shipped via
the `//:audit_sources` filegroup, the namespaced packages were simply **kept out** of that
list to dodge the failure — so ~10 code-emitting packages were **never audited** for code
uniqueness, literal publics, or reason consistency:

- `internal/kernel/ring`
- every logger middleware: `async`, `multi`, `sample`, `recover`, `failover`, `route`
- every logger sink: `file`, `syslog`, `console`

The gap was load-bearing, not a trivial omission — adding any of them to `audit_sources`
under the old rule failed the build (`var "Full" reason "RING_FULL", want "FULL"`). This is
issue #35.

Namespaced reasons are *deliberate* (ADR 0006): `RING_FULL` is globally unique and greppable,
whereas bare `MARSHAL_FAILED` is shared by json/xml/toml/cbor/… and only the Code
disambiguates. So the resolution is not to abolish one style but to **audit both**.

## Decision

The `reason` invariant becomes a disjunction — a Reason is valid when it equals **either**
derivation:

1. `screamingSnake(varName)` — the bare style, unchanged; or
2. `screamingSnake(CodeConstIdent − "Code")` — the namespaced style, where the Code constant
   is the Define call's first argument when it is a bare identifier (`CodeRingFull` →
   `RingFull` → `RING_FULL`). A non-identifier first argument (hex literal, selector) offers
   no derivable name, so only rule 1 applies to it.

All ten namespaced packages gain an `audit_srcs` filegroup and are added to
`//:audit_sources`, bringing the full SDK under the uniqueness / literal-public / reason
audit. With the disjunction in place the expanded set passes green.

## Consequences

- **Coverage is now complete.** Every `errs.Define` site in `internal/`, `pkg/`, and
  `third-party/` is audited for code uniqueness, literal publics, and a consistent reason —
  no package can hide a colliding code or a `fmt.Sprintf` public by staying off the list.
- **No public-contract churn.** Both reason styles keep their existing strings; the
  `[code REASON]` log line and the log-parser regex are unchanged. (The alternative —
  renaming ~40 namespaced reasons to bare form — would have broken the log contract.)
- **The rule is intentionally permissive by one axis.** A sentinel whose reason matches the
  *other* package's convention is accepted as long as it matches one derivation; this is the
  price of supporting both styles and is bounded by the still-strict uniqueness +
  literal-public checks. The helper returns `""` for non-identifier code args so the
  permissive branch never silently swallows a literal-coded sentinel.
- **CLAUDE.md rule 3 and the `internal/kernel/errs` audit doc** are updated to state both
  accepted derivations, so contributors know either style passes.

## Alternatives considered

- **Conform every reason to the bare style** (`RING_FULL`→`FULL`, ~40 edits). Rejected: it
  contradicts ADR 0006's deliberate global-uniqueness namespacing and changes the public
  `[code REASON]` log contract + the documented log-parser regex.
- **Drop the reason==name invariant entirely**, audit only uniqueness + literal publics.
  Rejected: the reason↔name tie is what stops a copy-paste sentinel from drifting (reason
  says one thing, var another); the disjunction keeps that guard for both styles.
- **Two separate audit lists / two test functions.** Rejected as redundant — one disjunction
  in one test covers both styles with less surface.
