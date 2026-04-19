# ADR 0001 — SDK Go Multi-Module Layout

**Status**: Accepted
**Date**: 2026-04-19
**Deciders**: @kodflow

## Context

We are bootstrapping a Go SDK (`github.com/kitsunium/sdk`) that must
provide normed, high-performance utilities. Requirements:

- Support multiple public major versions in parallel (`pkg/v1`, later
  `pkg/v2`, …) without breaking downstream consumers.
- Evolve internal layers for security patches and refactors **without**
  forcing a major bump of the public API.
- Keep the codebase approachable and lint-clean under the repo's
  strict ktn-linter conventions.

## Decision

Adopt a 4-layer architecture composed of 5 independent Go modules held
together by a `go.work` file.

```
github.com/kitsunium/sdk                            (umbrella, empty module root)
├── internal/kernel    — stdlib-only primitives (level, buffer, clock)
├── internal/core      — interfaces over kernel  (Handler, Logger, types)
├── internal/service   — concrete implementations (TextHandler, loggerImpl)
└── pkg/v1             — stable public façade (aliases + constructors)
```

Each directory carries its own `go.mod`. Cross-module dependencies are
declared as `require` pseudo-versions and resolved locally via:

- `go.work` for developer workflow (`go build ./...` from the repo root).
- `replace` directives inside each `go.mod` so CI can run tests with
  `GOWORK=off` and still compile.

Module tag format follows Go's standard: `<module-path>/vX.Y.Z`, e.g.
`internal/kernel/v0.1.0`, `pkg/v1/v1.0.0`.

### Enforcement

- `internal/` path component leverages Go's compile-time firewall to
  block external consumers from importing internal layers directly.
- `depguard` (golangci-lint) enforces inter-layer dependencies inside
  the repo: kernel ⊂ stdlib only, core ⊂ stdlib + kernel, service ⊂
  stdlib + kernel + core, pkg ⊂ stdlib + kernel + core + service.
- CI runs each module with `GOWORK=off` to validate that every module
  is buildable as an external consumer would see it.
- ktn-linter (148 rules across 8 phases) governs naming, comments,
  test layout, and modern Go idioms.

## Consequences

### Positive

- Internal layers can ship breaking changes with major bumps on the
  internal modules without touching `pkg/v1`'s public contract.
- Security fixes in `internal/*` propagate via minor bumps and
  independent `go.mod` updates in the consuming modules.
- `go.work` simplifies local development — no manual replace gymnastics
  when hacking across layers.
- `pkg/v1` stays small: it is re-exports + convenience constructors.
- Each module has its own test cycle and coverage report, making CI
  parallelism cheap.

### Negative

- Five `go.mod` files to keep tidy; mitigated by `make sdk-tidy` and
  `make sdk-sync` targets.
- Release ritual is non-trivial: leaf modules are tagged first, consumers
  bump their requires, then upward-pointing modules are tagged. A
  future `sdk-release-check` helper documents the order.
- `replace` directives + `go.work` both live in the repo; developers
  need to know which is authoritative (go.work wins while present).

## Alternatives considered

- **Single module.** Rejected: every internal rename becomes a public
  breaking change, and we lose the ability to bump internal majors
  independently.
- **Multi-module without `internal/`.** Rejected: there would be no
  compile-time firewall preventing downstream consumers from importing
  implementation details.
- **goreleaser Pro for monorepo tagging.** Deferred: manual tagging is
  acceptable for now; revisit when the release cadence accelerates.

## References

- Context research: `.claude/contexts/go-sdk-multimodule.md`
- Execution plan: `.claude/plans/sdk-go-multimodule-scaffolding.md`
- Go reference: <https://go.dev/ref/mod> § Workspaces and Module directories
- ktn-linter conventions: `.devcontainer/images/.claude/docs/ktn-linter-integration.md`
