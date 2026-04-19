<!-- updated: 2026-04-19T10:18:42Z -->
# pkg/

## Purpose

The SDK's stable public API surface. Each subdirectory is a major version (`v1`, `v2`, …). Consumers import `pkg/<major>/*`; `internal/*` is blocked by Go's internal-import rule.

## Contents

| Major | Purpose | State |
|---|---|---|
| `v1/` | Stable public API for logging + error introspection | Shipping |

## Versioning policy

- `pkg/v1` signatures are **frozen post-v1.0.0**. Any breaking change goes into a new `pkg/v2` (coexists with v1 until deprecation).
- Security fixes in `internal/*` propagate via minor bumps on the module concerned — no `pkg/v1` changes required because it only re-exports.
- Adding a new `pkg/vN` is a dedicated ADR.

## Conventions

- Public types are **type aliases** onto `internal/core/*` / `internal/kernel/*` (clean short names, zero runtime cost).
- Public functions are thin wrappers: validation + delegation. No business logic in this layer.
- **No constructors for internal types**. Consumers can introspect SDK errors via `pkg/v1/errs.*Of(err)` accessors but cannot forge `*errs.Error` values.
- **ldflags injection**: `pkg/v1/logger.Version` is the single injection point; all other packages read via `logger.FrameworkVersion()`.

## Subtree

- `v1/` — see `pkg/v1/CLAUDE.md`

## Do NOT

- Export the concrete `*errs.Error` type here; consumers should see `error` only.
- Import from `pkg/v1` into `internal/*`. The public facade sits at the top of the dependency graph.
- Rename or remove an exported identifier in `pkg/v1/*` without cutting `pkg/v2`.
