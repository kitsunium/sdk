<!-- updated: 2026-11-03T12:00:00Z -->
# third-party/transform/

> Reference moved to `third-party/transform/NOTES.md` (ADR 0253), under the same section titles: Why third-party/ — and the mechanism, stated correctly, Decompression bombs (the security core of this package), Round-trip, Streams (ADR 0213), The conformance table (#1123), Dictionaries (#900), Contents.

## Purpose

Vendor compressors — **zstd** (RFC 8878) and **s2** — wrapped as
`core/data/transform.Compressor` implementations beside the stdlib gzip/flate/zlib
schemes in `internal/service/data/transform`. Both come from **one** library,
`github.com/klauspost/compress`, which is the reason there is one package here
and not three.

Blank-importing this package registers `"zstd"` and `"s2"` with the core
`transform` registry, so they resolve through `transform.Lookup` exactly as the
stdlib schemes do. Nothing in `internal/*` or `pkg/v1/*` imports it — a consumer
who does not name it never compiles it.

Code range: `0.3.63.*` (`0x3f`, ADR 0066).

## Surface

| Symbol | What it is |
|---|---|
| `Zstd` | registered `"zstd"` singleton at `ZstdFastest` / `DefaultMaxDecompressedBytes` |
| `S2` | registered `"s2"` singleton at `DefaultMaxDecompressedBytes` |
| `NewZstdCompressor(level, maxDecompressedBytes)` | constructor; refuses a non-positive ceiling, clamps an unknown level |
| `NewZstdCompressorWithDictionary(level, maxDecompressedBytes, dict)` | the same with a trained zstd dictionary, copied; refuses an empty or malformed one (`DictionaryInvalid`); not registered (#900, ADR 0066 amended) |
| `NewS2Compressor(maxDecompressedBytes)` | constructor; refuses a non-positive ceiling |
| `ZstdCompressor` / `S2Compressor` | the concrete types; `ZstdCompressor` also has `Close() error` and `DictionaryID() uint32` (0 without a dictionary); both implement `core/data/transform.StreamingCompressor` — `NewReader(r, limit)`, `NewWriter(w)` (ADR 0213) — and `core/data/transform.BoundedDecompressor` — `DecompressBounded(dst, src, max)` (#917) |
| `ZstdLevel` + `ZstdFastest` / `ZstdDefault` / `ZstdBetter` / `ZstdBest` | the four levels the library implements |
| `ZstdAlgorithm` / `S2Algorithm` | the registry keys, `"zstd"` and `"s2"` |
| `DefaultMaxDecompressedBytes` | 64 MiB — what the singletons are built with |

An external consumer can use all of it without importing `internal/*`: the
`core/data/transform.Algorithm` values that appear in signatures are reachable by
inference (`algo := c.Algorithm()`), never by name.
`TestNoInternalImportNeeded` is that claim in compilable form.

## The output bound is a parameter, not an option

Every constructor takes `maxDecompressedBytes` as a **required positional
argument**. A functional option can be omitted; a positional parameter cannot,
and a security bound that can be omitted is not mandatory. This is the whole
reason the package has no `Option` type.

A non-positive value is **refused** (`LimitMisconfigured`, `0.3.63.4`), never
defaulted — ADR 0031's refuse half. Zero has two natural readings, "no limit"
and "refuse everything", which are opposites; a caller who typed it meant one of
them, and defaulting silently grants what the other reader wanted forbidden.

Contrast `ZstdLevel`, in the same package, which **clamps**: every level
round-trips the caller's bytes identically, so a clamped level costs ratio or
CPU and never correctness. Both halves of ADR 0031 live here, side by side, on
purpose.

## Registration on import is legitimate here (ADR 0030 / ADR 0048 do not bite)

ADR 0030 refuses an SDK default that writes to `os.Stdout`; ADR 0048 refuses
registering an OTLP emitter because "arming a network client from an import is
worse than the stdout hazard". Both refusals are about an import **acquiring a
resource or emitting on a channel the process shares**.

A registry insertion is neither. It opens no descriptor, writes nothing, and
only makes `Lookup("zstd")` succeed where it previously failed. And the specific
risk this package could have carried was checked rather than waved away: a zstd
`Encoder`/`Decoder` built at import time acquires **zero goroutines** and a few
kilobytes of heap (measured: 8 272 B and 3 792 B), with the window buffers
allocated lazily on first use. The capped decoder `DecompressBounded` decodes
a sized frame with (#917) is a second `Decoder`: 3 792 B more, zero
goroutines, measured on the singleton's options.

Had construction spawned a `GOMAXPROCS`-sized goroutine pool at import, the
answer would have been different — and the honest resolution would have been a
constructor-only package with no registered singleton.

Registration uses a package-level `var`, never `init()`, matching
`core/data/transform`'s documented convention and the stdlib sibling.

## What is NOT framed

`pkg/v1/data/codec`'s compressed-frame format (`compressed.go`) freezes a 1-byte
`algID` per scheme — `gzip=0x01`, `flate=0x02` — and exports only the `Gzip` and
`Flate` constants. **Neither scheme here has a frame id**, so
`codec.MarshalCompressed(f, "zstd", v)` returns `core/data/transform.UnknownCompressor`.

That is the documented behaviour and the existing precedent: `zlib` has been
registered-but-not-framed since ADR 0014, for the stated reason that allocating
a new `algID` changes a wire format frozen post-v1.0.0 and is a `pkg/v1`
decision, not a scheme package's. Consumers reach these schemes through their
exported singletons.

The frame layer's own bomb guard is worth noting if that ever changes: its
expansion-ratio bound is 1 000:1, which DEFLATE can barely reach and which both
schemes here clear by three orders of magnitude on repetitive data. Framing zstd
without revisiting that ratio would make the guard bite legitimate traffic.

## Error codes (range `0.3.63.*`)

| Code | Var | Trigger |
|---|---|---|
| `0.3.63.1` | `ZstdFailed` | malformed / truncated zstd stream, checksum mismatch |
| `0.3.63.2` | `S2Failed` | corrupt s2 block header, payload not honouring its header, input the block format cannot address |
| `0.3.63.3` | `DecompressionLimitExceeded` | the configured output ceiling refused the payload |
| `0.3.63.4` | `LimitMisconfigured` | `maxDecompressedBytes <= 0` at construction |
| `0.3.63.5` | `DictionaryMismatch` | a zstd frame names a dictionary the compressor does not hold — refused before decoding, never a corrupt payload; fields `frame_dictionary_id`, `dictionary_id` |
| `0.3.63.6` | `DictionaryInvalid` | `NewZstdCompressorWithDictionary` given an empty dictionary or bytes that are not one |

## Schemes considered and rejected

Recorded so the question is not reopened without new evidence. Full numbers in
the ADR; the measurements are on this box, `-benchtime=1s`, 4 MiB corpora.

| Candidate | Verdict | Why |
|---|---|---|
| `github.com/andybalholm/brotli` | **rejected** | pure Go and dependency-free in the build graph, but 32.9 MB/s compressing (3.1× slower than stdlib gzip) and 179.8 MB/s decompressing (slower than gzip) for a 4 % ratio gain over `zstd-better`; **+2.75 MiB of binary**; drags `net/http` into the build graph via its `http.go`. Its only unique value is the `Content-Encoding: br` wire format, and the SDK has **no** content negotiation anywhere (`grep -r 'Accept-Encoding' internal/ pkg/` is empty), so there is nothing to hook it to. |
| `github.com/pierrec/lz4/v4` | **rejected** | pure Go, dependency-free, +0.08 MiB. But its niche is contested and lost: vs `s2`, ratio 3.68 vs 3.55 (+3.7 %, its only win) against compression 316 vs 706 MB/s and decompression 741 vs 1 427 MB/s, 36 allocations vs 1. s2 comes from the module already taken for zstd, so lz4 would add a whole module to be beaten on three axes of four. |
| `s2` in place of lz4 | **taken** | same module as zstd, zero marginal dependency, and it is what the "speed / latency" column actually wants. |

**Reopen brotli if** the `net` domain grows `Accept-Encoding` negotiation. It
would then need its own package and its own `PP` range — not a slot in this one,
because its 2.75 MiB must not be linked by a consumer who came for zstd.

## Conventions

- **Registration without `init()`**: `var Zstd = coretransform.Register(mustZstd())`.
- **Options ride on the constructor**, never on the `Compressor` interface.
- **append-to-dst** — both verbs preserve the caller's `dst` prefix and append.
  `dst` and `src` must not overlap.
- **Errors wrap the library cause** via `errs.Wrap(cause, zstdWrap/s2Wrap)` so
  `errors.Is(err, originalCause)` keeps working. Never `fmt.Errorf` / `errors.New`.
- **`ZstdCompressor` is a pointer type** (it owns a shared encoder/decoder), so
  registry idempotence is pointer identity. A second, distinct compressor
  claiming `"zstd"` still panics at boot, which is the intent.
- **The singleton is never `Close`d** — it is owned by the process. A caller who
  builds its own must close it.

## Do NOT

- Add these to `pkg/v1/data/codec`'s blank imports, or to anything under `internal/*`
  or `pkg/v1/*`. The isolation is the feature; `scripts`-free proof is
  `GOWORK=off go list -deps ./...` per module, which must return zero hits for
  `klauspost`.
- Register `s2` under the name `"snappy"`. `s2.Encode` emits extensions a Snappy
  decoder does not read, so the name would promise interop the bytes do not
  honour — the mistake HTTP made with `deflate`.
- Add a level knob to `s2`. Its reason for existing is throughput; the levels
  that buy ratio land in zstd's territory at zstd's cost.
- Turn `maxDecompressedBytes` into a functional option, or default a
  non-positive one.
- Allocate a `pkg/v1/data/codec` frame `algID` from here — that is a `pkg/v1`
  decision (see §What is NOT framed).
- Ship a zstd dictionary, or register a dictionary compressor. A dictionary is
  the caller's artefact, trained on the caller's data and versioned by its ID
  (ADR 0066, amended 2026-11-03): the SDK takes one at construction, copies it,
  and refuses a frame naming another — it never chooses one.

## Verification

```sh
# from the module root, as a consumer builds it (ADR 0157)
cd third-party/transform
GOWORK=off go test -race -cover .                  # 95.4% of statements
GOWORK=off go test -run TestReportRatios -v .
GOWORK=off go test -run '^$' -bench=. -benchmem .
# fuzz_external_test.go (#1159): no panic, a typed code on every refusal
# (DICTIONARY_MISMATCH included: a frame may name a dictionary, #900), the
# result within dst + a 64 KiB ceiling with dst's prefix intact, and an exact
# round trip at every zstd level and for s2 — each into four dst shapes, so the
# tail decode of #1163 is fuzzed both landing in dst's spare capacity and
# outgrowing it, and under a prefix longer than the ceiling; the seeds — bombs
# past the ceiling, truncated frames, testdata/fuzz's frame naming an unknown
# dictionary — run with the ordinary suite
GOWORK=off go test -run '^$' -fuzz '^FuzzDecompressNeverExceedsCeiling$' -fuzztime 30s .
GOWORK=off go test -run '^$' -fuzz '^FuzzRoundTripIsExact$' -fuzztime 30s .
cd ../..
ktn-linter lint --skip-phases=tests ./third-party/transform/...
# Under Bazel
bazel test --config=race //third-party/transform:transform_test
```

Uncovered statements are the deliberately unreachable ones: the `mustZstd` /
`mustS2` panic arms (reachable only if this package's own constants are wrong),
the two library-option error arms in `NewZstdCompressor`, and `s2.Compress`'s
`bound < 0` arm, which needs a source slice larger than 4 GiB and is not
exercisable inside this VM's memory budget.
