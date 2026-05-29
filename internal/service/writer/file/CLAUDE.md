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
| `file.go` | `Writer` singleton, `fileFactory` (`Name` / `Open`) |

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
