<!-- updated: 2026-05-30T00:00:00Z -->
# internal/service/transform/

## Purpose

Concrete `core/transform.Compressor` implementations over the Go standard
library: **gzip** (`compress/gzip`) and **flate** (raw DEFLATE,
`compress/flate`). One Go package implements both schemes. Blank-importing the
package self-registers both singletons with the core registry so they become
resolvable by `Algorithm` (ADR 0014 D1). The first wave ships **stdlib only** —
zstd / snappy / s2 are deferred opt-in follow-ups behind their own vendor
imports, so the `pkg/v1/codec` module graph stays vendor-free.

Code range: `0.3.26.*` (`0x1a`, ADR 0014).

## Contents

| File | Surface |
|---|---|
| `gzip.go`    | `GzipCompressor` singleton + `gzipCompressor` (Algorithm "gzip") |
| `flate.go`   | `FlateCompressor` singleton + `flateCompressor` (Algorithm "flate") |
| `bounded.go` | `readAllBounded` — the shared layer-local decompression bound |
| `codes.go`   | `CodeGzipFailed` (0.3.26.1), `CodeFlateFailed` (0.3.26.2) |
| `errors.go`  | `GzipFailed` / `FlateFailed` sentinels + `gzipWrap` / `flateWrap` WrapParams |

## Bounded decompression (locked choice)

Each `Decompress` drains its stdlib reader through `io.LimitReader` capped at
`maxDecompressedBytes` (256 MiB). A stream that tries to exceed the cap returns
the scheme's `GzipFailed` / `FlateFailed` sentinel rather than driving an OOM.
This is the **conservative layer-local backstop** ADR 0014 D1 asks for; the full
self-describing decompression-bomb guard (max output size + max expansion ratio
keyed to the frame header, `CodeCompressedFrameInvalid`) lives in the
`pkg/v1/codec` **frame layer** (a later commit), not here.

## Conventions

- **Registration without `init()`**: `var GzipCompressor = transform.Register(gzipCompressor{})`
  and `var FlateCompressor = transform.Register(flateCompressor{})`.
- **Stateless value singletons** — idempotent re-registration is a no-op.
- **append-to-dst** — `Compress` / `Decompress` seed a `bytes.Buffer` with `dst`
  (compress) or `append` onto `dst` (decompress) so callers can reuse buffers.
- **Errors wrap the stdlib cause** via `errs.Wrap(cause, gzipWrap/flateWrap)` so
  `errors.Is(err, originalCause)` keeps working and the dotted-quad code
  surfaces. Never `fmt.Errorf` / `errors.New`.

## Do NOT

- Add a vendor compressor (zstd / snappy / s2) here — those land as opt-in
  follow-ups behind their own imports (ADR 0014 §Deferred).
- Move the bounded-read cap into `core/transform` — the port stays body-free.
- Add an `init()` — use the package-level `var = transform.Register(...)` form.

## Verification

```sh
bazel test --config=race //internal/service/transform:transform_test
# Fallback
cd internal/service && GOWORK=off go test -race -cover ./transform/...
```
