<!-- updated: 2026-09-09T00:00:00Z -->
# internal/service/transform/

## Purpose

Concrete `core/transform.Compressor` implementations over the Go standard
library: **gzip** (`compress/gzip`), **flate** (raw DEFLATE, `compress/flate`)
and **zlib** (the RFC 1950 envelope, `compress/zlib`). One Go package implements
all three schemes. Blank-importing the package self-registers all three
singletons with the core registry so they become resolvable by `Algorithm`
(ADR 0014 D1). The wave ships **stdlib only** — zstd / snappy / s2 are deferred
opt-in follow-ups behind their own vendor imports, so the `pkg/v1/codec` module
graph stays vendor-free.

Code range: `0.3.26.*` (`0x1a`, ADR 0014).

## `flate` and `zlib` are NOT the same thing

This is the single most likely source of an interop bug in this package, so it
is stated before anything else.

| Scheme | RFC | Wire shape |
|---|---|---|
| `flate` | RFC 1951 | bare DEFLATE bit-stream — no header, no checksum |
| `zlib`  | RFC 1950 | 2-byte CMF/FLG header + the same DEFLATE body + 4-byte Adler-32 trailer |
| `gzip`  | RFC 1952 | 10-byte header (+ optional fields) + DEFLATE body + CRC-32 and size trailer |

HTTP's `Content-Encoding: deflate` (RFC 9110 §8.4.1) names the **zlib envelope**,
*not* the raw DEFLATE stream — the name is a historical misnomer that has
produced interop bugs for two decades. A peer that advertises `deflate` wants
the `zlib` scheme here. `flate` remains the right choice only where the framing
is supplied by the surrounding format (as inside gzip and zlib themselves).

The two are not wire-compatible in either direction, and each has its own error
code so a rejected stream says which envelope it failed under. `TestZlibIsNotFlate`
and `TestZlibEnvelopeWrapsFlateBody` pin both halves of that statement, so a
refactor that quietly turned one into the other would fail rather than ship.

## Contents

| File | Surface |
|---|---|
| `gzip.go`    | `GzipCompressor` singleton + `gzipCompressor` (Algorithm "gzip") |
| `flate.go`   | `FlateCompressor` singleton + `flateCompressor` (Algorithm "flate") |
| `zlib.go`    | `ZlibCompressor` singleton + `NewZlibCompressor(level)` + `zlibCompressor` (Algorithm "zlib") |
| `bounded.go` | `readAllBounded` — the shared layer-local decompression bound |
| `codes.go`   | `CodeGzipFailed` (0.3.26.1), `CodeFlateFailed` (0.3.26.2), `CodeZlibFailed` (0.3.26.3) |
| `errors.go`  | `GzipFailed` / `FlateFailed` / `ZlibFailed` sentinels + `gzipWrap` / `flateWrap` / `zlibWrap` WrapParams |

## Bounded decompression (locked choice)

Each `Decompress` drains its stdlib reader through `io.LimitReader` capped at
`maxDecompressedBytes` (256 MiB). A stream that tries to exceed the cap returns
the scheme's `GzipFailed` / `FlateFailed` / `ZlibFailed` sentinel rather than
driving an OOM. This is the **conservative layer-local backstop** ADR 0014 D1
asks for; the full self-describing decompression-bomb guard (max output size +
max expansion ratio keyed to the frame header, `CodeCompressedFrameInvalid`)
lives in the `pkg/v1/codec` **frame layer** (a later commit), not here. All three
schemes share a DEFLATE body, so all three inherit the same bomb ratio and all
three are covered by `Test_{gzip,flate,zlib}Decompress`.

## The zlib level knob never yields an inert compressor (ADR 0031)

`zlib` is the first scheme here with a caller-visible knob, so it is the first
that could be configured into uselessness. `NewZlibCompressor(level)` **clamps**
to `zlib.DefaultCompression` anything that would not compress:

- a level outside `[zlib.HuffmanOnly, zlib.BestCompression]`, which
  `zlib.NewWriterLevel` rejects outright; and
- `zlib.NoCompression` (0), which the stdlib **accepts** and which stores the
  payload verbatim — in-range, silent, and exactly the inert policy ADR 0031
  forbids. This is the trap worth naming: the only invalid-looking value is not
  the dangerous one.

Clamp rather than refuse, on ADR 0031's own line: every accepted level
round-trips the caller's bytes identically, so the knob trades ratio against CPU
instead of carrying the caller's intent (contrast `RateLimiterConfig.Rate`,
which *is* the policy) — and the sibling gzip/flate schemes already hard-code
`DefaultCompression`, which makes it the floor a reader accepts without being
told the number. `Test_NewZlibCompressor_NeverInert` asserts the **observable**
outcome (bytes actually shrink, payload round-trips), never the clamped field,
so it survives a change of mechanism.

`zlibCompressor.Compress` still surfaces `NewWriterLevel`'s error, which the
clamp makes unreachable through the constructor. The branch is kept — and driven
by `Test_zlibCompressor_CompressBadLevel` through an in-package struct literal —
so a future scheme that bypasses the constructor fails typed rather than
silently.

## Conventions

- **Registration without `init()`**: `var GzipCompressor = transform.Register(gzipCompressor{})`,
  `var FlateCompressor = transform.Register(flateCompressor{})` and
  `var ZlibCompressor = transform.Register(zlibCompressor{level: zlib.DefaultCompression})`.
- **Stateless value singletons** — idempotent re-registration is a no-op.
  `zlibCompressor` carries a level field but is still a *comparable* value fixed
  at construction, so re-registering the same scheme stays a no-op.
- **Scheme options ride on the scheme's constructor**, never on the `Compressor`
  interface (`core/transform.Compressor` doc). `NewZlibCompressor` is that
  constructor; its result is deliberately **not** registered, since the registry
  entry is the one default-level singleton.
- **append-to-dst** — `Compress` / `Decompress` seed a `bytes.Buffer` with `dst`
  (compress) or `append` onto `dst` (decompress) so callers can reuse buffers.
- **Errors wrap the stdlib cause** via `errs.Wrap(cause, gzipWrap/flateWrap/zlibWrap)`
  so `errors.Is(err, originalCause)` keeps working and the dotted-quad code
  surfaces. Never `fmt.Errorf` / `errors.New`.

## Known boundary — zlib has no `pkg/v1/codec` frame algID

The compressed-frame format in `pkg/v1/codec/compressed.go` freezes a 1-byte
`algID` per scheme (`gzip=0x01`, `flate=0x02`) and exports only the `Gzip` /
`Flate` constants. **zlib is registered but not framed**: `MarshalCompressed(f,
"zlib", v)` returns `core/transform.UnknownCompressor`, which is the documented
behaviour for an algorithm with no frame id. Allocating `0x03` is an additive
change to a wire format frozen post-v1.0.0 and belongs to a `pkg/v1` decision,
not to this package. Consumers reach the scheme through
`core/transform.Lookup("zlib")` until then.

## Do NOT

- Add a vendor compressor (zstd / snappy / s2) here — those land as opt-in
  follow-ups behind their own imports (ADR 0014 §Deferred).
- Move the bounded-read cap into `core/transform` — the port stays body-free.
- Add an `init()` — use the package-level `var = transform.Register(...)` form.
- Treat `flate` as the HTTP `deflate` encoding, or register `zlib` under the
  `deflate` name — see the §`flate` and `zlib` are NOT the same thing table.
- Register the result of `NewZlibCompressor` — a distinct scheme claiming the
  taken `"zlib"` Algorithm panics at boot (`core/transform.Register`).

## Verification

```sh
bazel test --config=race //internal/service/transform:transform_test
# Fallback
cd internal/service && GOWORK=off go test -race -cover ./transform/...
```

## Accepted audit findings

- Deferred/accepted low+info audit findings (V73, V74) are recorded in `.claude/contexts/sdk-audit-2026-06-03-accepted.yaml` (2026-06-03 close-out). Each is a deliberate decision or deferred change, not an open bug.
