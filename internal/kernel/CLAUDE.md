<!-- updated: 2026-05-18T14:30:00Z -->
# internal/kernel/

## Purpose

The SDK's lowest layer: **stdlib-only AND generic** primitives. A package qualifies for `kernel/` only if both halves are true — it imports nothing outside the Go standard library AND its vocabulary is domain-neutral enough that any future domain could plausibly import it. The 2026-04-19 layer audit (`.claude/contexts/sdk-layer-placement-audit.md`) is the canonical reference; it documents why `level` was relocated OUT and which packages remain.

## Contents

| Package | Purpose | Code range |
|---|---|---|
| `errs/` | SDK-wide typed error + dotted-quad registry (ADR 0005) | 1000-1099 (meta-codes, documentary only) |
| `buffer/` | `sync.Pool` of `[]byte` + generic `Recycler[T]` for zero-alloc reuse | 1200-1299 (reserved) |
| `clock/` | `Clock` interface (`Now` + `Since`) for testable time | 1300-1399 (reserved) |
| `ring/` | SPSC lock-free bounded queue (ADR 0006) | 1400-1499 (RING_FULL / RING_EMPTY / RING_CAP_ZERO emit today) |

Each package owns a sibling `CLAUDE.md` documenting its surface and contract.

## Module

Single module `github.com/kitsunium/sdk/internal/kernel` — one `go.mod`, one `go.sum`, shared test harness. Each subdirectory is a Go package (not a sub-module). Build with `cd internal/kernel && GOWORK=off go build ./...`; CI uses Bazel (`bazel test --config=race //internal/kernel/...`).

## Why NO `level/` here?

`level` used to live at `internal/kernel/level/` but was moved to `internal/core/logger/level/` on 2026-04-19. `Debug / Info / Warn / Error` is logger-domain vocabulary — no non-logger domain will ever import it, so it fails the "generic" half of the kernel rule even though it's stdlib-only. See `.claude/contexts/sdk-layer-placement-audit.md`.

Lesson: stdlib-only is necessary but NOT sufficient. When considering a new kernel package, ask "would a future HTTP middleware, metrics writer, or cache reach for this?". If no, it belongs in `core/<domain>/`.

## Audit: who stays, who would leave

As of the 2026-04-19 audit (extended by ADR 0006 to admit `ring`):

- `errs` stays — every layer of the SDK uses typed errors, including kernel itself. Meta-infrastructure.
- `clock` stays — textbook generic time abstraction.
- `buffer` stays — byte-pool + `Recycler[T]` are generic even though `service/logger` is the heaviest consumer today (any future codec stream, HTTP body encoder, or metrics line writer can reuse them).
- `ring` admitted by ADR 0006 — SPSC bounded queue is a textbook generic primitive. Logger's async middleware is the only consumer today; metrics batchers and codec stream pipelines are obvious future users.

## Conventions unique to kernel

1. **Import-only-stdlib.** Kernel packages MAY import each other (e.g. `ring` imports `errs` for its sentinels) but MUST NOT reach into `core/*` or `service/*`. No package outside of `errs` may use `fmt.Errorf` / `errors.New` in production code.
2. **`errs` self-reference.** The `errs` package uses its own meta-codes 0.0.0.1..6 as documentary identifiers for Define-time validation failures — they are never instantiated as `*Error` sentinels.
3. **No domain vocabulary in public APIs.** No `User`, `Request`, `Log`, `Entity` in type names. `Queue[T]`, `Recycler[T]`, `Clock`, `Error` all pass.
4. **IFACE-PLUGIN marker.** Constructors returning an interface backed by an unexported struct (`buffer.NewRecycler`, `ring.New`) tag their interface with the `// IFACE-PLUGIN:` comment so ktn-linter recognises the swap-able-implementation pattern.

## Do NOT

- Add a package here whose only consumer is a specific domain (logger, HTTP, DB). Put it under `internal/core/<domain>/` as a subpackage.
- Import anything from `internal/core/*` or `internal/service/*`; kernel sits at the bottom.
- Replace package-level singletons (`clock.System`, `errs` sentinels) in tests — inject a fake at the call site instead.

## Verification

```
# Primary (Bazel — CI source of truth)
bazel test --config=race //internal/kernel/...

# Fallback (go test — quick local iteration)
cd internal/kernel
GOWORK=off go test -race -cover ./...
# expected: buffer 90%, clock 100%, errs 97.3%, ring 90%
```

## Subtree

- `errs/` — see `internal/kernel/errs/CLAUDE.md`
- `buffer/` — see `internal/kernel/buffer/CLAUDE.md`
- `clock/` — see `internal/kernel/clock/CLAUDE.md`
- `ring/` — see `internal/kernel/ring/CLAUDE.md`
