<!-- generated from internal/service/data/codec/ndjson/codec_bench_test.go — run `cd internal/service && GOWORK=off go test -run='^$' -bench=. -benchmem -benchtime=500ms -count=8 ./data/codec/ndjson/` to refresh -->
# Benchmarks — `internal/service/data/codec/ndjson`

NDJSON is a batch format: one record per line, and batches of thousands of
small records are what it is for. `pkg/v1/data/codec/BENCH.md` compares it with
the other formats on one payload; these two benchmarks answer the question that
comparison cannot — **what one more record costs**.

| | |
|---|---|
| Machine | AMD EPYC 7351P 16-Core Processor, 8 vCPU, 15 GiB |
| OS | Linux 6.12.101+deb13-amd64 |
| Go toolchain | go1.27.2 linux/amd64 |
| Base commit | `1f2be923` (+ the change measured below) |
| Measured | 2026-10-20, `-benchtime=500ms -count=8`, medians |

> **Numbers vary across machines.** Compare deltas measured on one machine,
> never figures from two.

## Marshal and Append over a batch of small records

`benchEvent` is five flat fields (`id`, `kind`, `actor`, `ok`, `millis`).
`Append` writes into a caller buffer that already has the capacity, so its
figures are the encoder's and not the allocator's.

| benchmark | ns/op | B/op | allocs/op |
|---|---:|---:|---:|
| `MarshalBatch/10` | 8 884 | 664 | **2** |
| `MarshalBatch/1000` | 882 300 | 74 220 | **2** |
| `AppendBatch/10` | 8 513 | 72 | **2** |
| `AppendBatch/1000` | 849 700 | 74 | **2** |

**Zero allocations per record**: the count is the same at ten records and at a
thousand. The two left are per call — the `json.Encoder` and its state —
plus, for `Marshal`, the detached copy of the pooled buffer (the 74 KiB at a
thousand records is that copy: the caller's output).

### Before: one `json.Marshal` per record

Until this report the loop called `json.Marshal` once per record and copied the
returned slice into the output, so every record allocated its own encoding only
to throw it away. Measured interleaved against that loop on the same machine:

| benchmark | ns/op | B/op | allocs/op |
|---|---:|---:|---:|
| `MarshalBatch/10` | 9 388 → 8 884 (−5.4 %) | 1 305 → 664 | 12 → 2 |
| `MarshalBatch/1000` | 941 200 → 882 300 (−6.3 %) | 154 050 → 74 220 | 1 002 → 2 |
| `AppendBatch/10` | 8 796 → 8 513 (−3.2 %) | 664 → 72 | 11 → 2 |
| `AppendBatch/1000` | 896 700 → 849 700 (−5.2 %) | 79 830 → 74 | 1 001 → 2 |

The time is mostly `encoding/json` walking each record; what one encoder per
batch removes is the garbage — a thousand-record `Append` used to leave 78 KiB
for the collector and now leaves 74 bytes. `TestAllocPerRecord` (race-off alloc
lane) pins the slope at 0 per record for every shape but `[]any`, whose dynamic
value json/v2 must still copy (1).
