<!-- updated: 2026-11-03T22:00:00Z -->
# internal/service/data/transform/

## Purpose

Concrete `core/data/transform.Compressor` implementations over the Go standard
library: **gzip** (`compress/gzip`), **flate** (raw DEFLATE, `compress/flate`)
and **zlib** (the RFC 1950 envelope, `compress/zlib`). One Go package implements
all three schemes. Blank-importing the package self-registers all three
singletons with the core registry so they become resolvable by `Algorithm`
(ADR 0014 D1). Its public door is `pkg/v1/data/transform` (`Compress`,
`Decompress`, `DecompressBounded`, `Lookup`), which `pkg/v1/data/codec`'s
compressed frame goes through too. This package ships **stdlib only**, so the
SDK module's graph stays vendor-free. The vendor compressors ADR 0014 deferred have since
landed **outside** this module, in `third-party/transform` — **zstd** and **s2**
over `github.com/klauspost/compress` (ADR 0066). They register into the same
`core/data/transform` registry, so `transform.Lookup("zstd")` resolves once a
consumer blank-imports that package and never before. `snappy` was closed by
substitution: s2 reads Snappy streams, and ADR 0066 §Why-not refuses to register
under the `"snappy"` NAME because the block encoder emits extensions a Snappy
decoder does not read.

Code range: `0.3.26.*` (`0x1a`, ADR 0014) — `GZIP_FAILED`, `FLATE_FAILED` and
`ZLIB_FAILED`, declared since ADR 0160 with the port's `0.2.5.*` in
`internal/core/data/transform`, under the values this package allocated. This
package declares no code: `wrap.go` restates each sentinel in the
`errs.WrapParams` a scheme attaches to a stdlib cause.

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
| `zlib.go`    | `ZlibCompressor` singleton + `NewZlibCompressor(level)` + `zlibCompressor` (Algorithm "zlib"); `usableLevel`, the one clamp of the three knobs |
| `pool.go`    | the recycled stdlib codecs — `take*`/`release*` per scheme, the per-level writer pools of all three schemes (`newLevelPools`, `levelPool`, #900), and `readerBox` with `boundedStream`, the stream reader built on it (a pooled decoder read under the caller's ceiling — the probe byte at the ceiling is `appendAllBounded`'s detector in stream form); `takeFlateReaderFrom` takes the pool as a `readerBoxPool` so the Reset-fault gate runs on a pool whose Get returns what Put stored — a `sync.Pool` guarantees no such thing (P migration, a collection), and a fixture it missed once poisoned a later flate decode under `bazel coverage` |
| `bounded.go` | `appendAllBounded` — the shared layer-local decompression bound and tail: `dst`'s spare capacity first, then pooled 32 KiB spill chunks (`spillPool`, at most 32 handed back per call) and one growth of `dst` to its final length (#1102) |
| `stream.go`  | the stream form of the three schemes (ADR 0213): `NewReader` / `NewWriter` on each, and `poolStream` (a pooled per-level encoder until `Close`, over the `resetter` the three stdlib writers share); the reader, `boundedStream`, is `pool.go`'s, beside the `readerBox` it reads through |
| `wrap.go`    | `gzipWrap` / `flateWrap` / `zlibWrap` — the WrapParams restating `core/data/transform`'s `GzipFailed` (0.3.26.1) / `FlateFailed` (0.3.26.2) / `ZlibFailed` (0.3.26.3) |

## Streams (ADR 0213)

Each scheme implements `core/data/transform.StreamingCompressor`: `NewReader(r,
max)` and `NewWriter(w)`, reached from `pkg/v1/data/transform.NewReader` /
`NewWriter`. The stream codecs are the whole-buffer path's pooled ones —
`takeGzipStream` / `takeFlateStream` / `takeZlibStream` share `fill*Box` with
the block takes, the writer a level's pool — and `Close` hands them back once;
a read or a write after it is the scheme's code over `io.ErrClosedPipe`. The
reader hands over at most `max` bytes and then asks the decoder for one more: a
byte is `DecompressedTooLarge` (sticky), the end is `io.EOF`, after the trailer
is checked. `NewWriter` writes the very bytes `Compress` writes — the
conformance table asserts it for every engine — so either form reads the other.
Measured (`BENCH.md` §14): the stream is as fast as the block form or faster on
4 MiB of JSON, and allocates kilobytes where the block form allocates the
plaintext.

## Bounded decompression (locked choice)

Each `Decompress` drains its stdlib reader through `appendAllBounded`, which
counts what it reads and stops one byte past `maxDecompressedBytes` (256 MiB). A stream that tries to exceed the cap returns
the scheme's `GzipFailed` / `FlateFailed` / `ZlibFailed` sentinel rather than
driving an OOM. This is the **conservative layer-local backstop** ADR 0014 D1
asks for; the full self-describing decompression-bomb guard (max output size +
max expansion ratio keyed to the frame header, `CodeCompressedFrameInvalid`)
lives in the `pkg/v1/data/codec` **frame layer** (a later commit), not here. All three
schemes share a DEFLATE body, so all three inherit the same bomb ratio and all
three are covered by `Test_decompressBackstop`, one row per scheme.

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

## Cost

**Constructing a stdlib codec dominated every small call, and it is now pooled.**
A CPU profile of a 256-byte gzip put `runtime.memclrNoHeapPointers` at 18.83 % —
zeroing tables the call was about to overwrite — and an allocation profile put
**99.94 %** of the bytes in `compress/flate` construction. `pool.go` recycles both
directions through `kernel/concur/recycler`. Medians of three runs, in `BENCH.md`:

| verb | payload | before | after |
|---|---:|---:|---:|
| gzip Compress | 256 B | 493 060 ns / 1 076 250 B | **10 175 ns / 258 B** |
| gzip Compress | 1 MiB | 2 934 639 ns / 1 084 052 B | 2 667 901 ns / 10 425 B |
| gzip Decompress | 256 B | 19 935 ns / 42 024 B | **6 581 ns / 840 B** |

**The gain is a function of payload size, not of scheme**: an encoder costs ~1.076 MB
to build whatever it then compresses, so pooling is 48× at 256 B and 1.10× at 1 MiB.
Output was verified byte-identical before and after across all 30 scheme × corpus ×
size combinations by SHA-256 — the pool changes cost, never bytes.

It is also a function of the caller's **GC rate**, which is why the round-trip row did
not add up at first: the runtime empties every `sync.Pool` at each collection, so a
fraction of iterations rebuild the encoder. With `GOGC=off` the arithmetic closes to
8 bytes out of 19 735.

Two measurements that change how the package should be read:

- **Do not compress below ~128 bytes.** `compress/flate` stores ≤32 B verbatim, emits a
  Huffman-only block for 33–127 B, and only above 128 B looks for matches — measured at
  127 B random = 12 224 ns against 128 B random = **1 129 ns, 10.8× on one byte**. Below
  128 B the output is larger than the input on every corpus.
- **The old zlib level sweep was measuring the constructor.** Before pooling
  `HuffmanOnly` looked like the fastest level, because its writer is the cheapest to
  build; with construction off the hot path `BestSpeed` is fastest and `HuffmanOnly` is
  3.98× slower with a 107× worse ratio. `NoCompression` measures identically to
  `DefaultCompression` on time *and* ratio — the §zlib-level clamp, confirmed rather
  than asserted.

The bound in `bounded.go` used to cost a flat **+24 B and +1 alloc at every
size** (the `io.LimitReader` struct); since #1102 it is a counter and costs
neither.

**Decompression lands in `dst`'s capacity** (#1102, `BENCH.md` §12). The
plaintext is read into `dst`'s spare capacity first, in place; what does not fit
goes into pooled 32 KiB chunks and `dst` grows once, to its final length. A
`dst` whose capacity holds the payload costs **zero allocations** for gzip and
flate (alloc lane: `TestDecompressIntoASizedDstAllocatesNothing`), and **one of
4 bytes for zlib** — `compress/zlib`'s `Reset` builds a fresh Adler-32 digest
per stream; a nil `dst` costs one allocation of the payload's size
(`TestDecompressIntoANilDstAllocatesThePayloadOnce`), where `io.ReadAll`'s
doubling chain and the copy after it cost 3.5× the plaintext. The compressed
input is read through a `bytes.Reader` held in the pooled decoder's box
(`readerBox.in`), not a `bytes.NewReader` per call. The spill pool keeps at most
1 MiB per call, so one large decode cannot leave its plaintext pinned in it.

### Measured and NOT taken — and since superseded

Superseded by #1102: the saving below is now delivered inside the contract
(`BENCH.md` §12), and the refused branch survives only as a priced replica in
`transform_bench_test.go`. The record:

A `if cap(dst) == 0 { return plain, nil }` fast path in all three `Decompress`
functions bought −32 % B/op at 1 MiB and −28 % at 4 KiB. It was **removed**: it
abandons the append-to-dst convention the port documents in three places, and no test
in the repository was sensitive because every call site passes `dst = nil` — so the
`append` branch it claimed to preserve had become dead code. Measuring it directly
also found what `-benchmem` cannot show: `io.ReadAll`'s 512-byte floor means a 64-byte
result is handed back pinning a 512-byte array, **8× retention** for as long as the
caller holds it. The optimisation is real above ~256 B and belongs in its own change,
with the three docs updated and a test that fails when the convention moves —
which #1102 is: `Test_appendAllBounded` fails when a body that fits `dst` is
decoded into another array.

## Conventions

- **Registration without `init()`**: `var GzipCompressor = transform.Register(gzipCompressor{})`,
  `var FlateCompressor = transform.Register(flateCompressor{})` and
  `var ZlibCompressor = transform.Register(zlibCompressor{level: zlib.DefaultCompression})`.
- **Value singletons** — idempotent re-registration is a no-op. Each scheme
  carries a level field but is still a *comparable* value fixed at
  construction, so re-registering the same scheme stays a no-op.
- **Scheme options ride on the scheme's constructor**, never on the `Compressor`
  interface (`core/data/transform.Compressor` doc). `NewGzipCompressor`,
  `NewFlateCompressor` and `NewZlibCompressor` are those constructors; their
  results are deliberately **not** registered, since the registry entry is the
  one default-level singleton.
- **append-to-dst** — `Compress` / `Decompress` seed a `bytes.Buffer` with `dst`
  (compress) or `append` onto `dst` (decompress) so callers can reuse buffers.
- **Errors wrap the stdlib cause** via `errs.Wrap(cause, gzipWrap/flateWrap/zlibWrap)`
  so `errors.Is(err, originalCause)` keeps working and the dotted-quad code
  surfaces. Never `fmt.Errorf` / `errors.New`.

## Known boundary — zlib has no `pkg/v1/data/codec` frame algID

The compressed-frame format in `pkg/v1/data/codec/compressed.go` freezes a 1-byte
`algID` per scheme (`gzip=0x01`, `flate=0x02`) and exports only the `Gzip` /
`Flate` constants. **zlib is registered but not framed**: `MarshalCompressed(f,
"zlib", v)` returns `core/data/transform.UnknownCompressor`, which is the documented
behaviour for an algorithm with no frame id. Allocating `0x03` is an additive
change to a wire format frozen post-v1.0.0 and belongs to a `pkg/v1` decision,
not to this package. Consumers reach the scheme through
`core/data/transform.Lookup("zlib")` until then.

## Do NOT

- Add a vendor compressor here. This package is the stdlib half of the domain
  and stays that way; `internal/service` is required by `pkg`, so a vendor
  import here lands in the graph of every consumer who only wanted a logger.
  zstd and s2 live in `third-party/transform` (ADR 0066), brotli and lz4 were
  measured and refused there.
- Move the bounded-read cap into `core/data/transform` — the port stays body-free.
- Add an `init()` — use the package-level `var = transform.Register(...)` form.
- Treat `flate` as the HTTP `deflate` encoding, or register `zlib` under the
  `deflate` name — see the §`flate` and `zlib` are NOT the same thing table.
- Register the result of `NewGzipCompressor`, `NewFlateCompressor` or
  `NewZlibCompressor` at another level than the default — a distinct scheme
  claiming a taken Algorithm panics at boot (`core/data/transform.Register`).
- Share one writer pool across levels — `Reset` keeps the level, so the pool
  must be keyed by it (`newLevelPools`).

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
files: zlib's envelope (`TestZlibIsNotFlate`, `TestZlibEnvelopeWrapsFlateBody`).
zstd and s2 are held to the same rows by `third-party/transform`'s own
`conformance_external_test.go`: this test cannot import that module (a module
cycle, and the layer firewall), so the vendor module carries the rows that
transfer — all but `DecompressBounded`'s, until #917, and the stream form
equal to the block form, which s2's framing is not.

## Verification

```sh
bazel test --config=race //internal/service/data/transform:transform_test
# Fallback
cd internal/service && GOWORK=off go test -race -cover ./data/transform/...
```

## Fuzz (#897)

`fuzz_external_test.go` — `FuzzDecompressBounded`: for gzip, flate and zlib: `DecompressBounded` never panics nor returns more than its ceiling, agrees with `Decompress` on what it accepts, and `Compress` then `DecompressBounded` gives the input back. The seed corpus runs with the package's tests; `scripts/ci/codec-fuzz.sh` (nightly, `codec-fuzz.yml`) explores it.
