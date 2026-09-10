# internal/service/writer/file/

## Purpose

Registers the **"file"** writer factory (ADR 0012). Importing this package
self-registers the factory (`var Writer = writer.Register(&fileFactory{})`, no
`init()`), so `writer.Open("file", logger.FileConfig{…})` resolves. The factory
is a thin adapter: it delegates to the existing append-only, symlink-hardened
sink in `service/logger/sink/file` and wraps it with the optional per-writer
`MinLevel` via `service/writer/levelgate`.

## Contents

| File | Role |
|---|---|
| `file.go` | `Writer` singleton, `fileFactory` (`Name` / `Open` / `Decode`) + decode helpers |

No `codes.go` — a wrong-type `Config` returns the shared
`core/writer.WriterConfigInvalid`; the underlying sink owns its I/O codes
(`0.3.14.*`: `PathEmpty`, `OpenFailed`, …), which propagate verbatim (origin
wins).

## Behaviour

- `Open` type-asserts `writer.FileConfig`; a mismatch returns
  `WriterConfigInvalid`.
- An empty `Path` surfaces the file sink's `PathEmpty`; a symlink / open failure
  surfaces `OpenFailed` — both forwarded unchanged.
- The opened sink is wrapped in `levelgate.New(base, cfg.MinLevel)` — a no-op
  when `MinLevel == Info` (inherit).

### Decoder (YAML-reachable via `FromConfig`)

`fileFactory` also implements `core/writer.Decoder`, so the default-active file
writer is reachable from a topology config blob. Recognised option keys (under a
writer entry's `config:` map):

| Key | Type | Default | Maps to |
|---|---|---|---|
| `path` | string | — (required, non-empty) | `Path` |
| `min_level` | string | inherit (info) | `MinLevel` via `level.ParseLevel` (`debug`/`info`/`warn`/`error`) |

A missing/empty/non-string `path`, an unknown `min_level`, or a non-string
`min_level` returns the shared `core/writer.WriterConfigInvalid` (no new code).
Unlike `Open` (which defers an empty path to the sink's `PathEmpty`), `Decode`
rejects an absent/empty path itself so a malformed topology fails fast.
**Secret gate:** the error names only the writer (`writer=file` field), never the
decoded path or option value.

## Cost

Measured in `BENCH.md` (median of five runs, AMD EPYC 7351P). Every row names
the filesystem it ran on, **detected by `statfs(2)`, never assumed from the
path** — because the one call that matters differs by three orders of magnitude
between RAM and a block device.

| | ns/op | allocs |
|---|---:|---:|
| `Write`, `/dev/null` (syscall floor) | 318.5 | 0 |
| `Write`, tmpfs | 882.3 | 0 |
| `Write`, ext4 | 1 007 | 0 |
| **what this package adds over a raw `*os.File.Write`** | **~+21** | **0** |
| `Flush` (fsync) alone, ext4 — nothing dirty | 55 556 | 0 |
| **write + `Flush` per record, ext4** | **1 385 658** | 0 |
| the same flushing every 256 records | 7 524 | 0 |
| `writer.Open` + `Close`, ext4 | 6 678 | 7 (512 B) |
| raw `os.OpenFile` + `Close`, identical flags | 4 507 | 3 (184 B) |

- **The package's own cost is ~21 ns and is per CALL, not per byte** — the sink
  row is flat to 0.7 % across a 42× payload range. It is one interface call, one
  `ctx.Err()` check and one mutex. `../console/BENCH.md` measures a completely
  different sink at 302.2 ns over the same `/dev/null`, against 309.8–318.5 here:
  the two default writers cost the same because the thing they cost is the same
  mutex.
- **Durability costs 1 376× a write, per record** — the same order as the 1 865×
  ADR 0054 measured for the queue domain's durable round trip, and for the same
  reason: the cost is device round trips, not payload. Flushing every 256
  records brings it to 7.5×. **A benchmark that only calls `Flush` under-reports
  it by 25×**, because consecutive `fsync`s with nothing dirty never reach the
  journal; that is stated in `BENCH.md` §4 and is why `WriteThenFlush` exists.
- **`b.TempDir()` is one environment variable away from being RAM.** With
  `TMPDIR` unset on this machine it resolves to `/tmp`, which is tmpfs, where
  `Flush` costs **219 ns instead of 55 556** — 254×, or 6 327× against a real
  dirty `fsync`. Every `Flush` assertion in this package's suite would pass
  identically against a filesystem that pushes nothing to any device. The bench
  row labels are what makes that visible; `SDK_BENCH_DISK_DIR` is a named
  variable rather than a guessed path for the same reason.
- **The writer adds ZERO allocations to an emit.** 1 allocation / 128 B with no
  transport, 1 allocation / 128 B through this writer; the profile puts 95.08 %
  of the objects on the handler's attrs clone and none on the transport. This
  SDK's file code is 3.82 % of an emit's CPU.
  `TestFileWriterAddsNoAllocationToAnEmit` pins the DELTA plus `Write` and
  `Flush` at zero, is mutation-checked in its own doc comment, and is covered by
  the race-off alloc lane (`tools/alloc-lane-targets.txt`, SDK-wide rule 12).
- **The sink's mutex costs nothing** — measured against a raw descriptor holding
  a `sync.Mutex` of its own, the two are within 8 % and the raw arm is slower in
  one of the three roots, because `internal/poll.FD.Write` already takes a
  per-descriptor write lock. And **eight goroutines get the throughput of one**:
  1 186 ns/op parallel against 1 007 ns serially on ext4.
- **The CWE-59 symlink refusal costs +2 171 ns and 4 allocations, once per
  writer**, against a per-record cost of ~21 ns.

## Do NOT

- Re-implement path validation / symlink hardening here — that lives in
  `service/logger/sink/file`. This package only resolves config → sink.
- Add rotation — same out-of-scope note as the underlying file sink.
- Add a per-record decorator in `Open`. Today the factory contributes no
  per-record code at all, which is exactly why the writer adds no allocation to
  an emit; a decorator that touches the bytes is the mutation
  `TestFileWriterAddsNoAllocationToAnEmit` exists to catch.
- Benchmark or assert anything about `Flush` on a path you have not checked with
  `statfs`. On tmpfs `fsync` is a syscall and a return, and the assertion passes
  while proving nothing — see `BENCH.md` §3.

## Verification

```sh
bazel test --config=race //internal/service/writer/file:file_test
# Fallback
cd internal/service && GOWORK=off go test -race -cover ./writer/file/...
```

## Accepted audit findings

- Deferred/accepted low+info audit findings (V44) are recorded in `.claude/contexts/sdk-audit-2026-06-03-accepted.yaml` (2026-06-03 close-out). Each is a deliberate decision or deferred change, not an open bug.
