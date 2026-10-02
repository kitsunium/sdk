<!-- generated from internal/service/codec/toml/toml_bench_test.go — run `cd internal/service && GOWORK=off go test -run='^$' -bench=. -benchmem -count=8 ./codec/toml/` to refresh -->
# Benchmarks — `internal/service/codec/toml`

The codec wrapped `github.com/pelletier/go-toml/v2` v2.4.3 and is now written
with the standard library alone. These numbers answer one question: what did
dropping the dependency cost, and what did it buy?

## How it was measured

`toml_bench_test.go` uses the codec's public surface only, so the same file
compiled against both implementations: the go-toml wrapper at `390aa80f` and
the native codec that replaced it. The two binaries ran **in alternation**,
four rounds of two samples each at `-benchtime=1s`, on a laptop shared with
other work — so the time column is the **best of the eight samples**, the
estimator least disturbed by a neighbour's load (the medians carried ±30–80 %
of noise and are not worth printing). Bytes and allocations do not depend on
load: they are exact.

| Dimension | Value |
|---|---|
| CPU | Apple M1 Pro, 10 cores |
| RAM | 16 GiB |
| OS | macOS 26.6.2 (25G83) |
| Go toolchain | go1.27.1 darwin/arm64 |
| Baseline | `390aa80f` (go-toml/v2 v2.4.3) |
| Measured | 2026-10-02T23:50Z |

## Results

| Benchmark | go-toml ns/op | native ns/op | time | go-toml B/op | native B/op | go-toml allocs | native allocs |
|---|---:|---:|---:|---:|---:|---:|---:|
| `UnmarshalConfigMap` — 2 KiB config → `map[string]any` | 5 328 | 5 646 | 1.06× | 4 955 | 4 914 | 82 | **80** |
| `UnmarshalConfigStruct` — same → typed struct | 7 193 | **5 310** | **0.74×** | 1 729 | 1 624 | 38 | **34** |
| `MarshalConfigStruct` | 5 375 | 5 323 | 0.99× | 1 364 | 1 356 | 6 | **5** |
| `MarshalConfigMap` | 10 916 | 10 950 | 1.00× | 1 633 | 1 985 | 45 | **31** |
| `AppendConfigStruct` — sized destination | 5 291 | 5 249 | 0.99× | 467 | 459 | 5 | **4** |
| `UnmarshalSmallMap` — the alloc-gate payload | 575 | 580 | 1.01× | 288 | 288 | 6 | **5** |
| `MarshalSmallMap` | 539 | 549 | 1.02× | 96 | 88 | 5 | **4** |
| `UnmarshalLargeMap` — 1 000 `[[items]]` → map | 634 580 | 733 586 | 1.16× | 521 780 | 1 061 590 | 10 758 | **9 761** |
| `UnmarshalLargeStruct` — same → `[]struct` | 896 097 | **654 967** | **0.73×** | 297 996 | 711 789 | 5 012 | **3 007** |
| `MarshalLargeStruct` | 647 888 | 596 142 | 0.92× | 91 232 | 91 528 | 3 | **2** |
| `UnmarshalWideTable` — 10 000 keys in one table | 109 381 333 | **2 638 567** | **0.024×** | 1 672 704 | 3 358 966 | 10 836 | 19 960 |

## What the numbers say

1. **Encoding did not move.** Every encode benchmark is within 8 % of
   go-toml's best — the output is byte-identical too — with one allocation
   fewer per call: the document is encoded once into the pooled
   `core/codec/scratch` buffer and copied out at its exact length.

2. **Decoding into a struct is a quarter faster** (0.73–0.74×) with fewer
   allocations: the parse is a single pass over the bytes, a struct's fields
   are found through a plan cached per type, and a predeclared scalar field
   (`string`, `int`, `float64`, …) skips the type lookup altogether, since such
   a type can have no `UnmarshalText`.

3. **Decoding into `map[string]any` is slightly slower: 6 % on a configuration,
   16 % on a thousand tables.** This is the cost of the design, not an
   accident: the native decoder checks the whole document — every redefinition
   rule — into a tree before it writes the map, where go-toml wrote the map
   while it parsed. A 2 KiB configuration decodes in 5.6 µs either way. Keys
   are interned in the pooled parser, as go-toml interned them, so a reloaded
   configuration allocates no key twice.

4. **Large documents allocate twice the bytes.** A thousand tables are some
   9 000 nodes of 64 bytes, a 560 KiB arena — past the 256 KiB ceiling above which a pooled
   object is dropped rather than kept (the SDK-wide threshold `core/codec/scratch`
   applies), so the arena is allocated per call. It is allocated **once**: the
   parser sizes it from the separators of the document instead of doubling it
   (which cost 2.4 MiB per call before that change). Under the ceiling — any
   configuration — the arena is reused and costs nothing.

5. **A wide table is 41× faster.** go-toml's seen-tracker searched a table's
   keys linearly for every new key, so a table of *n* keys cost *n²/2*
   comparisons: 109 ms for 10 000 keys. The native parser moves a table past
   16 keys into a hash index, 2.6 ms — and the gap widens with *n*, which is
   the difference between a slow document and a denial of service. It
   allocates more here because the key intern table stops at 4 096 entries.

The allocation ceilings `TestAllocBudget` pins (`codec_integration_test.go`)
were re-pinned from go-toml's 17 / 15 / 16 to 7 / 7 / 6 for the 5 / 5 / 4 the
native codec measures.
