<!-- updated: 2026-05-18T14:30:00Z -->
# internal/service/logger/middleware/recover/

## Purpose

Panic-recovery `Sink` decorator. Wraps a downstream sink so a panic inside
`Write` / `Flush` / `Close` is caught and surfaced as the typed
`Panicked` sentinel — the producer goroutine stays alive.

Use case: defensive guard around third-party sinks (HTTP, AWS SDKs,
custom user code) where a panic would otherwise unwind the application's
hot path.

## Contents

| File | Role |
|---|---|
| `recover_sink.go` | `recoverSink` + `New`; every method `defer`s its own recover block |
| `panic_value.go`  | `panicValue` adapter; `safeString` / `safeTypeName` panic-safe formatters |
| `codes.go`, `errors.go` | sentinels — range 0.3.21.\* |

## Behaviour

- `New(downstream)` rejects nil — returns `DownstreamNil`.
- On panic, the deferred recover wraps the value via `errs.Wrap` with the
  `Panicked` sentinel. The `panicValue.Error()` method exposes only
  `"panic of type T"` so the `Source()` chain never leaks the verbatim
  panic value; the rich `%v` rendering lives only in the wrapping error's
  `Private` field.
- `safeString` guards the `%v` formatting with an inner recover so a
  `Stringer` that itself panics during rendering degrades to a diagnostic
  marker instead of escaping past the outer recover.
- `safeTypeName` uses `%T` which reads the runtime type descriptor and
  cannot panic — included for symmetry and call-site documentation.

## Error catalogue — range 0.3.21.\*

| Code      | Sentinel        | Trigger |
|---|---|---|
| 0.3.21.1  | `Panicked`      | downstream `Write` / `Flush` / `Close` panicked |
| 0.3.21.2  | `DownstreamNil` | `New(nil)` |

## Do NOT

- Rely on `Error()` for panic introspection — read the wrapper's `Private`
  or `Fields["panic_type"]` instead.
- Place `recover` **inside** an `async` chain expecting it to catch
  drainer panics; the drainer runs on its own goroutine. Wrap close to
  the downstream sink that may panic.

## Verification

```
bazel test --config=race //internal/service/logger/middleware/recover:recover_test
```
