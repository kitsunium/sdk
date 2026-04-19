<!-- updated: 2026-04-19T10:18:42Z -->
# internal/core/

## Purpose

Domain **interfaces** and domain **value types**. No concrete behaviour lives here — implementations go in `internal/service/*`, the public facade lives in `pkg/v1/*`. Core's job is to describe "what the SDK's domains are" without prescribing how they are realised.

## Contents

| Package | Purpose | Code range |
|---|---|---|
| `logger/` | `Handler` + `Logger` interfaces, `RecordEvent`, `AttrValue` | 2100-2199 (reserved) |
| `logger/level/` | Severity levels (`Debug` / `Info` / `Warn` / `Error`) | 1100-1199 (reserved) |

## Module

Single module `github.com/kitsunium/sdk/internal/core` — one `go.mod`, one `go.sum`.

## Conventions

- **Interface-first.** Core packages expose interfaces + immutable value types. Methods with bodies belong in `internal/service/*`.
- **Role-suffix on exported structs.** `AttrValue` / `RecordEvent` — the ktn-linter `KTN-STRUCT-ROLE` rule requires it. Short names reappear as type aliases at `pkg/v1/logger` (`Attr = AttrValue`).
- **Imports allowed**: stdlib + `internal/kernel/*`. Never `internal/service/*` or `pkg/*`.

## Design notes (logger)

The `core/logger` package is deliberately tiny:

- `Handler` — 3 methods (`Enabled` / `Handle` / `WithAttrs`), not single-method, to avoid the ktn-linter `-er` naming rule AND because `WithAttrs` is a genuine part of the contract.
- `Logger` — 3 methods (`Log` / `With` / `Enabled`), mirror the structure.
- `RecordEvent` carries a `time.Time` that MAY be the zero value; `service/logger.TextHandler` supplies the fallback via `kernel/clock`.
- Levels live in the `level/` subpackage (not inlined) so they keep their short import alias `level.Debug` and stay isolated from the interface surface.

## Why `level/` is a subpackage here

See `.claude/contexts/sdk-layer-placement-audit.md`. Summary:
- `level` is logger-domain vocabulary; it cannot live in `internal/kernel/`.
- Inlining Level into `internal/core/logger` would clutter the interface package and force a namespace collision (`logger.Level` next to `logger.Logger`).
- Subpackage keeps the `level.Debug` call-site short AND tests isolated.

## Do NOT

- Add concrete types with runtime behaviour here. Any struct whose methods DO anything belongs in `internal/service/*`.
- Import `context` outside of interface signatures.
- Grow a third "level-like" subpackage. If a second domain needs severity, declare its own type in that domain; don't reuse `logger/level`.

## Subtree

- `logger/` — see `internal/core/logger/README.md` (no CLAUDE.md needed: one package, README.md covers the hierarchy)
- `logger/level/` — see `internal/core/logger/level/README.md`

## Verification

```
# Primary (Bazel)
bazel test --config=race //internal/core/...

# Fallback (go test)
cd internal/core
GOWORK=off go test -race -cover ./...
# expected: logger has no tests (interfaces only), logger/level 100%
```
