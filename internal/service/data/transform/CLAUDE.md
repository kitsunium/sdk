<!-- updated: 2026-11-03T22:00:00Z -->
# internal/service/data/transform/

The reference half of this file moved, unchanged and under the same section titles, to
`internal/service/data/transform/NOTES.md` (ADR 0253): read the section a change touches.
It holds:
§`flate` and `zlib` are NOT the same thing; §Contents; §Streams (ADR 0213); §A level knob never yields an inert compressor (ADR 0031); §The conformance table.

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
flate **and zlib** (alloc lane: `TestDecompressIntoASizedDstAllocatesNothing`):
zlib's block decode reads the RFC 1950 envelope itself (`zlibBody`,
`zlibCheckTrailer`, `adler32.Checksum` over the plaintext) over the pooled flate
decoder, because `compress/zlib`'s `Reset` builds an Adler-32 digest per stream
(#1102, `BENCH.md` §12.1); a nil `dst` costs one allocation of the payload's size
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

## Verification

```sh
bazel test --config=race //internal/service/data/transform:transform_test
# Fallback
cd internal/service && GOWORK=off go test -race -cover ./data/transform/...
```

## Fuzz (#897)

`fuzz_external_test.go` — `FuzzDecompressBounded`: for gzip, flate and zlib: `DecompressBounded` never panics nor returns more than its ceiling, agrees with `Decompress` on what it accepts, and `Compress` then `DecompressBounded` gives the input back; zlib's engine is also held, on every input, to `compress/zlib` (`fuzzZlibAgrees`: same bytes on what both accept, the standard library's error on what both refuse), the guard on the envelope read in this package (#1102). `fuzzPastCeiling` holds gzip and flate, and `fuzzZlibAgrees` zlib, to the ceiling-first order: what the standard library decodes past the ceiling is `DecompressedTooLarge` and no other code (#1432). The seed corpus runs with the package's tests; `scripts/ci/codec-fuzz.sh` (nightly, `codec-fuzz.yml`) explores it.
