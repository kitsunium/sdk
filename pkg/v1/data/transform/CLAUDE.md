<!-- updated: 2026-10-05T12:00:00Z -->
# pkg/v1/data/transform/

## Purpose

Public facade for the byte-transform registry (ADR 0014): compress and
decompress with a named `Algorithm`, without the codec package. Importing it
registers the three stdlib schemes of `internal/service/data/transform` —
`gzip`, raw DEFLATE `flate`, and the RFC 1950 `zlib` envelope — and links no
codec. Before it, compression was public only through `pkg/v1/data/codec`'s
self-describing frame, so a program that wanted gzip linked sixteen codecs; the
frame now goes through this package too.

## Surface

| Symbol | Role |
|---|---|
| `Gzip`, `Flate`, `Zlib` | the three `Algorithm`s this package's import registers |
| `Algorithm`, `Compressor`, `BoundedDecompressor` | aliases of `internal/core/data/transform`'s port and its optional extension (ADR 0074) |
| `Lookup(algo)`, `Available()` | the registry, which a `third-party/transform` import (zstd, s2) fills too |
| `Compress(algo, dst, src)`, `Decompress(algo, dst, src)` | resolve and delegate; an unregistered `algo` is `UnknownCompressor`, returned as is, `dst` untouched |
| `DecompressBounded(algo, dst, src, limit)` | at most `limit` bytes of plaintext or `DecompressedTooLarge`: a `BoundedDecompressor` stops at the ceiling; any other scheme is decompressed under its own backstop and judged after |
| `CodeUnknownCompressor`, `CodeCompressedFrameInvalid`, `CodeDecompressedTooLarge` and the sentinels of the same names | the `0.2.5.*` codes the facade and the codec frame return, aliased from the core |

## Why-this-shape

- **A ceiling that holds whoever the scheme is.** `DecompressBounded` is the
  frame layer's old `decompressUnderCeiling`, made public: the three stdlib
  schemes stop at the caller's ceiling, so the ceiling bounds the WORK; a
  scheme that cannot be told one (`third-party/transform`'s zstd and s2, a
  consumer's own) is judged after its own backstop. Declining to tighten the
  work never changes the verdict — `TestTheCeilingHolds` pins both paths with
  the same refusal, `dst` returned as it was.
- **A negative ceiling is zero.** As in the core contract: "unlimited" would
  be a third meaning, and the one that is a memory-exhaustion bug.
- **The scheme sentinels are re-exported like the others.** `GzipFailed`,
  `FlateFailed` and `ZlibFailed` (`0.3.26.*`) are declared by
  `internal/core/data/transform` since ADR 0160, so this facade aliases them
  beside the `0.2.5.*` ones; `TestACorruptStreamIsTheSchemesRefusal` pins that
  a corrupt stream is matched by its scheme's code and sentinel.
- **No codec is linked.** `TestGoListDepsNamesNoCodec` asks `go list -deps`;
  `TestItLinksNoModuleOutsideTheSDK` reads the test binary's modules.

## Do NOT

- Register a scheme here, or give `zlib` a frame id: the frame's `algID` table
  is `pkg/v1/data/codec`'s, frozen at `gzip=0x01` / `flate=0x02`.
- Import `pkg/v1/data/codec` here: the codec package depends on this one.
- Hand-edit `README.md` — regenerate with `make docs-readme`.

## Generated

`facade_gen.go` is kit's (ADR 0166): every alias, re-exported constant and variable, and forwarder of this package is declared in the `facade:` of `design/data/transform.yaml`, doc comments included, and kit writes it. Where this file names another file as holding one of them, read `facade_gen.go`: `doc.go` holds the package comment, which kit writes from the design (ADR 0167), and the hand-written files the declarations of their own. Change a re-export, or its doc comment, in the design and run `kit gen` (then `make api` and `make docs-readme`); `make api-check` fails on a `facade_gen.go` edited by hand.

## Verification

```sh
bazel test --config=race //pkg/v1/data/transform:transform_test
cd pkg && GOWORK=off go test -race ./v1/data/transform/
```

`TestItLinksNoModuleOutsideTheSDK` skips under Bazel, which records no module
information in a binary, and `TestGoListDepsNamesNoCodec` without a go tool on
PATH.

## Reference

- ADR 0014 D1, ADR 0066, ADR 0156 §1; `internal/service/data/transform/CLAUDE.md`
