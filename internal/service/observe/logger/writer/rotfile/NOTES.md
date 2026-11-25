# Notes — <!-- updated: 2026-10-03T00:00:00Z -->

Reference moved out of `CLAUDE.md` (ADR 0253), unchanged and under the same section titles. `CLAUDE.md` keeps what to obey; this file holds the rest.

## Behaviour

- `Open` type-asserts `rotfile.Config`; a mismatch returns the shared
  `core/observe/logger/writer.WriterConfigInvalid`. An empty `Path` or a symlink target
  surfaces `RotFileOpenFailed` (declared in `core/observe/logger/writer/rotfile`).
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
- **A refused reopen never kills the sink (#463).** When the reopen after a
  rotation fails — a link planted at `Path`, `EMFILE`, `ENOSPC` —
  `reopenAfterFailure` drops the closed descriptor (`s.f == nil`) instead of
  keeping it with `size == 0`, which no threshold ever rotated away again:
  every later write failed `ROT_FILE_WRITE_FAILED` forever, even once the cause
  was gone. Now `Write` and `rotate` reopen through `ensureOpen` first, failing
  `ROT_FILE_OPEN_FAILED` while the cause stands and writing again as soon as it
  is gone; `Flush` and `Close` have nothing to do without a descriptor
  (`syncOpen`, `closeOpen`). `Test_rotatingSink_aFailedReopenNeverKillsTheSink`
  holds it with a directory and with a link at `Path`.

- **A Flush holds no writer (#815).** `Flush` reads the active descriptor under
  `mu`, read-locks `pin` while it still holds `mu`, releases `mu` and fsyncs:
  a `Write` that does not rotate never waits for the disk. `rotate` write-locks
  `pin` (under `mu`) before closing the active file, so a rotation waits for an
  fsync of the file it is about to close — rotations are rare — and a Flush
  never sees its descriptor closed by a rotation. A `Close` racing the sync is
  reported (`ROT_FILE_WRITE_FAILED` or nil, never a hang): `os.File` keeps the
  descriptor until the in-flight `Sync` returns. Lock order: `mu` then `pin`,
  both places. `syncFile` is the tests' blocking seam; nil in production.
- **A batch is one write (#807).** `WriteBatch` (the `corelogger.BatchSink`
  sibling) concatenates into one reused buffer (let go past 1 MiB), checks
  `MaxBytes` once for the whole batch — a batch past the cap lands in a fresh
  file, as an oversized single record does — and issues one `write(2)`; it
  reports the records that landed whole.

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

`rotFileFactory` implements `core/observe/logger/writer.Decoder`, so a blank-imported `rotfile`
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
