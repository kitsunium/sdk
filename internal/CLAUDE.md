<!-- updated: 2026-05-21T21:27:56Z -->
# internal/

## Purpose

The SDK's private layer. Everything here is blocked from external import by Go's `internal/` rule AND by Bazel `package_group` + `visibility` (ADR 0004). Three sublayers model the SDK's dependency discipline:

```
kernel/    stdlib-only, generic primitives (no domain vocabulary)
core/      domain interfaces and domain values
service/   concrete implementations of core contracts
```

## Dependency direction

Strictly top-down — enforced at `bazel build` time, not by lint:

```
kernel ──┐
core  ──┼──▶ service ──▶ (pkg/v1 re-exports / consumes)
         │
         └── pkg/v1 (direct for value types like Attr / Level)
```

| Layer | Imports allowed |
|---|---|
| `internal/kernel/**` | stdlib only |
| `internal/core/**`   | stdlib + `internal/kernel/*` |
| `internal/service/**`| stdlib + kernel + core (+ vetted third-party encoders for codec/*) |
| `pkg/v1/**` (consumes) | stdlib + kernel + core + service |

Any import going "upward" fails `bazel build` before it ever reaches `ktn-linter`. `.golangci.yml` ships a code-quality second-opinion ruleset only; the layer firewall is now Bazel visibility.

## Modules

Each sublayer is its own Go module (release independence + clean `go.sum` per layer):

| Module path | Build with |
|---|---|
| `github.com/kitsunium/sdk/internal/kernel`  | `cd internal/kernel && GOWORK=off go build ./...` |
| `github.com/kitsunium/sdk/internal/core`    | `cd internal/core && GOWORK=off go build ./...`   |
| `github.com/kitsunium/sdk/internal/service` | `cd internal/service && GOWORK=off go build ./...`|

`replace` directives in each `go.mod` resolve intra-repo dependencies without published pseudo-versions; `go.work` at the repo root lets `go build ./...` from the SDK root work without `replace`. Third-party deps live only in `internal/service/go.mod` (cbor, msgpack, toml, yaml).

## Conventions

- **Role-suffix types.** Exported structs take a role suffix (`AttrValue`, `RecordEvent`, `WrapParams`) per ktn-linter `KTN-STRUCT-ROLE`. Short, clean names are re-exported at `pkg/v1/*` via type aliases (`Attr = AttrValue`).
- **One exported struct per file** (`KTN-STRUCT-ONEFILE`). Exceptions: DTOs with serialization tags.
- **Doc comments follow Effective Go.** Lead with the identifier name and name the parameters and return values inline (`Foo returns the X computed from y and z.`). No Javadoc-style `Params:` / `Returns:` sections — they were removed project-wide in PR #26. Every control block still takes a `//:` intent comment; every `case` label has its own intent comment.
- **Tests.** `*_internal_test.go` for white-box, `*_external_test.go` for black-box; table-driven with a `runCase` helper so the linter's static analyser sees direct calls.
- **Dotted-quad code ranges.** Each emitter package owns a 256-slot `PP` octet (ADR 0005 + ADR 0006). Per-package `codes.go` declares the `Code` constants; `errors.go` calls `errs.Define`. The AST audit enforces uniqueness and `reason = screamingSnake(varName)` OR `screamingSnake(CodeConst − "Code")` (the namespaced style — ADR 0006/0020). Every emitter package must ship an `audit_srcs` filegroup and appear in `//:audit_sources`, else it is unaudited under Bazel.

## Subtree

- `kernel/` — see `internal/kernel/CLAUDE.md`
- `core/`   — see `internal/core/CLAUDE.md`
- `service/`— see `internal/service/CLAUDE.md`

## Do NOT

- Move a logger-specific or codec-specific concept into `kernel/`. The kernel rule is both "stdlib-only" AND "generic". `level` was moved OUT for that reason.
- Call `fmt.Errorf` / `errors.New` in production. All errors go through `errs.Define` / `errs.Wrap`.
- Reference `service/*` from `core/*` or `kernel/*`; the direction is top-down.
- Add a third-party import in `kernel/*` or `core/*` — codec parsers and encoders live in `service/codec/*`.

## Verification

```
# Primary (Bazel — source of truth for CI)
bazel test --config=race //internal/...

# Visibility firewall — kernel must have zero outgoing go_library edges
bazel query 'kind("go_library", deps(//internal/kernel/...)) except //internal/kernel/...'
# expected: empty result

# Fallback (go test — still works for quick local iteration)
GOWORK=off
for m in internal/kernel internal/core internal/service; do
  (cd $m && go test -race -cover ./...)
done
```
