<!-- updated: 2026-05-18T14:30:00Z -->
# internal/service/logger/sink/file/

## Purpose

Terminal `Sink` that appends formatted bytes onto an on-disk file. Opens
with `O_APPEND | O_CREATE | O_WRONLY` (+ `O_NOFOLLOW` on Linux) so
concurrent producers — and other processes appending to the same file —
emit atomic records as long as the payload stays under `PIPE_BUF` on
POSIX. Rotation is intentionally out of scope (compose with a future
`sink/rotate` or lumberjack-style wrapper).

## Contents

| File | Role |
|---|---|
| `file.go`                  | `fileSink` + `New` + `Write` / `Flush` (`fsync`) / `Close` + `refuseSymlink` |
| `open_flags_linux.go`      | `openFlags = O_APPEND \| O_CREATE \| O_WRONLY \| O_NOFOLLOW` |
| `open_flags_other.go`      | non-Linux fallback (no `O_NOFOLLOW`) |
| `codes.go`, `errors.go`    | sentinels — range 0.3.14.\* |

## Security hardening

- **Symlink rejection (CWE-59).** `refuseSymlink` calls `os.Lstat` before
  `os.OpenFile` and refuses paths whose final component is a symlink.
  On Linux, `O_NOFOLLOW` closes the residual TOCTOU window between the
  Lstat check and the open. Pre-condition: the parent directory must not
  be attacker-writable.
- **Default permission 0600.** `defaultFilePerm` restricts reads to the
  owning UID so diagnostic content (attr values, wrapped `Private`
  fields that consumers log via the Source chain) is never world-readable.
  Operators that need group/other access `chmod` explicitly.

## Behaviour

- `New(path)` validates non-empty path, runs the symlink hardening, then
  opens with the platform-specific flags. The constructor uses a
  `defer`-on-error pattern: any post-open error closes the descriptor and
  chains the close failure via `errs.Wrap` (CloseFailed).
- `Write` is mutex-serialised; payload errors wrap as `WriteFailed`
  (EX_IOERR).
- `Flush` calls `*os.File.Sync` (fsync) under the same mutex; failures
  wrap as `SyncFailed` (EX_IOERR).
- `Close` releases the descriptor; failures wrap as `CloseFailed`
  (EX_IOERR). Subsequent `Write` calls surface the kernel's "file already
  closed" error wrapped as `WriteFailed`.

## Error catalogue — range 0.3.14.\*

| Code      | Sentinel       | Trigger |
|---|---|---|
| 0.3.14.1  | `PathEmpty`    | `New("")` |
| 0.3.14.2  | `OpenFailed`   | `os.OpenFile` failed OR path is a symlink (EX_IOERR) |
| 0.3.14.10 | `CtxCancelled` | `Write` saw a cancelled `ctx` |
| 0.3.14.20 | `WriteFailed`  | `*os.File.Write` failed (EX_IOERR) |
| 0.3.14.30 | `SyncFailed`   | `*os.File.Sync` failed during `Flush` (EX_IOERR) |
| 0.3.14.40 | `CloseFailed`  | `*os.File.Close` failed (EX_IOERR) |

## Tests

Use `t.TempDir()` for paths and `t.Cleanup(func(){ sink.Close() })` so the
descriptor releases deterministically even on test failure.

## Do NOT

- Place the log file under an attacker-writable directory; the hardening
  is TOCTOU-safe only when the directory tree is not world-writable.
- Expect rotation — sink composition or out-of-band tooling owns that.

## Verification

```
bazel test --config=race //internal/service/logger/sink/file:file_test
```
