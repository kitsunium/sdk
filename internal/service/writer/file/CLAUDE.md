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

## Do NOT

- Re-implement path validation / symlink hardening here — that lives in
  `service/logger/sink/file`. This package only resolves config → sink.
- Add rotation — same out-of-scope note as the underlying file sink.

## Verification

```sh
bazel test --config=race //internal/service/writer/file:file_test
# Fallback
cd internal/service && GOWORK=off go test -race -cover ./writer/file/...
```

## Accepted audit findings

- Deferred/accepted low+info audit findings (V44) are recorded in `.claude/contexts/sdk-audit-2026-06-03-accepted.yaml` (2026-06-03 close-out). Each is a deliberate decision or deferred change, not an open bug.
