# internal/service/writer/rotfile/

## Purpose

Registers the **"rotfile"** writer factory (ADR 0014): a size-capped, on-disk
file sink that rotates when a write would exceed `MaxBytes` and optionally gzips
each rotated file. Importing this package self-registers the factory
(`var Writer = writer.Register(&rotFileFactory{})`, no `init()`), so
`writer.Open("rotfile", rotfile.Config{…})` resolves.

Unlike `service/writer/file`, this sink **owns its descriptor directly** — it
must close, rename, and reopen `Path` across a rotation, so it cannot delegate
to the append-only `service/logger/sink/file`. The security hardening of that
sink is reproduced here and, critically, **re-run on every reopen**.

## Contents

| File | Role |
|---|---|
| `rotfile.go` | `Writer` singleton, `rotFileFactory` (`Name` / `Open`) |
| `rotating_sink_config.go` | `Config` — an **alias** of `core/writer.RotFileConfig` (moved there for issue #93 so `pkg/v1/logger` can re-export it); type identity unchanged |
| `rotating_sink.go` | `rotatingSink` + `Write` / `Flush` / `Close` + `openHardened` / `refuseSymlink` / `explainOpenFailure` / `newRotatingSink` (wires the interval daemon) |
| `rotate_interval.go` | `tickRotate` — the `worker.Every` daemon tick body |
| `decode.go` | `rotFileFactory.Decode` (`core/writer.Decoder`) + key-coercion helpers |
| `rotate.go` | `maybeRotate` / `rotate` / `shiftBackups` (threshold + cycle) |
| `backups.go` | backup-name arithmetic + per-slot move/gzip helpers |
| `prune.go` | `pruneByAge` calendar pruning (MaxAgeDays) |
| `open_flags_unix.go` | `openFlags = O_APPEND \| O_CREATE \| O_WRONLY \| O_NOFOLLOW` — every `unix` GOOS |
| `open_flags_other.go` | `!unix` fallback (no `O_NOFOLLOW`): windows, plan9, js/wasm, wasip1 |
| `codes.go`, `errors.go` | sentinels — range 0.3.27.\* (service slot 0x1b) |

## Security hardening

- **Symlink rejection re-applied on EVERY reopen (CWE-59).** `openHardened`
  runs the `os.Lstat` symlink refusal (`refuseSymlink`) **and** opens with
  `openFlags` + `0600`. It is the single open entry point used by both
  construction **and** the rotation reopen, so a symlink planted at `Path`
  after a rename is refused on the next write — not only at first `New`.
  Pre-condition: the parent directory must not be attacker-writable.
- **The two refusals are not redundant.** `refuseSymlink` is a *check*, so it
  has a window after it; `O_NOFOLLOW` in `openFlags` is what makes the *kernel*
  refuse inside that window. `O_NOFOLLOW` governs the **final component only** —
  a symlink at a parent component is still traversed, which is what the
  attacker-writable-directory pre-condition covers.
- **Both refusals carry `kind=symlink`** on the same sentinel
  (`RotFileOpenFailed`), so a planted link is distinguishable from a full disk
  at whichever of the two points caught it. The `os.Lstat` inside
  `explainOpenFailure` runs **after** the open has already been refused: it is
  diagnosis, never the decision, and nothing branches on the errno (it is
  `ELOOP` on linux/openbsd/darwin, `EMLINK` on freebsd/dragonfly, `EFTYPE` on
  netbsd — ADR 0082 §D4).
- **0600 everywhere.** The active file, every rotated `.N`, and every `.N.gz`
  sibling are forced to `0600`. The gzip sibling is created with an explicit
  `0600` rather than inheriting gzip's default permissions.

### Which platform gets which protection

Protection is **not uniform**, and this table says where it is not. `Lstat` is
`refuseSymlink`, present everywhere; `O_NOFOLLOW` is the kernel half.

| Platform | `syscall.O_NOFOLLOW` in go1.27.0 | Protection at the open |
|---|---|---|
| every `//go:build unix` GOOS — `linux` (13 arches), `darwin`, `freebsd`, `openbsd`, `netbsd`, `dragonfly`, `android`, `ios`, `aix`, `solaris`, `illumos` | present on all **39** GOOS/GOARCH pairs the `unix` tag selects | `Lstat` **+** `O_NOFOLLOW` — the TOCTOU window between them is closed |
| `windows`, `plan9`, `js/wasm` | absent | `Lstat` **only** — a link planted between the check and the open **is followed** |
| `wasip1` | present (`0400`), but it is a `path_open` lookupflag, not a kernel flag | `Lstat` only, **by choice** — the refusal would belong to the WASI host and no lane here runs one |

Measured, not assumed: compiling `const _ = syscall.O_NOFOLLOW` as a *library*
package (never linked, so cgo/PIE link rules cannot mask the question) for all
**47** pairs of `go tool dist list` under the toolchain this repo resolves
(`go1.27.0`, `$(go env GOROOT)/src/syscall/zerrors_<goos>_<goarch>.go`). 39 are
selected by `unix` and every one of them compiles the constant; the remaining 8
are `js/wasm`, `plan9/{386,amd64,arm}`, `windows/{386,amd64,arm64}` and
`wasip1/wasm` — the three rows of the second and third lines above.

Closing the gap on Windows needs a different primitive, not a different flag:
`FILE_FLAG_OPEN_REPARSE_POINT` **opens** the link and the handle must then be
rejected (ADR 0082 §D2 measured both shapes for `internal/service/lock`). Not
done here, and named rather than implied.

## Behaviour

- `Open` type-asserts `rotfile.Config`; a mismatch returns the shared
  `core/writer.WriterConfigInvalid`. An empty `Path` or a symlink target
  surfaces this package's `RotFileOpenFailed`.
- `Write` is mutex-serialised; it rotates **before** the write when the
  projected size would exceed `MaxBytes` (`MaxBytes <= 0` disables rotation; an
  empty file is never spun into a backup so a single oversized record still
  lands).
- Rotation renames `Path -> Path.1`, shifting `.1 -> .2 …` up to `MaxBackups`
  (dropping the oldest; `MaxBackups == 0` keeps all), optionally gzips the
  rotated file, then reopens `Path`.
- **Producer-stall window:** when `Compress` is set, the gzip runs while the
  sink is between descriptors (active file closed). Producers stall for the
  compaction; this is the documented trade-off versus a background goroutine.
- **Crash window:** the rename is **not** followed by a directory `fsync`, so a
  crash between rename and reopen can leave `Path` missing until the next write.
  Accepted for logs — dir-fsync on every rotation is not paid for.

### Interval rotation (`RotateEvery`, opt-in)

- A strictly positive `RotateEvery` starts a `worker.Every` ticker daemon that
  forces a rotation on each interval — the "archive every 24h" policy — composing
  with the `MaxBytes` size threshold and the `MaxBackups` / `MaxAgeDays`
  retention caps. The zero value spawns **no** goroutine: plain file logging
  never pays for a ticker it did not ask for (rotation stays off by default).
- A failing tick does not log or panic on the daemon goroutine: `tickRotate`
  stashes the typed `RotFileRotateFailed` under the mutex and the **next** `Write`
  surfaces it exactly once via the `Config.OnError` hook, then clears it (genuine
  one-shot propagation, not a latched error — `KTN-ERROR-DISCARD`). The
  triggering record is **still written** on that same `Write`: the hook reports
  the failure, it does not gate the write, so a rare rotation I/O failure never
  also costs a dropped log line. `OnError` is optional (`nil` disables it); the
  field mirrors the `middleware/async` `OnError func(error)` pattern.
- `Close` joins the ticker **before** taking the mutex (Stop blocks on the join,
  and the tick locks the mutex via `Rotate` — joining under the lock would
  deadlock). The `Write` path stays **0 alloc** with the daemon running (see
  `BENCH.md`).

### Decoder (YAML-reachable via `FromConfig`, opt-in import)

`rotFileFactory` implements `core/writer.Decoder`, so a blank-imported `rotfile`
is reachable from a topology config blob. Recognised keys (under a writer entry's
`config:` map):

| Key | Type | Maps to |
|---|---|---|
| `path` | string (required) | `Path` |
| `max_bytes` | int | `MaxBytes` |
| `max_backups` | int | `MaxBackups` |
| `max_age_days` | int | `MaxAgeDays` |
| `compress` | bool | `Compress` |
| `rotate_every` | string (Go duration) | `RotateEvery` |
| `min_level` | string | `MinLevel` via `level.ParseLevel` |

`rotate_every` is **tolerant**: unset / non-positive / unparsable → interval
rotation disabled (never reaches `worker.Every`). An enabled `rotate_every` with
no `max_age_days` defaults retention to **7 days**. A malformed scalar shape for
any other key returns `RotFileDecodeFailed`. **Secret gate:** the error names
only the writer, never the offending value.

## Error catalogue — range 0.3.27.\*

| Code | Sentinel | Trigger |
|---|---|---|
| 0.3.27.1 | `RotFileOpenFailed` | open / reopen failed OR path is a symlink (EX_IOERR) |
| 0.3.27.2 | `RotFileRotateFailed` | a rename / gzip / chmod step in the cycle failed, or a failing interval tick (EX_IOERR) |
| 0.3.27.3 | `RotFileWriteFailed` | `*os.File.Write` / `Sync` / `Close` failed, or cancelled ctx (EX_IOERR) |
| 0.3.27.4 | `RotFileDecodeFailed` | a config-map value had an unexpected shape (EX_CONFIG) |

## Tests

Use `t.TempDir()` for paths. The reopen-hardening case plants a symlink where
the reopened active file would be created and asserts `RotFileOpenFailed`.

Every one of those cases goes through `openHardened`, so `refuseSymlink`
answers first and the flag word is never asked anything — they pass identically
with and without `O_NOFOLLOW`. `open_flags_unix_internal_test.go` (`//go:build unix`) is
the one that is not blind to it: it opens with the same flag word and mode
against a path that **is** a symlink, which is the state a planter leaves by
winning the race against the check. Its dangling-link row is the sharper of the
two — a followed `O_CREATE` *creates* the target, so the target's continued
absence is positive proof of non-traversal. A row that cannot be planted is not
a passing row: the parent counts planted rows and fails at zero rather than
going green on an empty table.

`./writer/rotfile` runs on real kernels in `.github/workflows/e2e-cross.yml`
(`SERVICE_FILE_PKGS`): linux, macos-15, freebsd, openbsd, netbsd. It is skipped
on the windows lane (the suite asserts 0600 and chmod-freezes directories,
neither of which means anything under ACLs), and **dragonfly has no lane at
all** — it gets `O_NOFOLLOW` by build tag and by cross-compile, never by
execution.

## Do NOT

- Place the log file under an attacker-writable directory — the hardening is
  TOCTOU-safe only when the directory tree is not world-writable.
- Delegate to `service/logger/sink/file` — that sink is append-only and cannot
  reopen across a rename, which is the whole point of this package.

## Verification

```sh
bazel test --config=race //internal/service/writer/rotfile:rotfile_test
# Fallback
cd internal/service && GOWORK=off go test -race -cover ./writer/rotfile/...
```

## Accepted audit findings

- Deferred/accepted low+info audit findings (V48) are recorded in `.claude/contexts/sdk-audit-2026-06-03-accepted.yaml` (2026-06-03 close-out). Each is a deliberate decision or deferred change, not an open bug.
