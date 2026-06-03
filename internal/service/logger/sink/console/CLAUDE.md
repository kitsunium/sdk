<!-- updated: 2026-05-18T14:30:00Z -->
# internal/service/logger/sink/console/

## Purpose

Terminal `Sink` that writes formatted bytes onto an `io.Writer` (typically
`os.Stderr` or `os.Stdout`) under a `sync.Mutex` so concurrent producers
emit atomic lines. This is the default sink used by
`pkg/v1/logger.Default`.

## Contents

| File | Role |
|---|---|
| `console.go`            | `consoleSink` + `New` / `NewStderr` / `NewStdout` |
| `codes.go`, `errors.go` | sentinels — range 0.3.13.\* |

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
bazel test --config=race //internal/service/logger/sink/console:console_test
```

## Accepted audit findings

- Deferred/accepted low+info audit findings (V38) are recorded in `.claude/contexts/sdk-audit-2026-06-03-accepted.yaml` (2026-06-03 close-out). Each is a deliberate decision or deferred change, not an open bug.
