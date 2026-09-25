<!-- updated: 2026-09-13T17:40:00Z -->
# internal/service/logger/sink/file/

## Purpose

Terminal `Sink` that appends formatted bytes onto an on-disk file. Opens
with `O_APPEND | O_CREATE | O_WRONLY` (+ `O_NOFOLLOW` on every Unix) so
concurrent producers — and other processes appending to the same file —
emit atomic records as long as the payload stays under `PIPE_BUF` on
POSIX. Rotation is intentionally out of scope (compose with a future
`sink/rotate` or lumberjack-style wrapper).

## Contents

| File | Role |
|---|---|
| `file.go`                  | `fileSink` + `New` + `Write` / `Flush` (`fsync`) / `Close` + `refuseSymlink` + `explainOpenFailure` |
| `open_flags_unix.go`       | `openFlags = O_APPEND \| O_CREATE \| O_WRONLY \| O_NOFOLLOW` — every `unix` GOOS |
| `open_flags_other.go`      | `!unix` fallback (no `O_NOFOLLOW`): windows, plan9, js/wasm, wasip1 |
| `codes.go`, `errors.go`    | sentinels — range 0.3.14.\* |

## Security hardening

- **Symlink rejection (CWE-59), in two halves.** `refuseSymlink` calls
  `os.Lstat` before `os.OpenFile` and refuses paths whose final component
  is a symlink. A check has a window after it, and `O_NOFOLLOW` in
  `openFlags` is what makes the *kernel* refuse inside that window — so
  the two are not redundant. `O_NOFOLLOW` governs the **final component
  only**: a symbolic link at a PARENT component is still traversed, which
  is what the "do NOT place the log file under an attacker-writable
  directory" pre-condition below covers.
- **Both refusals carry `kind=symlink`.** One sentinel (`OpenFailed`)
  answers every open failure here, so a full disk and someone redirecting
  the log path would otherwise produce the same line. The field is added
  by `refuseSymlink` (policy, before the open) and by `explainOpenFailure`
  (diagnosis, *after* the kernel already refused). The `os.Lstat` in
  `explainOpenFailure` is never the decision — a planter who removes the
  link between the open and that stat changes a field and cannot change
  the refusal. The errno is deliberately not consulted: `ELOOP` on linux /
  openbsd / darwin, `EMLINK` on freebsd / dragonfly, `EFTYPE` on netbsd
  (ADR 0082 §D4).
- **Default permission 0600.** `defaultFilePerm` restricts reads to the
  owning UID so diagnostic content (attr values, wrapped `Private`
  fields that consumers log via the Source chain) is never world-readable.
  Operators that need group/other access `chmod` explicitly.

### Which platform gets which protection

Protection is **not uniform**, and this table says where it is not. `Lstat`
is `refuseSymlink`, present everywhere; `O_NOFOLLOW` is the kernel half.

| Platform | `syscall.O_NOFOLLOW` in go1.27.1 | Protection at the open |
|---|---|---|
| every `//go:build unix` GOOS — `linux` (13 arches), `darwin`, `freebsd`, `openbsd`, `netbsd`, `dragonfly`, `android`, `ios`, `aix`, `solaris`, `illumos` | present on all **39** GOOS/GOARCH pairs the `unix` tag selects | `Lstat` **+** `O_NOFOLLOW` — the TOCTOU window between them is closed |
| `windows`, `plan9`, `js/wasm` | absent | `Lstat` **only** — a link planted between the check and the open **is followed** |
| `wasip1` | present (`0400`), but it is a `path_open` lookupflag, not a kernel flag | `Lstat` only, **by choice** — the refusal would belong to the WASI host and no lane here runs one |

Measured, not assumed: compiling `const _ = syscall.O_NOFOLLOW` as a
*library* package (never linked, so cgo/PIE link rules cannot mask the
question) for all **47** pairs of `go tool dist list` under the toolchain
this repo resolves (`go1.27.1`,
`$(go env GOROOT)/src/syscall/zerrors_<goos>_<goarch>.go`). 39 are selected
by `unix` and every one of them compiles the constant; the remaining 8 are
`js/wasm`, `plan9/{386,amd64,arm}`, `windows/{386,amd64,arm64}` and
`wasip1/wasm` — the second and third rows above.

Of the `unix` GOOS, five are **executed** on a real kernel by the
`e2e-cross` lane (`SERVICE_FILE_PKGS` carries `./logger/sink/file`):
linux, darwin, freebsd, openbsd, netbsd. DragonFly, aix, solaris, illumos,
android and ios get the flag by build tag and by cross-compile, never by
execution — they are covered, not proven, and this sentence is the
difference.

Closing the gap on Windows needs a different primitive, not a different
flag: `FILE_FLAG_OPEN_REPARSE_POINT` **opens** the link and the handle must
then be rejected (ADR 0082 §D2 measured both shapes for
`internal/service/lock`). Not done here, and named rather than implied.

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
| 0.3.14.2  | `OpenFailed`   | `os.OpenFile` failed OR path is a symlink (EX_IOERR); carries `kind=symlink` when a link is what was found |
| 0.3.14.10 | `CtxCancelled` | `Write` saw a cancelled `ctx` |
| 0.3.14.20 | `WriteFailed`  | `*os.File.Write` failed (EX_IOERR) |
| 0.3.14.30 | `SyncFailed`   | `*os.File.Sync` failed during `Flush` (EX_IOERR) |
| 0.3.14.40 | `CloseFailed`  | `*os.File.Close` failed (EX_IOERR) |

## Tests

Use `t.TempDir()` for paths and `t.Cleanup(func(){ sink.Close() })` so the
descriptor releases deterministically even on test failure.

`TestNew_RejectsSymlink` plants a link and then calls `New`, so
`refuseSymlink` answers first and the flag word is never asked anything —
that case passes identically with and without `O_NOFOLLOW`.
`open_flags_unix_internal_test.go` (`//go:build unix`) is what asks the
flag word directly: it opens with exactly `openFlags` and `defaultFilePerm`
against a path that IS a link, with a live row and a dangling row, asserts
no descriptor and an untouched target, and **never inspects the errno**. A
row that cannot be planted is not a passing row — the parent counts the
rows planted and fails on zero.

## Do NOT

- Place the log file under an attacker-writable directory; the hardening
  is TOCTOU-safe only when the directory tree is not world-writable.
- Expect rotation — sink composition or out-of-band tooling owns that.

## Verification

```
bazel test --config=race //internal/service/logger/sink/file:file_test
```

The Bazel lane is Linux only, and Linux had `O_NOFOLLOW` before this package
did. The off-Linux proof is `e2e-cross`, which runs
`go test -count=1 -short ./logger/sink/file` on macos-15, freebsd 15.0,
openbsd 7.9 and netbsd 10.1 (and skips `SERVICE_FILE_PKGS` on windows).
