<!-- run `cd internal && GOWORK=off go test -run='^$' -bench=. -benchmem -count=6 -benchtime=300ms ./service/net/http2/` to refresh; not `make bench`, which regenerates the codec's report only -->
# Benchmarks — `internal/service/net/http2`

The frame layer and HPACK codec of the loop's HTTP/2 profile (ADR 0220). The
question is what a request costs against the sibling `http1` codec on the same
machine. A figure is the median of six runs; only the ratios between rows
measured on one machine at one moment are worth reading.

## Reproducibility envelope

| Dimension | Value |
|---|---|
| CPU                | AMD EPYC 7351P, 8 vCPU (a busy VM, GOMAXPROCS=4) |
| OS / kernel        | Linux 6.12.101+deb13-amd64 |
| Go toolchain       | go1.27.3 linux/amd64 |
| Git branch         | `wip/sdk-lot-2-h2` |
| Git commit         | base `43e7682c6` (uncommitted when measured) |
| Generated (UTC)    | 2026-11-09 |
| Bench wall-clock   | `-benchtime=300ms -count=6` |

## Results

| Benchmark | ns/op | B/op | allocs/op |
|---|---|---|---|
| `ParseFrameHeader` | **3.7** | 0 | 0 |
| `Decode` (a GET of 8 fields: 3 indexed, 5 literals) | **620** | 0 | 0 |
| `Decode_bomb` (16 KiB block, 4 000 references to a 4 KiB entry, refused) | **500** | 0 | 0 |
| `AppendHeaders` (a 200 with 3 fields and a content length) | **430** | 0 | 0 |
| `FeedHeaders` (16 HEADERS frames in a batch, answered and the connection restarted) | **2 950** (**185** a stream) | 0 | 0 |
| `WindowUpdateCoalescing` (16 streams consumed, one batch of credits) | **700**, 17 frames a batch (one per stream, one for the connection) | 0 | 0 |

Beside it on the same machine, `http1`'s `Parse/get` (266 B, 5 fields of a
GET): **490 ns**, 0 allocations. A field block costs about 1.3 of them for
twice the fields, with the table's maintenance and the validation of every
name and value included: HPACK's bytes on a keep-alive connection, not its
algorithm, are where a request wins or loses, and the literal `Decode` row
is that first, unindexed request.

`FeedHeaders` is the frame reader and the state machine without the HPACK
decode (the adapter decodes each block): about 180 ns a stream with its
HEADERS frame, its stream state and its answer, 16 streams in flight, nothing
allocated. `WindowUpdateCoalescing` writes the batch's credits in one pass of
the streams, one frame each and one for the connection, whatever the frames of
DATA they came from.

`Decode_bomb` is under the microsecond of #959's acceptance: the 16 KiB block
costs its first two 4 KiB references, each copied, and the third is refused
before any copy.
