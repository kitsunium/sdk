<!-- updated: 2026-10-02T19:58:06Z -->
# internal/service/observe/logger/sink/console/

## Purpose

Terminal `Sink` that writes formatted bytes onto an `io.Writer` (typically
`os.Stderr` or `os.Stdout`) under a `sync.Mutex` so concurrent producers
emit atomic lines. It backs `pkg/v1/observe/logger`'s `ConsoleStderr` /
`ConsoleStdout` and `NewWriterSink`; `pkg/v1/observe/logger.Default` uses the fused
`TextHandler` instead, not this sink.

## Contents

| File | Role |
|---|---|
| `decl_gen.go` | written by kit gen from the design (ADR 0170): `NewStderr` and `NewStdout`, each one call of its unexported body, measured to inline with the body inlined into it. Every body stays hand-written, in the files this document names — each wrapper's under its unexported name |
| `console.go`            | `consoleSink` + `New` / `NewStderr` / `NewStdout` |
| `internal/core/observe/logger/sink/console` | its sentinels — range 0.3.13.\* — declared in the core mirror since ADR 0160; this package declares none |

## Behaviour

- `New(w)` rejects nil writers — returns `WriterNil`.
- `NewStderr` / `NewStdout` bypass validation (those FDs are non-nil by
  construction) and return the `Sink` directly.
- `Write` serialises every call through the internal mutex; the wrapped
  `io.Writer.Write` runs under the lock so concurrent goroutines never
  interleave lines.
- Errors from the underlying writer are wrapped via `errs.Wrap` with
  `WriteFailed` (EX_IOERR / exit 74), attaching `bytes` and `level`
  diagnostics fields.
- `Flush` is a no-op (writes are already synchronous); honours
  `ctx.Err()` for symmetry with other sinks.
- `Close` is a **no-op** — the caller owns `os.Stdout` / `os.Stderr` and
  is responsible for closing custom `io.Writer`s.

## Error catalogue — range 0.3.13.\*

Declared in `internal/core/observe/logger/sink/console` since ADR 0160 §2: this engine returns the sentinels below and declares none, so a test or a caller names them `coreconsole.X`.

| Code      | Sentinel        | Trigger |
|---|---|---|
| 0.3.13.1  | `WriterNil`     | `New(nil)` |
| 0.3.13.10 | `CtxCancelled`  | `Write` saw a cancelled `ctx` (wraps `context.Canceled`) |
| 0.3.13.20 | `WriteFailed`   | underlying `io.Writer.Write` returned an error (EX_IOERR) |

## Do NOT

- Call `Close` expecting it to close the underlying writer.
- Write very large payloads (above PIPE_BUF on POSIX) without considering
  atomicity — the mutex still serialises, but multi-line payloads cross
  PIPE_BUF and lose kernel-level atomicity guarantees.

## Verification

```
bazel test --config=race //internal/service/observe/logger/sink/console:console_test
```
