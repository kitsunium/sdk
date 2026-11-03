<!-- generated from internal/service/data/codec/csv/append_bench_test.go — run `go test -run='^$' -bench='Marshal1KiB|Append1KiB' -benchmem -benchtime=500ms -count=6 ./internal/service/data/codec/csv/` to refresh -->
# Benchmarks — `internal/service/data/codec/csv`

| | |
|---|---|
| Machine | AMD EPYC 7351P 16-Core Processor, 8 vCPU, 15 GiB |
| OS | Linux 6.12.101+deb13-amd64 |
| Go toolchain | go1.27.3 linux/amd64 |
| Base commit | `740e7c1e` (+ the change measured below) |
| Measured | 2026-11-03, `-benchtime=500ms -count=6`, interleaved old/new, medians (benchstat) |

> **Numbers vary across machines.** Compare deltas measured on one machine,
> never figures from two.

## Marshal and Append of a value of about 1 KiB (#1124)

`Append` writes onto a caller buffer that already has the room, so its
figures are what Append costs beyond the bytes themselves.

The value is a 40-row table of four cells, one of them quoted (`kibValue`).

| benchmark | ns/op | B/op | allocs/op |
|---|---:|---:|---:|
| `Marshal1KiB` | 8 456 → 8 550 (~) | 5 120 → 1 024 | 2 → **1** |
| `Append1KiB` | 8 455 → 8 280 (~) | 5 120 → **0** | 2 → **0** |

Before, `Marshal` built an `encoding/csv.Writer` (its 4 KiB `bufio.Writer`)
over a pooled buffer and cloned the result, and `Append` was that `Marshal`
plus a copy. The records are now written by the package's own encoder
(`encode.go`), which writes `encoding/csv`'s bytes — pinned against it by
`TestAppendRecordsIsEncodingCSV` and `FuzzAppendRecordsIsEncodingCSV` — straight
into the destination: `Marshal` allocates its result once at its exact size,
`Append` nothing when `dst` has the room. The time is the per-byte quoting
scan either way.

## Unmarshal of blank lines (#1098)

Measured by `sizing_integration_test.go` (race off, `testing.Benchmark`'s byte
counter): 1 MiB of `'\n'` decoded into `[][]string` allocated **25 178 515 B**
on the base — a slice header reserved per newline, which encoding/csv then
skips — and **4 233 B** now. The table is reserved from the input's non-empty
lines, at most 4 096 (`maxReservedRecords`), and grows by `append` past that.

## Typed tables (#902) — 1 000 rows

`benchOrder` is four columns — an `int64`, a string, a `float64`, a `bool`
(`typed_bench_test.go`, `-benchtime=300ms -count=6`, medians):

| benchmark | ns/op | B/op | allocs/op |
|---|---:|---:|---:|
| `UnmarshalTyped1000` | 525 600 | 69 263 | **1 018** |
| `UnmarshalRecordsAndConvert1000` | 428 300 | 157 798 | 2 017 |
| `MarshalTyped1000` | 412 300 | 65 536 | **1** |

`UnmarshalRecordsAndConvert1000` is what a caller wrote before #902: decode
into `[][]string`, then `strconv` each cell into the struct. Reading straight
into `[]benchOrder` halves the bytes and the allocations — no per-record
`[]string` clone, rows decoded in place into the slice's free slots — and is
about a fifth slower: each cell goes through `reflect` (`FieldByIndex`, then a
typed `Set`) where the hand-written loop names the fields. The allocation per
row left is the record's one string (encoding/csv's), which the `item` field
keeps. `MarshalTyped1000` writes into one buffer sized from an estimate:
one allocation for the table.
