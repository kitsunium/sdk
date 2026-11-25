# Notes — <!-- updated: 2026-11-03T12:00:00Z -->

Reference moved out of `CLAUDE.md` (ADR 0253), unchanged and under the same section titles. `CLAUDE.md` keeps what to obey; this file holds the rest.

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

### The caller's ceiling (`DecompressBounded`, #917)

Both compressors implement `core/data/transform.BoundedDecompressor`, so a
caller that expects less than the instance's ceiling — consensus decoding a
1 MiB snapshot chunk under its declared length — stops the work at its bound
(`bounded.go`). The instance's ceiling stays the backstop above it
(`DECOMPRESSION_LIMIT_EXCEEDED`); past the caller's, the refusal is
`DecompressedTooLarge`, `dst` handed back.

- **zstd** reads the first frame's header (`zstd.Header`): a size announced
  past `max` is refused before a byte is decoded; a size within it is decoded
  by a second shared decoder built with `WithDecodeAllCapLimit`, into a tail of
  exactly that capacity, which it cannot outgrow — a second frame, or a frame
  whose announced size lies, hits that capacity and falls to the bounded
  stream (`NewReader` under `max`), which grows `dst` 64 KiB at a time and
  never past `max`; so does a frame that announces nothing (what `NewWriter`
  writes).
- **s2** reads the block header's length and refuses past `max` before
  allocating, then decodes as `Decompress` does.

`TestZstdDecompressBoundedRefusesABombBeforeAllocatingIt` measures what a
refusal allocates (`runtime.MemStats.TotalAlloc`, not parallel): a 64 MiB bomb
under a 1 MiB ceiling, refused from the header — 0 bytes —, with its size
forged down to 4 KiB — 5.5 MiB, the decoder's window and the ceiling's worth —,
and as a stream without a size — 10 MiB, the stream decoder's 8 MiB window and
the ceiling: never the 64 MiB. `TestZstdDecompressBoundedHoldsTheCallersCeiling`
holds the boundary (exactly `max` in, one byte less out, into a nil `dst`, a
prefix and spare capacity, one frame and two), and the vendor conformance table
runs the stdlib table's ceiling row over every registered engine.

### Consensus snapshots in zstd (#917)

Consensus links no vendor: a product imports this package and sets
`Config.SnapshotCodec` to `transform.Zstd`, a `BoundedDecompressor`; the cut
names `"zstd"` in `SnapshotMeta.Codec`, and every node that links this package
reads it — the codec the metadata names, looked up in the registry (ADR 0152
D18 "Compression"). `TestAClusterInstallsAZstdSnapshot`
(`snapshot_external_test.go`) runs three nodes of `internal/service/app/consensus`
on its memory doubles and installs a zstd snapshot on a lagging follower; the
follower that does not link zstd stops `VERSION_UNSUPPORTED`, proved in the
consensus suite, a binary that cannot link it
(`TestAFollowerThatDoesNotLinkTheLeadersCodecStops`).
`BenchmarkSnapshotCutAndInstall` measures the cut and the install of a 256 MiB
state with flate and with zstd (`BENCH.md` §Consensus snapshots). The import of
`internal/service/app/consensus` is a test's, beside the stdlib transform's:
the module requires `internal` alone.

### Why 64 MiB and not the stdlib schemes' 256 MiB

Arithmetic, not taste. DEFLATE's maximum expansion is roughly 1 032:1, so
reaching 256 MiB through gzip costs an attacker about 254 KiB of upload. Both
schemes here expand far harder — the two fixtures above are 13 KiB and 28 bytes
— so the same ceiling would be orders of magnitude cheaper to reach. The number
that matters is what an attacker spends, not what the format is called.

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
- **Drained without a copy** (#892). The reader is an `io.WriterTo`:
  `io.Copy` hands the caller's writer each decoded block in one `Write`, from
  where it was decoded — zstd through `Decoder.WriteTo`, s2 through
  `s2Frame` — behind `ceilingWriter`, which lets `limit` bytes through and
  refuses the next one as `Read` does. A failure of the caller's writer ends
  the stream: its own error, unwrapped as `io.Copy` returns it, then sticky. The
  fault is told by the fact that the last `Write` failed, never by comparing
  values (a writer's error of an uncomparable type is returned as is), and a
  writer's `io.EOF` is such a fault: zstd's `Decoder.WriteTo` keeps it and
  returns nil, so `boundedStream.WriteTo` reads `ceilingWriter.failure`
  first; a later `Read` or `WriteTo` finds the stream cut short, never
  `io.EOF`.
- **s2's framing is read by the package** (`s2frame.go`). `s2.Reader` can only
  copy a block into the caller's buffer, and its `DecodeConcurrent` starts
  goroutines even at one; `s2Frame` reads the same chunks with the same bounds,
  checked before anything is allocated, and the same errors (`s2.ErrCorrupt`,
  `ErrCRC`, `ErrUnsupported`), each block's CRC-32C verified before a byte of
  it leaves. It takes `s2.Decode` for a compressed block and `hash/crc32` for
  the checksum, and nothing else of the vendor's stream code. `TestS2FrameReadsWhatTheLibraryReads` and
  `FuzzS2FrameAgainstTheLibrary` hold it to `s2.Reader`, through `Read` and
  `WriteTo`: same bytes, same error. The writer cuts 256 KiB blocks
  (`s2StreamBlockSize`): 1 MiB outgrew a core's L2, so the CRC and the copy
  reread each block from L3; the reader still takes the format's 4 MiB.
- **Measured** (`BENCH.md` §Streams): every stream row within the 10 % of the
  block form at 4 MiB on this box, s2 decompression of random bytes 21 %
  faster than its block form where it was 78 % slower; the bound itself is the
  `tstream` suite of the A/B lane, on the runner, with a floor row
  (`BenchmarkStreamFloor`): a measured base of this implementation's copy and
  CRC-32C, not a proof that every stream must exceed the bound.

## The conformance table (#1123)

`conformance_external_test.go` runs the contracts of the stdlib table
(`internal/service/data/transform/conformance_external_test.go`) over every
Algorithm the registry holds in this test binary — zstd and s2, and the stdlib
three that `transform_bench_test.go`'s import registers — read from
`coretransform.Available`, never listed: the round trip keeping `dst`, a
malformed block refused with the engine's code and `dst` back, and the four
stream rows of ADR 0213 (round trip, the caller's ceiling handed over then
refused and sticky, a malformed or truncated stream refused, a closed stream
refusing with `Close` idempotent), and the caller's ceiling on the block path
(`DecompressBounded` admits exactly `max` and refuses one byte less, #917).
`registeredCodes` is the one per-engine
fact, and `TestEveryRegisteredEngineHasItsCode` fails an engine registered
without one. It is a second copy because it cannot be the first: the
`internal` test cannot import this module (a module cycle, and a layer below
`third-party/` reaching it — `scripts/check-layer-deps.sh`), and a `_test` file
is importable by nobody; one importable suite is #985's. One stdlib row does
not transfer: "the stream form is what `Compress` writes", which s2's framing
format is not (`TestTheStreamFormsAgainstTheBlockForms`).

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

## Contents

| File | Surface |
|---|---|
| `doc.go` | the package comment — kit writes it from the design (ADR 0167) |
| `transform.go` | `DefaultMaxDecompressedBytes`, `checkLimit` |
| `bounded.go` | the caller's ceiling (#917): `DecompressBounded` on both compressors, the zstd header check, the capped decode, the bounded-stream fallback |
| `stream.go` | the stream form (ADR 0213): `NewReader` / `NewWriter` on both compressors, `boundedStream` (an `io.WriterTo` too, #892), `ceilingWriter`, `encoderStream`, the s2 stream pools |
| `s2frame.go` | `s2Frame`, the s2 framing format's decoder `NewReader` reads through — `Read` and `WriteTo`, `s2.Reader`'s bounds and errors (#892) — and `s2StreamBlockSize` |
| `zstd.go` | `Zstd` singleton, `ZstdCompressor` (its shared encoder, decoder and capped decoder), `NewZstdCompressor`, `NewZstdCompressorWithDictionary`, `newZstd`, `ZstdLevel`, `encoderLevel`, `isLimitError`, `frameDictionaryID`, `joinTail` (the tail decode, #1163) |
| `s2.go` | `S2` singleton, `S2Compressor`, `NewS2Compressor` |
| `gen_codes.go` | `0.3.63.1`–`0.3.63.6`; `ZstdFailed`, `S2Failed`, `DecompressionLimitExceeded`, `LimitMisconfigured`, `DictionaryMismatch`, `DictionaryInvalid` — written by kit gen from `design/third-party/transform.yaml` (ADR 0164) |
| `errors.go` | hand-written beside them: `zstdWrap`, `s2Wrap`, `dictionaryInvalidWrap` |
| `BENCH.md` | the numbers, the choice table, and the pprof behind the one optimisation |
