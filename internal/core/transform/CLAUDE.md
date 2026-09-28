<!-- updated: 2026-09-28T19:19:15Z -->
# internal/core/transform/

## Purpose

Declares the **byte-transform port** of the SDK: the `Compressor` contract and
the process-wide registry mapping an `Algorithm` to a registered `Compressor`.
The **fifth** core sibling beside `codec` / `writer` / `crypto` / `logger`,
admitted by ADR 0014 D1. Peer of `internal/core/codec` — the registry resolves
an `Algorithm` to a `Compressor` exactly as codec resolves a `Format` to a
`Codec`.

Compression is modelled as a **parallel registry, never a codec `Format`**:
adding `gzip` as a `Format` would break the frozen `any→[]byte` codec contract
and the append-only Format set (ADR 0014 §Why-not). The compression *verbs*
(`MarshalCompressed` / `UnmarshalCompressed`) and the self-describing
compressed-frame format live at `pkg/v1/codec`; this package declares only the
port + registry.

No algorithm bodies and no vendor types live here. Concrete schemes live under
`internal/service/transform/` (stdlib gzip/flate/zlib) and, for the vendor
schemes `zstd` and `s2`, under `third-party/transform/` (ADR 0066), each
self-registering via a package-level `var` at import — no `init()`. `flate` (raw DEFLATE, RFC
1951) and `zlib` (the RFC 1950 envelope HTTP misnames `deflate`) are distinct
`Algorithm`s, not aliases — see that package's `CLAUDE.md`.

Code range: `0.2.5.*` (ADR 0014).

## Contents

| File | Surface |
|---|---|
| `transform.go` | `Algorithm` typed string (`String` / `Known`) + `Compressor` interface (`Algorithm` / `Compress` / `Decompress`) + `BoundedDecompressor`, the optional extension adding `DecompressBounded(dst, src, max)`, detected by type assertion |
| `registry.go`  | `snapshot.Value`-backed Compressor registry: `Register` / `Lookup` / `Available` |
| `codes.go`     | `Code*` constants — range 0.2.5.\* |
| `errors.go`    | `UnknownCompressor` (0.2.5.1), `CompressionFailed` (0.2.5.2), `DecompressionFailed` (0.2.5.3), `CompressedFrameInvalid` (0.2.5.4), `DuplicateRegistration` (0.2.5.5), `DecompressedTooLarge` (0.2.5.6) |

`CodeCompressedFrameInvalid` is allocated here so the whole `0.2.5.*` block is
declared in one place per ADR 0014, but its **emitter** is the `pkg/v1/codec`
frame layer — the decompression-bomb guard lives in the frame, not in this port.
`DecompressedTooLarge` is what a scheme's `DecompressBounded` returns past the
caller's ceiling: it states only that the ceiling was exceeded, never that the
stream was well formed, because the decode stops before the trailer.

## Conventions

- **`snapshot.Value`, not `sync.Map`** — schemes register once at import, then
  it is read-many (ADR 0011). `Register` publishes via `Value.Update`
  (mutex-serialised); `Lookup` is lock-free.
- **Registration is a package-level `var`, never `init()`** (`KTN-FUNC-NOINIT`):
  `var GzipCompressor = transform.Register(gzipCompressor{})` in each scheme.
- **Idempotent re-registration** of the same scheme is fine; a *distinct* scheme
  claiming a taken `Algorithm` **panics at boot** with the dotted-quad code, and
  so does an unusable one — a typed nil pointer or a non-comparable value
  (`internal/kernel/plugin`, ADR 0071).
- **`Algorithm("")` is the reserved invalid zero value** — `Known()` is false.
- **append-to-dst convention** — `Compress` / `Decompress` follow the stdlib
  shape (`dst` may be nil) so callers can reuse buffers on the hot path. `src`
  must not overlap `dst`'s spare capacity (`dst[len(dst):cap(dst)]`): every
  scheme writes there while still reading `src`, and no scheme can check it
  cheaply.

## Do NOT

- Add a `MustRegister` or deregistration API — the registry is append-only and
  `Register` already panics on conflict.
- Model compression as a codec `Format` — that breaks the frozen `any→[]byte`
  contract (ADR 0014 §Why-not).
- Put a scheme implementation or a vendor import here — those live in
  `service/transform/`.
- Grow a new core sibling without first widening the layer purpose via an ADR
  (the gate `transform` cleared via ADR 0014, like `writer` via ADR 0012 and
  `crypto` via ADR 0013).

## Verification

```sh
bazel test --config=race //internal/core/transform:transform_test
# Fallback
cd internal/core && GOWORK=off go test -race -cover ./transform/...
```
