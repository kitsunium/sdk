<!-- generated from internal/service/data/codec/bson/bson_bench_test.go — run `go test -run='^$' -bench=. -benchmem ./internal/service/data/codec/bson/` to refresh -->
# Benchmarks — `internal/service/data/codec/bson`

The codec was rewritten from a wrapper around `go.mongodb.org/mongo-driver/bson`
v1.17.9 into a native implementation of BSON 1.1. These are the two measured
against each other: the package's own benchmarks, compiled once at the last
library-backed commit and once at the native one, and the facade benchmarks of
`pkg/v1/data/codec` restricted to `/bson/`, the same way.

## Reproducibility envelope

| Dimension | Value |
|---|---|
| CPU | Apple M1 Pro, 10 cores |
| RAM | 16 GiB |
| OS | macOS 26.6.2 |
| Go toolchain | go1.27.1 darwin/arm64 |
| Base commit | `390aa80f` (the library-backed codec), against the native codec on top of it |
| Generated (UTC) | 2026-10-03 |
| Method | the old and new test binaries run **alternately**, 8 rounds each (`-benchtime 400ms` for the package, `250ms` for the facade); `benchstat` medians, 95 % intervals |

> **The machine was not quiet.** Other builds ran throughout, with a load
> average between 30 and 90 on ten cores, so every timing carries a wide
> interval (±30 % to ±200 %). Alternating the two binaries gives both the same
> conditions; it does not make either fast. **Allocation counts and bytes are
> exact** — they do not depend on load — and they are where the difference is
> unambiguous. Re-run on a quiet machine before quoting a time.

## 1. The package's own benchmarks

`record` is one ten-field document (a string, a date, an array, a sub-document,
bytes), `batch1000` a thousand of them in one document (~258 KB), `map16` a
sixteen-key `map[string]any`. Decodes go into the struct the document was
written from (`Typed`) or into `map[string]any` (`Map`).

| benchmark | ns/op, driver | ns/op, native | Δ | B/op, driver → native | allocs/op, driver → native |
|---|---:|---:|---:|---:|---:|
| Marshal/record | 1 622 | 1 292 | ~ (p=0.13) | 256 → 256 | 1 → 1 |
| Marshal/batch1000 | 1 979 µs | 2 268 µs | ~ (p=0.96) | 327 Ki → **852 Ki** | 3 001 → 13 |
| Marshal/map16 | 4 721 | 3 624 | ~ (p=0.16) | 1 330 → 320 | 39 → 1 |
| Append/record | 1 795 | 766 | **−57 %** | 256 → **0** | 1 → **0** |
| Append/batch1000 | 2 121 µs | 627 µs | **−70 %** | 332 Ki → **0** | 3 002 → **0** |
| Append/map16 | 6 743 | 3 291 | **−51 %** | 1 330 → **0** | 39 → **0** |
| UnmarshalTyped/record | 3 605 | 2 696 | ~ (p=0.20) | 1 072 → 528 | 41 → 12 |
| UnmarshalTyped/batch1000 | 6 301 µs | 1 917 µs | **−70 %** | 928 Ki → 325 Ki | 38 760 → 10 000 |
| UnmarshalMap/record | 16 022 | 4 295 | **−73 %** | 2 681 → 1 408 | 91 → 41 |
| UnmarshalMap/batch1000 | 13 384 µs | 3 154 µs | **−76 %** | 2.48 Mi → 1.35 Mi | 90 770 → 40 750 |
| UnmarshalMap/map16 | 26 629 | 5 073 | **−81 %** | 4 162 → 2 024 | 116 → 49 |
| MarshalParallel | 2 903 | 761 | **−74 %** | 448 → 448 | 2 → 2 |
| UnmarshalParallel | 2 894 | 831 | **−71 %** | 688 → 144 | 39 → 10 |

Geometric mean of the times: **−59 %**. A second, interleaved run of the two
`Marshal` rows alone (10 rounds each, load ~35) gave medians of 1 216 → 504 ns
for `record` and 1 253 → 635 µs for `batch1000` — the direction the
allocations predict, at p=0.052 under that noise.

### The one regression: bytes for a document past 256 KiB

`Marshal/batch1000` allocates **2.6× the bytes** the driver did, in 13
allocations instead of 3 001. The document is ~258 KB. `Marshal` encodes into
the codec family's shared scratch buffer (`core/data/codec/scratch`), and that pool
refuses to keep a buffer past `MaxRetainedBufBytes` (256 KiB) — a policy every
codec shares, so that one oversized payload cannot pin a large allocation. A
document past that size therefore grows its buffer again on every call, and
the caller gets an exact-size copy of it. The driver kept its own pool of
buffers up to 16 MiB, which is the difference. Two things bound it: the encoder
doubles its buffer at element boundaries (Go's append grows past 256 bytes by a
quarter, which put the same benchmark at 1.4 MiB/op before the change), and a
caller encoding large documents repeatedly can use `Append` into a buffer it
keeps — **0 B/op** above.

## 2. Through `pkg/v1/data/codec`

The facade benchmarks encode the shared `complexRT` fixture — every scalar
family, nested structs, `map[string]int` and `map[string]innerRT` — at three
sizes (`small` ≈ 8 KB, `medium` ≈ 100×, `large` ≈ 1 000× its collections).

| benchmark | B/op, driver → native | allocs/op, driver → native | ns/op Δ |
|---|---:|---:|---:|
| Marshal small / medium / large | 10.0 Ki / 47.8 Ki / 419 Ki → 9.3 Ki / 27.2 Ki / 244 Ki | 34 / 712 / 7 013 → 2 / 2 / 5 | ~ / ~ / ~ |
| Unmarshal small / medium / large | 16.2 Ki / 165 Ki / 1.68 Mi → 12.3 Ki / 74.6 Ki / 745 Ki | 243 / 4 360 / 43 680 → 73 / 1 333 / 13 040 | ~ / **−40 %** / ~ |
| Append small / medium / large | 10.0 Ki / 47.8 Ki / 419 Ki → 24 B / 27 B / 2.2 Ki | 34 / 712 / 7 013 → 1 / 1 / 1 | **−64 %** / ~ / ~ |
| AppendParallel small / medium / large | 10.0 Ki / 48.2 Ki / 431 Ki → 2 B / 45 B / 8.1 Ki | 33 / 711 / 7 027 → 0 / 0 / 0 | **−80 %** / **−85 %** / **−65 %** |
| MarshalParallel large | 433 Ki → 232 Ki | 7 030 → 2 | **−57 %** |

Geometric mean of the 18 facade timings: **−42 %**, no row significantly
slower. `pkg/v1/data/codec/BENCH.md` is regenerated on the project's reference
machine by `make bench`; it still shows the driver until that run.

## 3. Where the allocations went

- **A struct encodes in one allocation, and appends in none.** Per-type plans
  (`plan.go`) are built once and cached by `reflect.Type`; field names are
  pre-encoded; values are read through `reflect.Value`'s typed getters, and the
  value types (`time.Time`, `ObjectID`, `url.URL`) through `reflect.TypeAssert`,
  which does not box. The driver allocated once per element of `batch1000`.
- **A map costs nothing beyond its output.** The reflective map walk reuses two
  settable slots, pooled per map type, and writes the entries in iteration
  order before sorting them in place (`sortSegments`). The pooling took
  `Marshal/map16` from 3 allocations to 1 and the facade's `large` from 2 013
  to 5.
- **Decoding allocates what the result holds**: strings, slices, maps, and the
  `any` boxes of `map[string]any` — about a third of the driver's count. The
  validator allocates nothing.

`TestAllocBudget` (race-off alloc lane) pins the ceilings: a struct marshals in
1 allocation and appends in 0, a small map marshals in 1 and appends in 0.
