# internal/service/data/transform — notes

Reference moved out of `CLAUDE.md` (ADR 0253), unchanged and under the same section titles.

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
| `gzip.go`    | `GzipCompressor` singleton + `NewGzipCompressor(level)` + `gzipCompressor` (Algorithm "gzip"); `storedLevel` / `writeLevel`, the level field's zero-is-default representation gzip and flate share (#900) |
| `flate.go`   | `FlateCompressor` singleton + `NewFlateCompressor(level)` + `flateCompressor` (Algorithm "flate") |
| `zlib.go`    | `ZlibCompressor` singleton + `NewZlibCompressor(level)` + `zlibCompressor` (Algorithm "zlib"); `usableLevel`, the one clamp of the three knobs; the block decode's own RFC 1950 envelope — `zlibBody` (header, FDICT id) and `zlibCheckTrailer` (Adler-32) — over the pooled flate decoder, errors the stdlib's own sentinels (#1102) |
| `pool.go`    | the recycled stdlib codecs — `take*`/`release*` per scheme, the per-level writer pools of all three schemes (`newLevelPools`, `levelPool`, #900), and `readerBox` with `boundedStream`, the stream reader built on it (a pooled decoder read under the caller's ceiling — the probe byte at the ceiling is `appendAllBounded`'s detector in stream form); `takeFlateReaderFrom` takes the pool as a `readerBoxPool` so the Reset-fault gate runs on a pool whose Get returns what Put stored — a `sync.Pool` guarantees no such thing (P migration, a collection), and a fixture it missed once poisoned a later flate decode under `bazel coverage` |
| `bounded.go` | `appendAllBounded` — the shared layer-local decompression bound and tail: `dst`'s spare capacity first, then pooled 32 KiB spill chunks (`spillPool`, at most 32 handed back per call) and one growth of `dst` to its final length (#1102); the end of a stream that filled `dst` exactly is found by one byte into the box's `probe` (`probeEnd`), never by taking a chunk (`BENCH.md` §12.2); the ceiling outranks a cut stream's fault (#1432): a decoder hands the last bytes of a cut stream over with its sticky fault, so a read past `max` beside a fault is the overflow (`ceilingFirst`), and `drainBox` judges the decoder's `Close` fault only for a stream that stayed within `max` |
| `stream.go`  | the stream form of the three schemes (ADR 0213): `NewReader` / `NewWriter` on each, and `poolStream` (a pooled per-level encoder until `Close`, over the `resetter` the three stdlib writers share); the reader, `boundedStream`, is `pool.go`'s, beside the `readerBox` it reads through |
| `wrap.go`    | `gzipWrap` / `flateWrap` / `zlibWrap` — the WrapParams restating `core/data/transform`'s `GzipFailed` (0.3.26.1) / `FlateFailed` (0.3.26.2) / `ZlibFailed` (0.3.26.3) |

## Streams (ADR 0213)

Each scheme implements `core/data/transform.StreamingCompressor`: `NewReader(r,
max)` and `NewWriter(w)`, reached from `pkg/v1/data/transform.NewReader` /
`NewWriter`. The stream codecs are the whole-buffer path's pooled ones —
`takeGzipStream` / `takeFlateStream` / `takeZlibStream` share `fill*Box` with
the block takes (gzip's and flate's; zlib's block decode reads its own envelope
over the flate pool's decoder, so `zlibReaderPool` is the stream's alone), the
writer a level's pool — and `Close` hands them back once;
a read or a write after it is the scheme's code over `io.ErrClosedPipe`. The
reader hands over at most `max` bytes and then asks the decoder for one more: a
byte is `DecompressedTooLarge` (sticky), the end is `io.EOF`, after the trailer
is checked. `NewWriter` writes the very bytes `Compress` writes — the
conformance table asserts it for every engine — so either form reads the other.
Measured (`BENCH.md` §14): the stream is as fast as the block form or faster on
4 MiB of JSON, and allocates kilobytes where the block form allocates the
plaintext.

## A level knob never yields an inert compressor (ADR 0031)

`zlib` was the first scheme here with a caller-visible knob; since #900 gzip and
flate have one too — `NewGzipCompressor(level)`, `NewFlateCompressor(level)`,
re-exported with `NewZlibCompressor` by `pkg/v1/data/transform` — so an HTTP
response at `BestSpeed` or an archive at `BestCompression` no longer needs
`compress/gzip` directly. The three share **one clamp**, `usableLevel`, because
`compress/gzip` and `compress/zlib` re-export `compress/flate`'s level constants
(`TestTheThreeLevelBandsAreOne`). It maps to `DefaultCompression` anything that
would not compress:

- a level outside `[HuffmanOnly, BestCompression]`, which the stdlib writers
  reject outright; and
- `NoCompression` (0), which the stdlib **accepts** and which stores the
  payload verbatim — in-range, silent, and exactly the inert policy ADR 0031
  forbids. This is the trap worth naming: the only invalid-looking value is not
  the dangerous one.

Clamp rather than refuse, on ADR 0031's own line: every accepted level
round-trips the caller's bytes identically, so the knob trades ratio against CPU
instead of carrying the caller's intent (contrast `RateLimiterConfig.Rate`,
which *is* the policy) — and `DefaultCompression` is what every registered
singleton encodes at, the floor a reader accepts without being told the number.
`Test_NewZlibCompressor_NeverInert` and `TestEveryLevelKnobNeverYieldsAnInertCompressor`
assert the **observable** outcome (bytes actually shrink, payload round-trips),
never the clamped field, so they survive a change of mechanism. Measured on
4 MiB of JSON log records (`BenchmarkLevel`, `BENCH.md` §13): `BestSpeed`
190 MiB/s at ratio 4.73, the default 85 MiB/s at 5.35, `BestCompression`
13 MiB/s at 5.86 — the same for the three schemes, which share the DEFLATE encoder.

**gzip and flate keep their zero value as the default.** Their level field
stores the clamped level with `DefaultCompression` written as 0 (`storedLevel`)
and read back as -1 (`writeLevel`): `NoCompression` is the one level the clamp
never lets through, so 0 is free, and the registered singleton stays the zero
value `gzipCompressor{}` it always was. `NewGzipCompressor(gzip.DefaultCompression)`
therefore EQUALS `GzipCompressor`, and registering it again is the idempotent
republish, not a boot panic (`TestTheDefaultLevelIsTheSingleton`). zlib stores
its level as is, as before.

**One writer pool per level, per scheme.** `Reset` keeps the level a writer was
built at, for all three stdlib writers, so a writer recycled into another
level's compressor would encode at the wrong ratio without failing.
`TestEveryLevelEncodesAsTheStdlibDoes` runs every level interleaved, twice, and
requires each pooled encode to equal a fresh stdlib writer at that level byte
for byte. The pools are built empty at package load: nothing is constructed
until a level is first used.

Each scheme's `Compress` still surfaces the writer constructor's error, which
the clamp makes unreachable through the constructors. The branch is kept — and
driven by `Test_zlibCompressor_CompressBadLevel` and `TestAnOutOfBandLevelFailsTyped`
through in-package struct literals — so a future caller that bypasses the
constructor fails typed rather than silently.

## The conformance table

`conformance_external_test.go` (#1123) is every contract a registered engine
keeps, written once and run over every Algorithm the registry holds — read from
`coretransform.Available`, never listed: the round trip keeping `dst`, a
malformed stream refused with the engine's code and `dst` back, the caller's
ceiling admitting exactly `max` bytes and refusing one more, and the stream rows
of ADR 0213. The one per-engine fact, its refusal code, is `engineCodes`, and
`TestEveryEngineHasItsCode` fails an engine registered without one.
`TestABrokenEngineFailsTheRow` hands the rows a gzip that ignores the ceiling and
requires the two ceiling rows — and only those — to fail. The layer backstop
under a lowered cap is `Test_decompressBackstop`, one row per engine, white-box
because the cores are unexported. What differs between engines stays in their
files: zlib's envelope (`TestZlibIsNotFlate`, `TestZlibEnvelopeWrapsFlateBody`, and
`zlib_envelope_internal_test.go` — `TestZlibEnvelopeAgreesWithTheStandardLibrary`
holds the package's own RFC 1950 reading to `compress/zlib` on eighteen frames,
`TestZlibEnvelopeHoldsTheCeilingAtItsEdge` the bound's edges).
zstd and s2 are held to the same rows by `third-party/transform`'s own
`conformance_external_test.go`: this test cannot import that module (a module
cycle, and the layer firewall), so the vendor module carries the rows that
transfer — all but `DecompressBounded`'s, until #917, and the stream form
equal to the block form, which s2's framing is not.

`ceiling_external_test.go` (#1432) — `Test_Ceiling_TruncatedPastMax`: gzip, flate
and zlib streams cut at many offsets, judged around what each cut still decodes
to (read from the standard library): a ceiling below it is `DecompressedTooLarge`
and never the cut's `GZIP_FAILED` / `FLATE_FAILED` / `ZLIB_FAILED`, a ceiling at
or above it is the scheme's code, through `DecompressBounded` over a `dst` absent,
sized to the ceiling and sized to the plaintext, and through `NewReader`. The
stream reader already answered in that order — it asks its probe byte before it
looks at the decoder's fault — so its rows pin it; the block path was the one red.
`third-party/transform`'s zstd and s2 were swept the same way and hold the order.
