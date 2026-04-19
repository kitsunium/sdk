<!-- updated: 2026-04-19T10:18:42Z -->
# internal/

## Purpose

The SDK's private layer. Everything here is blocked from external import by Go's `internal/` rule. Three sublayers model the SDK's dependency discipline:

```
kernel/    stdlib-only, generic primitives (no domain vocabulary)
core/      domain interfaces and domain values
service/   concrete implementations of core contracts
```

## Dependency direction

Strictly top-down:

```
kernel ──┐
core  ──┼──▶ service ──▶ (pkg/v1 re-exports / consumes)
         │
         └── pkg/v1 (direct for value types like Attr / Level)
```

`.golangci.yml` encodes this via `depguard`:

| Files match | Allow-list |
|---|---|
| `**/internal/kernel/**/*.go` | stdlib only |
| `**/internal/core/**/*.go`  | stdlib + `…/internal/kernel` |
| `**/internal/service/**/*.go` | stdlib + kernel + core |
| `**/pkg/**/*.go` | stdlib + kernel + core + service |

Any import going "upward" fails the lint.

## Modules

Each layer directory is its own Go module (for release independence):

| Module path | Build with |
|---|---|
| `github.com/kitsunium/sdk/internal/kernel` | `cd internal/kernel && GOWORK=off go build ./...` |
| `github.com/kitsunium/sdk/internal/core` | `cd internal/core && GOWORK=off go build ./...` |
| `github.com/kitsunium/sdk/internal/service` | `cd internal/service && GOWORK=off go build ./...` |

`replace` directives in each `go.mod` resolve intra-repo dependencies without published pseudo-versions. `go.work` at the repo root lets `go build ./...` from the SDK root work without `replace`.

## Conventions

- **Role-suffix types.** Exported structs take a role suffix (`AttrValue`, `RecordEvent`, `WrapParams`) per ktn-linter `KTN-STRUCT-ROLE`. Short, clean names are re-exported at `pkg/v1/*` via type aliases (`Attr = AttrValue`).
- **One exported struct per file** (`KTN-STRUCT-ONEFILE`). Exceptions: DTOs with serialization tags.
- **Every function needs a docstring** with `Params:` + `Returns:` sections; every control block takes a `//:` intent comment; every case label has its own intent comment. See existing files for the concrete shape.
- **Tests.** `*_internal_test.go` for white-box, `*_external_test.go` for black-box; table-driven with a `runCase` helper so the linter's static analyser sees direct calls.
- **Code ranges.** Each emitter package owns a 100-slot block of error codes (ADR 0002 registry). The AST audit (`make sdk-errs-audit`) enforces uniqueness and `reason = screamingSnake(varName)`.

## Subtree

- `kernel/` — see `internal/kernel/CLAUDE.md`
- `core/` — see `internal/core/CLAUDE.md`
- `service/` — see `internal/service/CLAUDE.md`

## Do NOT

- Move a logger-specific concept into `kernel/`. The kernel rule is both "stdlib-only" AND "generic". `level` was moved OUT for that reason.
- Call `fmt.Errorf` / `errors.New` in production. All errors go through `errs.Define` / `errs.Wrap`.
- Reference `service/*` from `core/*` or `kernel/*`; the direction is top-down.

## Verification

```
# Primary (Bazel — the source of truth for CI)
bazel test --config=race //internal/...

# Fallback (go test — still works for quick local iteration)
GOWORK=off
for m in internal/kernel internal/core internal/service; do
  (cd $m && go test -race -cover ./...)
done
```
