<!-- updated: 2026-04-19T10:18:42Z -->
# internal/service/

## Purpose

Concrete implementations of the contracts declared in `internal/core/*`. This is where actual I/O, formatting, locking, and error wrapping happen. The `pkg/v1/*` facade wraps service constructors and hides the wiring from consumers.

## Contents

| Package | Purpose | Code range |
|---|---|---|
| `logger/` | `TextHandler` + `loggerImpl` realising `core/logger.Handler` + `Logger` | 3100-3199 (emitter) |

## Module

Single module `github.com/kitsunium/sdk/internal/service` — one `go.mod`. `replace` directives resolve `../kernel` and `../core` locally so `GOWORK=off go build ./...` works per-module in CI.

## Emitted errors (service/logger)

| Code | Var | Trigger |
|---|---|---|
| 3101 | `WriterNil` | `NewTextHandler(nil, …)` |
| 3102 | `HandlerNil` | `New(nil)` |
| 3110 | `CtxCancelled` | `Handle(ctx, r)` with a cancelled context — wraps `context.Canceled` |
| 3120 | `WriteFailed` | Underlying `io.Writer.Write` returned an error — wraps the cause, ExitCode override 74 (EX_IOERR) |

All four are declared in `logger/codes.go` + `logger/errors.go`. The AST audit (`make sdk-errs-audit`) enforces the convention `Go var name == Reason`.

## Conventions

- **Imports allowed**: stdlib + `internal/kernel/*` + `internal/core/*`. Never `pkg/*`.
- **Concurrent safety**: every public type exposes the "safe for concurrent use" contract from core, and implements it (typically via `sync.Mutex` for stateful handlers).
- **Error wrapping**: `errs.Wrap(cause, WrapParams{…})` when the cause is a stdlib error; the `errs.WrapParams` fields are SILENTLY IGNORED when the cause is already an `*errs.Error` (origin wins). See `internal/kernel/errs/README.md`.
- **Function length**: `KTN-FUNC-MAXLOC` caps at 50 lines. `TextHandler.Handle` was split into `renderLine` + `writeLine` helpers for that reason.

## Do NOT

- Re-export a service type as the public-facing API. The public facade is `pkg/v1/*` — consumers should never see a `svclogger.TextHandler` type.
- Reach into `core/logger` structs to mutate them. `AttrValue` and `RecordEvent` are immutable after construction.
- Swallow writer errors silently in a handler. Wrap them via `errs.Wrap` so `errors.Is(err, originalCause)` keeps working.

## Subtree

- `logger/` — see `internal/service/logger/README.md` (full contract, error catalogue, output format)

## Verification

```
# Primary (Bazel)
bazel test --config=race //internal/service/...

# Fallback (go test)
cd internal/service
GOWORK=off go test -race -cover ./...
# expected: logger 100%
```
