<!-- updated: 2026-04-19T10:18:42Z -->
# internal/kernel/

## Purpose

The SDK's lowest layer: **stdlib-only AND generic** primitives. A package qualifies for `kernel/` only if both halves are true — it imports nothing outside the Go standard library AND its vocabulary is domain-neutral enough that any future domain could plausibly import it.

## Contents

| Package | Purpose | Code range |
|---|---|---|
| `errs/` | SDK-wide typed error + registry audit | 1000-1099 (meta-codes, documentary only) |
| `buffer/` | `sync.Pool` of `[]byte` for zero-alloc formatting | 1200-1299 (reserved) |
| `clock/` | Time abstraction (`Now` + `Since`) for testability | 1300-1399 (reserved) |

The README of each package documents its surface, contract, and the explicit "Do NOT" list.

## Module

Single module `github.com/kitsunium/sdk/internal/kernel` — one `go.mod`, one `go.sum`, shared test harness. Each subdirectory is a Go package (not a sub-module).

## Why NO `level/` here?

`level` used to live at `internal/kernel/level/` but was moved to `internal/core/logger/level/` on 2026-04-19. Rationale: `Debug / Info / Warn / Error` is logger-domain vocabulary. No non-logger domain will ever import it, so it fails the "generic" half of the kernel rule even though it's stdlib-only. The audit that led to the move is documented in `.claude/contexts/sdk-layer-placement-audit.md`.

Lesson: stdlib-only is necessary but NOT sufficient for kernel placement. When considering a new kernel package, ask "would a future HTTP middleware, metrics writer, or cache reach for this?". If no, it belongs in `core/<domain>/`.

## Audit: who stays, who would leave

As of 2026-04-19 audit (context doc above):

- `errs` stays — every layer of the SDK uses typed errors, including kernel itself. Meta-infrastructure.
- `clock` stays — textbook generic time abstraction.
- `buffer` stays — byte-pool scratchpad is generic even though only `service/logger` uses it today (HTTP body encoder, JSON serialiser, metrics line-protocol would all reach for it).

## Conventions unique to kernel

1. **Import-only-stdlib.** No package outside of `kernel/errs` may use `fmt.Errorf`; kernel packages themselves may use `fmt` for formatting since they define the error type.
2. **`errs` is the only kernel package that may use `errs` itself** (circularly, for its meta-codes 1001-1003 — those codes are documentary, never instantiated as `*Error`).
3. **No domain vocabulary in public APIs.** No `User`, `Request`, `Log`, `Entity` in type names.

## Do NOT

- Add a package here whose only consumer is a specific domain (logger, HTTP, DB). Put it under `internal/core/<domain>/` as a subpackage.
- Import anything from `internal/core/*` or `internal/service/*`; kernel sits at the bottom.
- Rely on `kernel/` package imports for cross-package coupling — keep each package self-contained.

## Verification

```
cd internal/kernel
GOWORK=off go test -race -cover ./...
# expected: buffer 90%, clock 100%, errs 97.3%
```
