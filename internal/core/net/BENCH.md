<!-- generated from internal/core/net/websocket_bench_test.go — run `cd internal/core && GOWORK=off go test -run='^$' -bench=. -benchmem -benchtime=1s ./net/` to refresh -->
# Benchmarks — `internal/core/net`

The WebSocket wire format (ADR 0047), which is where this package's per-message
work lives. Everything below is **zero allocations** except one row, and the
single most useful fact is a comparison nobody would guess.

## Unmasking, not UTF-8 validation, is what caps an inbound text connection

Both run once over the whole payload of every inbound message. They are not
close:

| | throughput |
|---|---:|
| `ApplyWSMask`, 1 MiB | **1 320 MB/s** |
| `ValidateWSText`, 1 MiB ASCII | **32 839 MB/s** |
| ratio | **25×** |

RFC 6455 §5.1 requires every client-to-server frame to be masked, so a server
XORs every inbound byte with a four-byte key — there is no fast path and no way
to skip it. UTF-8 validation, meanwhile, gets the standard library's
eight-bytes-at-a-time ASCII check.

So on an ASCII text connection the mask is **96 %** of the per-byte work, and
anyone reaching for a faster validator is optimising the 4 %.

## Non-ASCII text costs 28× to validate — and then the mask wins again

| 4 KiB payload | ns/op | throughput |
|---|---:|---:|
| `ValidateWSText`, ASCII | 143.0 | 28 639 MB/s |
| `ValidateWSText`, three-byte runes | 3 858 | **1 061 MB/s** |
| `ApplyWSMask` | 3 026 | 1 354 MB/s |

The ASCII fast path is worth 27×, and losing it puts validation and masking in
the same order of magnitude. A conversation in Japanese, Arabic or Russian
therefore costs roughly **twice** what the same byte count costs in English —
which is a capacity-planning fact rather than a defect, and it is stated so
nobody discovers it from a graph.

ADR 0047 judges UTF-8 on the **reassembled** message, because a rune may
straddle a fragment and a per-frame check would reject valid input. These rows
are the price of that correctness, paid once per message rather than once per
frame.

## Per-frame costs are fixed and tiny

| | ns/op | allocs |
|---|---:|---:|
| `WSFrameHeaderLen` | 2.921 | **0** |
| `ParseWSFrameHeader` | 29.70 | **0** |
| `AppendWSFrame`, 64 B | 16.30 | **0** |
| `AppendWSFrame`, 4 KiB | 118.8 | **0** |
| `AppendWSClosePayload` | 28.83 | **0** |
| `ParseWSClosePayload` | 44.23 | 4 B, 1 |

`ParseWSFrameHeader` at 29.7 ns enforces every rule that does not depend on
direction — reserved bits, reserved opcodes, control-frame shape, minimal length
encoding — so ADR 0047's "every MUST-fail is enforced rather than tolerated"
costs 30 nanoseconds per frame.

The single allocation in the whole file is `ParseWSClosePayload` returning the
close *reason* as a string. It happens once per connection.

`AppendWSFrame` writing into a reused buffer is allocation-free, which is what
makes a per-message send path viable; the throughput numbers on those rows are a
`copy`, not real work.

## Two API contracts this file tripped over, both of them the design working

Writing these benchmarks hit two refusals, and neither was a bug:

- **`ParseWSFrameHeader` refuses an unmasked frame.** `AppendWSFrame` writes the
  SERVER's direction, which is unmasked, so feeding its output to the parser is
  refused with `WS_PROTOCOL_VIOLATION`. The benchmark now assembles a masked
  CLIENT frame by hand — which is what the connection actually parses.
- **`ParseWSFrameHeader` refuses a slice longer than the header.** It takes
  exactly `WSFrameHeaderLen` bytes and will not over-read into a payload whose
  announced length it has not validated yet. The benchmark follows the same
  two-step contract a connection does.

Both are the kind of thing a benchmark finds by being a fresh caller.

## Reproducibility envelope

> **Numbers vary across machines**, and the two per-byte rows more than most:
> they are memory bandwidth and the compiler's vectorisation, both of which
> differ sharply across CPUs. The allocation column is exact. What this report
> asserts is the **ratios** — mask-vs-validate and ASCII-vs-multibyte.

| Dimension | Value |
|---|---|
| CPU cores          | 8 |
| RAM                | 15 GiB |
| OS / kernel        | Linux 6.12.101+deb13-amd64 |
| Architecture       | amd64 |
| Go toolchain       | go1.27.1 linux/amd64 |
| Git branch         | jaimerias-que-tu-te-connect |
| Git commit         | 3224012 |
| Generated (UTC)    | 2026-09-10 |
| Bench wall-clock   | `-test.benchtime=1s`, single run |

## Results

```
goos: linux
goarch: amd64
pkg: github.com/kitsunium/sdk/internal/core/net
cpu: AMD EPYC 7351P 16-Core Processor
BenchmarkApplyWSMask_64B-8            	21806594	        55.59 ns/op	1151.38 MB/s	       0 B/op	       0 allocs/op
BenchmarkApplyWSMask_4KiB-8           	  400326	      3026 ns/op	1353.63 MB/s	       0 B/op	       0 allocs/op
BenchmarkApplyWSMask_1MiB-8           	    1552	    794396 ns/op	1319.97 MB/s	       0 B/op	       0 allocs/op
BenchmarkValidateWSText_64B-8         	47843720	        23.04 ns/op	2777.48 MB/s	       0 B/op	       0 allocs/op
BenchmarkValidateWSText_4KiB-8        	 8349897	       143.0 ns/op	28638.75 MB/s	       0 B/op	       0 allocs/op
BenchmarkValidateWSText_1MiB-8        	   35934	     31931 ns/op	32838.68 MB/s	       0 B/op	       0 allocs/op
BenchmarkValidateWSText_Multibyte-8   	  311748	      3858 ns/op	1061.33 MB/s	       0 B/op	       0 allocs/op
BenchmarkWSFrameHeaderLen-8           	411115767	         2.921 ns/op	       0 B/op	       0 allocs/op
BenchmarkParseWSFrameHeader-8         	40799472	        29.70 ns/op	       0 B/op	       0 allocs/op
BenchmarkAppendWSFrame_64B-8          	68881999	        16.30 ns/op	3925.75 MB/s	       0 B/op	       0 allocs/op
BenchmarkAppendWSFrame_4KiB-8         	10242938	       118.8 ns/op	34482.37 MB/s	       0 B/op	       0 allocs/op
BenchmarkAppendWSClosePayload-8       	41683942	        28.83 ns/op	       0 B/op	       0 allocs/op
BenchmarkParseWSClosePayload-8        	27285171	        44.23 ns/op	       4 B/op	       1 allocs/op
```
