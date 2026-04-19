<!-- updated: 2026-04-19T10:18:42Z -->
# docs/

## Purpose

Long-form SDK documentation. Everything in here is authoritative — READMEs at the package level are the short-form mirror; ADRs here are the source of truth for cross-cutting decisions.

## Contents

| File | Topic | State |
|---|---|---|
| `adr/0001-sdk-go-multimodule-layout.md` | Multi-module layout (kernel/core/service/pkg-v1) | Accepted |
| `adr/0002-sdk-errors-package.md` | Layered `errs` package, registry, breaking changes | Accepted |

## ADR conventions

- Sequential numbering (`0001`, `0002`, …). Never reuse a number.
- Filename slug = short kebab-case summary of the decision.
- Header fields: `Status`, `Date`, `Deciders`, optional `Supersedes`, optional `Related`.
- Standard sections: Context, Decision, Consequences / Semantics, Breaking changes, Why not …, Deferred, References.
- Immutable after merge. Supersede via a new ADR that references the old one.

## ADR 0002 registry coupling

The code-allocation table in `docs/adr/0002-sdk-errors-package.md` is the **documentary source of truth**; the AST audit test in `internal/kernel/errs/registry_external_test.go` embeds the same table as the **executable source of truth**. Keep the two in sync manually on every change — a future cross-reference test will catch drift.

## Do NOT

- Put feature documentation here. Packages document themselves via `README.md`.
- Delete an accepted ADR. Mark it superseded by adding a new ADR and updating the `Status`.
- Re-number existing ADRs.

## Subtree

- `adr/` — Architecture Decision Records (see files above)
