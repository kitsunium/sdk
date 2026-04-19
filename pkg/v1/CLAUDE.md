<!-- updated: 2026-04-19T10:18:42Z -->
# pkg/v1/

## Purpose

The first (and today only) major version of the SDK's public API. Type signatures exposed here are frozen post-v1.0.0 — breaking changes land in `pkg/v2`.

## Packages

| Package | Role | README |
|---|---|---|
| `logger/` | Logging facade: `Config`, `NewText`, `Default`, `Info/Warn/Error/Debug`, `String/Int` | `pkg/v1/logger/README.md` |
| `errs/` | Read-only error introspection: `CodeOf`, `ReasonOf`, `PublicOf`, `PrivateOf`, `LayerOf`, `HTTPStatusOf`, `ExitCodeOf`, `HasCode`, `HasReason` | `pkg/v1/errs/README.md` |

## Module

Single module `github.com/kitsunium/sdk/pkg/v1` — one `go.mod`, one `go.sum`.

## Public surface contract

- **Stable identifiers**: every exported name / type / const listed in the package READMEs is frozen until `pkg/v2`.
- **No constructors for internal types**: `pkg/v1/errs` exposes `Of`-accessors, never the concrete `*errs.Error`.
- **No pass-through of `FieldValue`** at v1: if consumer demand appears, we add `FieldsOfAsMap(err) map[string]string` in a minor bump.
- **Breaking change policy**: 4 breaking signature changes + 1 behaviour change already landed with the errors MR (see ADR 0002 "Breaking changes"). The next set — if any — goes to `pkg/v2`.

## Conventions

- Import aliases at call sites: always `logger "github.com/kitsunium/sdk/pkg/v1/logger"` + `errs "github.com/kitsunium/sdk/pkg/v1/errs"` for clarity.
- Every emitted record carries `framework_version` via the ldflags-injected `logger.Version`. Injection recipe is in `pkg/v1/logger/README.md`.
- `logger.NewText(Config{})` refuses a nil Writer and returns `(nil, WriterRequired)`. Use `logger.Default()` for the stderr convenience.

## Do NOT

- Expose `PrivateOf(err)` output in HTTP/gRPC responses, error pages, or any user-facing surface. It is diagnostic-only — documented in the package godoc and the README.
- Try to construct a `*errs.Error` from consumer code. Go's `internal/` rule blocks it and the design is deliberate.
- Set `Version` at runtime from application code; use the ldflags recipe so every binary commits its version at link time.

## Verification

```
cd pkg/v1
GOWORK=off go test -race -cover ./...
# expected: logger 84.2%, errs [no statements] (accessors are re-exports)
```

## Subtree

- `logger/` — `pkg/v1/logger/README.md`
- `errs/` — `pkg/v1/errs/README.md`
