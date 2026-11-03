<!-- updated: 2026-11-03T12:00:00Z -->
# third-party/transform/

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

## Why `third-party/` — and the mechanism, stated correctly

**Not because the library drags a dependency graph.** Measured, `GOWORK=off`,
Go 1.27.1:

| Question | Answer |
|---|---|
| transitive module requires | **none** — `klauspost/compress`'s `go.mod` declares `module`, `go`, and a `retract` block, nothing else |
| pulls `golang.org/x/sys` (banned SDK-wide) | **no** |
| pure Go / cgo | pure Go; `go list -deps -json` reports zero `CgoFiles` |
| cross-compiles on the `cross-build` matrix | **12 of 12** cells, `CGO_ENABLED=0` (illumos and solaris added with ADR 0144) |

So the criterion that quarantines HCL — *introducing* a banned module into a
module that forbids it (ADR 0034 §Decision.2) — **does not apply here**. The
placement rests on ADR 0034 §Decision.3's *other* criterion: it imposes a
dependency on consumers who never use the integration. `internal/service` and
`pkg` are dep-light on purpose, and a consumer who wants a logger should not
resolve, download, verify and link a compression library to get one.
`third-party/transform` is a module of its own (ADR 0157), which nothing in the
SDK requires, so the cost is paid by the blank-import and by nobody else — and
a consumer of these two compressors inherits no other vendor.

**Do not cite the downgrade narrative.** ADR 0022's "hcl downgrades `x/sys`"
sentence was retired as a placement rule by ADR 0034: under minimal version
selection a dependency requiring a *lower* version cannot lower a higher
requirement that is still present. A placement argument here cites the
consumer-graph criterion, or it cites a measurement.

## Surface

| Symbol | What it is |
|---|---|
| `Zstd` | registered `"zstd"` singleton at `ZstdFastest` / `DefaultMaxDecompressedBytes` |
| `S2` | registered `"s2"` singleton at `DefaultMaxDecompressedBytes` |
| `NewZstdCompressor(level, maxDecompressedBytes)` | constructor; refuses a non-positive ceiling, clamps an unknown level |
| `NewZstdCompressorWithDictionary(level, maxDecompressedBytes, dict)` | the same with a trained zstd dictionary, copied; refuses an empty or malformed one (`DictionaryInvalid`); not registered (#900, ADR 0066 amended) |
| `NewS2Compressor(maxDecompressedBytes)` | constructor; refuses a non-positive ceiling |
| `ZstdCompressor` / `S2Compressor` | the concrete types; `ZstdCompressor` also has `Close() error` and `DictionaryID() uint32` (0 without a dictionary); both implement `core/data/transform.StreamingCompressor` — `NewReader(r, limit)`, `NewWriter(w)` (ADR 0213) |
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

## Decompression bombs (the security core of this package)

| Scheme | Where the refusal happens | Cost of refusing |
|---|---|---|
| `zstd` | in the decoder, from `WithDecoderMaxMemory` — either the declared window or the running decoded size | no full materialisation |
| `s2` | in this package, from `s2.DecodedLen` — the block header's varint length, read before any decode | a few byte loads |

Measured by the package's own tests, with the fixture ratios printed so a
future change that made them harmless would be visible rather than silently
green:

- `TestZstdRefusesDecompressionBomb` — **13 317 bytes → 64 MiB (5 039:1)**,
  refused.
- `TestS2RefusesDecompressionBombBeforeDecoding` — **28 bytes → 64 MiB
  (2 396 745:1)**, refused from the header.
- `TestZstdRefusesMultiFrameBomb` — 64 concatenated frames, each individually
  *under* the ceiling, 6 912 compressed bytes claiming 32 MiB against a 1 MiB
  ceiling. Refused. This is the one that had to be measured rather than assumed:
  a ceiling enforced **per frame** would have accepted every frame and
  materialised 64× the bound without ever violating it. `WithDecoderMaxMemory`
  is enforced across the whole `DecodeAll` call.
- `TestBoundaryIsTheDeclaredSize` — exactly at the ceiling is accepted, one byte
  past is refused, for both schemes, into a nil `dst` and into one already
  longer than the ceiling (#1163): the ceiling counts the payload, never
  `len(dst)`.
- `TestRegisteredSingletonsAreBounded` — the guard is not opt-in: the singletons
  an import registers already carry the ceiling.

**A bomb has its own code.** `DECOMPRESSION_LIMIT_EXCEEDED` (`0.3.63.3`) is
distinct from `ZSTD_FAILED` / `S2_FAILED`, because "someone sent bytes we could
not parse" and "someone sent bytes engineered to exhaust this process" are
different events for whoever reads the logs. Sharing one code makes the second
invisible inside the first, and an alert built on it fires on every scanner that
pokes the endpoint. `TestCorruptInputIsNotReportedAsABomb` pins both directions.

A refusal returns `dst` at its original length — never a partial payload.

### Why 64 MiB and not the stdlib schemes' 256 MiB

Arithmetic, not taste. DEFLATE's maximum expansion is roughly 1 032:1, so
reaching 256 MiB through gzip costs an attacker about 254 KiB of upload. Both
schemes here expand far harder — the two fixtures above are 13 KiB and 28 bytes
— so the same ceiling would be orders of magnitude cheaper to reach. The number
that matters is what an attacker spends, not what the format is called.

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
allocated lazily on first use.

Had construction spawned a `GOMAXPROCS`-sized goroutine pool at import, the
answer would have been different — and the honest resolution would have been a
constructor-only package with no registered singleton.

Registration uses a package-level `var`, never `init()`, matching
`core/data/transform`'s documented convention and the stdlib sibling.

## Round-trip

`TestRoundTripIsExact` runs `Decompress(Compress(x)) == x` over nine shapes —
empty, single byte, ASCII, JSON-ish, all-zeros, all-`0xff`, incompressible,
binary with NULs and newlines, invalid UTF-8 — for both schemes.

**Both schemes are bijective on this port**: they are byte-transforms, not
schema-bound codecs, so there is no `form`-style repetition ambiguity and no
`multipart`-style out-of-band boundary. Nothing here is non-bijective.

Two adjacent properties are pinned because they are the ways this could
silently stop being true:

- `TestCompressPreservesDstPrefix` / `TestDecompressPreservesDstPrefix` — the
  append-to-dst convention. `s2.Encode` and `s2.Decode` treat their first
  argument as **scratch**, so handing them the caller's `dst` directly would
  destroy the bytes already in it. That bug is invisible to every test that
  passes `dst=nil`.
- `TestTheCeilingCountsThePayloadNotDst` — a `dst` longer than the ceiling
  still takes a payload under it, the prefix intact (#1163). zstd's library
  counts `len(dst)` into its own budget, so `Decompress` hands `DecodeAll` the
  tail `dst[len(dst):]` — a zero-length slice over the spare capacity — and the
  payload lands in that capacity when it fits: no allocation, no copy
  (`TestDecompressIntoSpareCapacityAllocatesNothing`, alloc lane). s2 sizes its
  tail from the block header and always did.
- `TestSchemesAreNotWireCompatible` — an s2 block is refused by zstd and a zstd
  frame by s2. The `flate`-versus-`zlib` interop trap `internal/service/data/transform`
  documents at length, checked before it can be repeated with two new names.

## Streams (ADR 0213)

Both schemes implement `core/data/transform.StreamingCompressor`, so
`pkg/v1/data/transform.NewReader("zstd", r, limit)` reaches them once this
package is imported. The reader (`boundedStream`, `stream.go`) hands over at
most `limit` bytes, then `DecompressedTooLarge` — counted, since a stream
declares no length — and classifies the library's errors as the block path
does: a window past the compressor's ceiling is `DECOMPRESSION_LIMIT_EXCEEDED`
at the frame's header (`WithDecoderMaxMemory` is the stream decoder's window
bound), a frame naming an unknown dictionary `DICTIONARY_MISMATCH`.

- **No goroutine.** zstd is built with `WithDecoderConcurrency(1)` /
  `WithEncoderConcurrency(1)` and s2 with `WriterConcurrency(1)`: the codec runs
  in the caller's goroutine (`TestAStreamRoundTrips` counts them).
- **Pooled.** zstd keeps one pool of stream decoders and one of encoders per
  compressor, built with its options and dictionary; s2 one shared pool of
  each. `Close` hands them back once, unbound from the caller's reader or writer.
- **s2's stream form is not its block.** A block declares its length up front;
  a stream cannot, so s2 streams are the framing format — chunked, CRC-32C per
  chunk — which `Decompress` refuses and `NewReader` refuses the block form of,
  both as `S2_FAILED` (`TestTheStreamFormsAgainstTheBlockForms`). A zstd stream
  is frames, which `Decompress` reads.
- **Measured** (`BENCH.md` §Streams): within 3 % of the block form for zstd at
  4 MiB, faster on incompressible bytes; s2 within 10 % on JSON, 78 % slower on
  random bytes, where decoding is a copy and the stream's CRC-32C is the work.

## The conformance table (#1123)

`conformance_external_test.go` runs the contracts of the stdlib table
(`internal/service/data/transform/conformance_external_test.go`) over every
Algorithm the registry holds in this test binary — zstd and s2, and the stdlib
three that `transform_bench_test.go`'s import registers — read from
`coretransform.Available`, never listed: the round trip keeping `dst`, a
malformed block refused with the engine's code and `dst` back, and the four
stream rows of ADR 0213 (round trip, the caller's ceiling handed over then
refused and sticky, a malformed or truncated stream refused, a closed stream
refusing with `Close` idempotent). `registeredCodes` is the one per-engine
fact, and `TestEveryRegisteredEngineHasItsCode` fails an engine registered
without one. It is a second copy because it cannot be the first: the
`internal` test cannot import this module (a module cycle, and a layer below
`third-party/` reaching it — `scripts/check-layer-deps.sh`), and a `_test` file
is importable by nobody; one importable suite is #985's. Two stdlib rows do not
transfer: the caller's ceiling on the block path (`DecompressBounded`, which
these engines lack until #917 — their block ceiling is the instance's, held by
`bomb_external_test.go`) and "the stream form is what `Compress` writes", which
s2's framing format is not (`TestTheStreamFormsAgainstTheBlockForms`).

## Dictionaries (#900)

`NewZstdCompressorWithDictionary(level, max, dict)` builds a zstd compressor
that encodes with a trained dictionary — the format `zstd --train` and
`zstd.BuildDict` write: a magic number, a non-zero ID, the entropy tables. It is
the largest ratio win available on the payloads a wire compressor serves:
measured on the JSON corpus (`BenchmarkZstdDictionary`, `BENCH.md`), the ratio
of a 256-byte record goes from 1.37 to **2.78**, of 1 KiB from 2.55 to 4.19, of
4 KiB from 4.05 to 5.13 — paid for in compression time (3–4× at 4 KiB), not in
decompression.

- **The dictionary is the caller's.** It is trained on the caller's data and the
  SDK ships none; the constructor copies it (the library keeps slices of what it
  loads), and its result is not registered — `"zstd"` in the registry stays the
  dictionary-less singleton.
- **A frame names its dictionary, and a reader without it refuses.** Every frame
  the compressor writes carries the dictionary's ID; a reader that does not hold
  that ID — a compressor with another dictionary, with none, the registered
  `Zstd` — returns `DictionaryMismatch` (`0.3.63.5`) before decoding a byte, its
  fields naming the frame's ID and the reader's. A dictionary frame read without
  its dictionary is never decoded into a corrupt payload
  (`TestADictionaryFrameDecodesOnlyWithItsDictionary`). A frame without a
  dictionary decodes through any reader.
- **The ID is the version.** A new dictionary gets a new ID; a reader meeting an
  old frame learns from the error which one it needs. `DictionaryID()` reports a
  compressor's, for storing beside the payloads.
- **Refused, never defaulted.** An empty dictionary, bytes without the magic, a
  truncated one and an ID of zero (which a frame cannot name) are
  `DictionaryInvalid` (`0.3.63.6`) at construction (`TestTheDictionaryIsRefusedWhenItIsNotOne`).
- **The window is bounded by the ceiling**, as without a dictionary:
  `WithDecoderMaxMemory(maxDecompressedBytes)` also caps the window a frame may
  declare, so no separate `WithDecoderMaxWindow` is set.

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

## Contents

| File | Surface |
|---|---|
| `doc.go` | the package comment — kit writes it from the design (ADR 0167) |
| `transform.go` | `DefaultMaxDecompressedBytes`, `checkLimit` |
| `stream.go` | the stream form (ADR 0213): `NewReader` / `NewWriter` on both compressors, `boundedStream`, `encoderStream`, the s2 stream pools |
| `zstd.go` | `Zstd` singleton, `ZstdCompressor`, `NewZstdCompressor`, `NewZstdCompressorWithDictionary`, `newZstd`, `ZstdLevel`, `encoderLevel`, `isLimitError`, `frameDictionaryID`, `joinTail` (the tail decode, #1163) |
| `s2.go` | `S2` singleton, `S2Compressor`, `NewS2Compressor` |
| `gen_codes.go` | `0.3.63.1`–`0.3.63.6`; `ZstdFailed`, `S2Failed`, `DecompressionLimitExceeded`, `LimitMisconfigured`, `DictionaryMismatch`, `DictionaryInvalid` — written by kit gen from `design/third-party/transform.yaml` (ADR 0164) |
| `errors.go` | hand-written beside them: `zstdWrap`, `s2Wrap`, `dictionaryInvalidWrap` |
| `BENCH.md` | the numbers, the choice table, and the pprof behind the one optimisation |

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
