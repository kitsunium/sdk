<!-- generated from internal/service/codec/msgpack/msgpack_bench_test.go — run `go test -run='^$' -bench=. -benchmem -count=10 ./internal/service/codec/msgpack/` from the repository root to refresh -->
# Benchmarks — `internal/service/codec/msgpack`

The suite drives the codec only through the `core/codec` contract —
`Marshal`, `Append`, `Unmarshal`, `NewEncoder`, `NewDecoder` — so the same file
measures whichever implementation sits behind `msgpack.New()`. This first
report is the **vendor-backed** codec (`github.com/vmihailenco/msgpack/v5`
v5.4.1), recorded before anything replaced it, so a later implementation has a
baseline taken on the same machine with the same harness.

## Reproducibility envelope

| Dimension | Value |
|---|---|
| CPU | Apple M1 Pro (10 cores) |
| RAM | 16 GiB |
| OS | macOS 26.6.2 (Darwin 25.6.0), arm64 |
| Go toolchain | go1.27.1 darwin/arm64 |
| Git commit | 390aa80f (vendor-backed codec) |
| Generated | 2026-10-03 |
| Wall clock | `-benchtime=1s -count=10`, summarised by `benchstat` (median, 95 % CI) |

The machine was shared with other builds while this ran: the confidence
intervals below are wide on a few rows (StreamEncode ±142 %), and the medians
are what the rows report.

## Payloads

`benchRecord` mirrors `pkg/v1/codec`'s `complexRT` fixture — one field of every
family the codec writes (bools, every integer width, floats, strings, bytes,
slices, string-keyed maps, nested structs by value, pointer and slice, a
timestamp). `small` is the record itself, `medium` and `large` multiply its
collections by 100 and 1 000.

| payload | encoded |
|---|---:|
| small | 611 B |
| medium | 11 753 B |
| large | 122 085 B |

`StreamEncode` / `StreamDecode` write and read back 64 `small` records per
iteration through one Encoder / Decoder.

## Vendor-backed codec (baseline)

| benchmark | time/op | throughput | B/op | allocs/op |
|---|---:|---:|---:|---:|
| Marshal/small | 2.43 µs | 240 MiB/s | 1 169 | 13 |
| Marshal/medium | 66.6 µs | 168 MiB/s | 25 648 | 513 |
| Marshal/large | 652 µs | 179 MiB/s | 252 394 | 5 013 |
| Append/small | 2.38 µs | 244 MiB/s | 528 | 12 |
| Append/medium | 61.5 µs | 182 MiB/s | 13 343 | 512 |
| Append/large | 595 µs | 196 MiB/s | 128 640 | 5 012 |
| Unmarshal/small | 5.23 µs | 112 MiB/s | 3 387 | 62 |
| Unmarshal/medium | 119 µs | 94 MiB/s | 82 691 | 1 556 |
| Unmarshal/large | 1.23 ms | 95 MiB/s | 888 453 | 15 060 |
| UnmarshalAny/small | 6.36 µs | 92 MiB/s | 5 780 | 136 |
| UnmarshalAny/medium | 127 µs | 89 MiB/s | 149 001 | 3 527 |
| UnmarshalAny/large | 1.15 ms | 101 MiB/s | 1 526 299 | 35 619 |
| StreamEncode (64 × small) | 276 µs | 136 MiB/s | 33 945 | 772 |
| StreamDecode (64 × small) | 349 µs | 107 MiB/s | 221 385 | 3 976 |

Two things stand out. `Marshal` and `Append` allocate once per map ENTRY of
`StringStruct` (513 at medium, 5 013 at large): the library walks a
`map[string]struct` through `reflect.MapIter.Key()/Value()`, which copies each
non-pointer element to the heap. And `Append` is not cheaper than `Marshal` by
the copy alone — it still encodes into the library's pooled encoder first and
appends afterwards.
