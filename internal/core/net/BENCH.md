<!-- generated from internal/core/net/websocket_bench_test.go — run `cd internal/core && GOWORK=off go test -run='^$' -bench=. -benchmem -benchtime=1s -count=3 ./net/` to refresh; every published number is the MEDIAN of the three -->
# Benchmarks — `internal/core/net`

The WebSocket wire format (ADR 0047), which is where this package's per-message
work lives. Everything below is **zero allocations** except one row, and the
single most useful fact is a comparison nobody would guess.

## Unmasking WAS what capped an inbound connection. It is not any more.

This section used to read *"Unmasking, not UTF-8 validation, is what caps an
inbound text connection"*, and it was right: `ApplyWSMask` ran one byte at a
time at 1 320 MB/s against the standard library's eight-bytes-at-a-time ASCII
check at 32 839 MB/s, so the mask was **96 %** of the per-byte work and anyone
reaching for a faster validator was optimising the 4 %.

That conclusion held right up to the moment somebody read it. `ApplyWSMask` now
moves a machine word at a time — see §The word transform — and the ranking has
changed hands:

| 4 KiB payload, per-byte pass | ns/op | throughput | share of the per-byte work |
|---|---:|---:|---:|
| `ApplyWSMask`, byte at a time (before) | 3 006 | 1 363 MB/s | 96 % |
| `ApplyWSMask`, word at a time (now) | **186.8** | **21 923 MB/s** | **57 %** |
| `ValidateWSText`, ASCII | 140.7 | 29 117 MB/s | 43 % |

The two passes are now within 1.3× of each other, and neither dominates. RFC
6455 §5.1 still requires every client-to-server frame to be masked, so the XOR
is still not optional and not skippable — it is simply no longer the reason an
inbound connection is slow.

## Non-ASCII text costs 28× to validate — and now validation is the whole cost

| 4 KiB payload | ns/op | throughput |
|---|---:|---:|
| `ValidateWSText`, ASCII | 140.7 | 29 117 MB/s |
| `ValidateWSText`, three-byte runes | 3 891 | **1 052 MB/s** |
| `ApplyWSMask` | 186.8 | 21 923 MB/s |

The ASCII fast path is worth 28×, and losing it no longer merely *matches* the
mask — it dwarfs it. Counting only this package's two per-byte passes, a 4 KiB
message of three-byte runes costs 186.8 + 3 891 = 4 078 ns against 186.8 + 140.7
= 327 ns for the same byte count in ASCII: **12.5×**. Before the widening the
same comparison was 6 897 against 3 147 ns, or 2.2× — the mask used to hide most
of the difference by being expensive on both sides of it.

This is a capacity-planning fact rather than a defect, and it is stated so
nobody discovers it from a graph. It is also where the next per-byte win is, if
one is ever wanted — `ValidateWSText` on multibyte input is now the slowest
thing this package does per byte, by an order of magnitude.

ADR 0047 judges UTF-8 on the **reassembled** message, because a rune may
straddle a fragment and a per-frame check would reject valid input. These rows
are the price of that correctness, paid once per message rather than once per
frame.

## Per-frame costs are fixed and tiny

| | ns/op | allocs |
|---|---:|---:|
| `WSFrameHeaderLen` | 2.955 | **0** |
| `ParseWSFrameHeader` | 29.46 | **0** |
| `AppendWSFrame`, 64 B | 14.62 | **0** |
| `AppendWSFrame`, 4 KiB | 117.4 | **0** |
| `AppendWSClosePayload` | 20.83 | **0** |
| `ParseWSClosePayload` | 44.60 | 4 B, 1 |

`ParseWSFrameHeader` at 29.5 ns enforces every rule that does not depend on
direction — reserved bits, reserved opcodes, control-frame shape, minimal length
encoding — so ADR 0047's "every MUST-fail is enforced rather than tolerated"
costs 30 nanoseconds per frame.

These rows have not moved, and that is now load-bearing rather than incidental:
with the mask 16× cheaper, the fixed per-frame cost is what a heavily fragmented
message pays. See §What it was worth in situ.

The single allocation in the whole file is `ParseWSClosePayload` returning the
close *reason* as a string. It happens once per connection.

`AppendWSFrame` writing into a reused buffer is allocation-free, which is what
makes a per-message send path viable; the throughput numbers on those rows are a
`copy`, not real work.

## The word transform, and the eight bytes it is not worth below

`ApplyWSMask` moves 32 bytes at a time, then 8, then 1. `BenchmarkApplyWSMask`
runs the shipped form and the byte-at-a-time form it replaced **in the same
binary, on the same lengths and the same buffers**, because a before number from
one run and an after number from another invites a machine-state difference to
be read as a speed-up. `byte_at_a_time` is `applyWSMaskReference`, which is the
previous production body verbatim and is also the correctness oracle in
`websocket_frame_external_test.go` — so the thing being timed is exactly the
thing being proved equivalent.

| bytes | byte at a time | word at a time | speed-up | why this length |
|---:|---:|---:|---:|---|
| 0 | 2.791 ns | 3.517 ns | **0.79×** | an empty Ping |
| 1 | 3.318 ns | 4.196 ns | **0.79×** | |
| 3 | 4.206 ns | 6.292 ns | **0.67×** | |
| 4 | 4.596 ns | 7.343 ns | **0.63×** | one key width — the worst ratio |
| 7 | 6.563 ns | 10.53 ns | **0.62×** | the largest payload with no whole word |
| 8 | 7.081 ns | 4.193 ns | 1.69× | one word, no tail — the crossover |
| 15 | 12.23 ns | 11.22 ns | 1.09× | one word + a seven-byte tail |
| 64 | 56.84 ns | 5.911 ns | 9.62× | two blocks, no tail |
| 125 | 103.3 ns | 14.74 ns | 7.01× | §5.5's control-frame ceiling |
| 4 096 | 3 006 ns | 186.8 ns | **16.1×** | net/http's hijacked `bufio.Reader` |
| 4 097 | 3 007 ns | 187.7 ns | **16.0×** | one byte past it |
| 65 536 | 48 186 ns | 2 948 ns | 16.3× | L2-resident |
| 1 048 576 † | 771 197 ns | 49 207 ns | 15.7× | L3-resident — see below |

† The 1 MiB row is the ONE pair in this table not taken from the `-count=3`
sweep in §Results, which read 773 112 ns / 63 019 ns there. It is the median of
five runs taken in isolation instead, for the reason in the third bullet below.

**The crossover is exactly 8 bytes**, and below it the wide form is genuinely
slower — by at most 3.97 ns, at seven bytes. That is the key word being built
and then thrown away, and it is stated rather than smoothed over.

Three rows are not monotone in throughput and none of them is noise:

- **15 bytes reads slower per byte than 8** (1.34 vs 1.91 GB/s). Fifteen is one
  word plus a seven-byte tail, and those seven bytes cost 7.03 ns — the
  byte-at-a-time rate, because that is literally the loop handling them.
- **125 bytes reads slower per byte than 64** (8.48 vs 10.83 GB/s). Sixty-four
  is two 32-byte blocks and nothing else; 125 is three blocks, three words and a
  five-byte tail.
- **1 MiB reads slower per byte than 64 KiB** (21.3 vs 22.2 GB/s), and it is the
  only row in this file that moves between runs: inside the full sweep it
  measured 63.0 µs / 16.6 GB/s, and re-run in isolation at `-count=5` it is
  49.2 µs / 21.3 GB/s with four of the five runs inside 48–50 µs. One MiB
  exceeds this CPU's 512 KiB L2, so the row is memory-hierarchy-bound and any
  other tenant of the shared L3 shows up in it. The byte-at-a-time row at the
  same size does **not** move (771–775 µs, 0.6 % spread) because at 1.36 GB/s it
  is compute-bound and never asks the cache for anything it cannot have. Making
  the transform 16× faster moved its bottleneck off the CPU, and this row is the
  signature of that.

## No short-payload branch — measured, then refused

The 0.62× at seven bytes invites an obvious repair: below one word, skip the
key-word set-up and go straight to the byte loop. `benchMaskShortGuarded` is
that repair, kept in the benchmark file so the decision not to ship it stays
measurable instead of becoming folklore.

| bytes | word at a time | with the short branch | verdict |
|---:|---:|---:|---|
| 0 | 3.517 ns | 3.083 ns | recovers 0.43 ns |
| 4 | 7.343 ns | 5.147 ns | recovers 2.20 ns |
| 7 | 10.53 ns | 7.035 ns | recovers 3.50 ns |
| 8 | 4.193 ns | 7.013 ns | **loses 2.82 ns** |
| 15 | 11.22 ns | 12.77 ns | **loses 1.55 ns** |
| 64 | 5.911 ns | 7.935 ns | **loses 2.02 ns** |
| 125 | 14.74 ns | 17.52 ns | **loses 2.78 ns** |
| 4 096 | 186.8 ns | 189.7 ns | loses 2.9 ns |

It does not pay. The branch is taken on every call, so it is charged to the
8-to-125-byte payloads where the wide form's win actually begins, and it loses
more there than it recovers below eight. The sizes it helps are the sizes
already too cheap to matter: a control frame is capped at 125 bytes by §5.5, and
the whole spread across the sub-8 rows is under four nanoseconds — against a
`ParseWSFrameHeader` that costs 29 ns on the same frame.

## What it was worth in situ

The rows above are an isolated harness. `internal/service/net/websocket` is
where the transform is actually called, and that number is the one that counts.
Measured by alternating the two implementations in the same session — base, new,
base, new — so machine drift cannot be read as a result:

| `internal/service/net/websocket` | before | after | speed-up |
|---|---:|---:|---:|
| `BenchmarkReceive/binary/4096` | 3 250 ns | **526.5 ns** | **6.17×** |
| `BenchmarkReceive/binary/65536` | 48 310 ns | 5 419 ns | 8.91× |
| `BenchmarkReceive/binary/1048576` | 775 600 ns | 110 400 ns | 7.02× |
| `BenchmarkReceive/binary/64` | 198.2 ns | 137.4 ns | 1.44× |
| `BenchmarkReceive/text/4096` | 3 410 ns | 681.8 ns | 5.00× |
| `BenchmarkReceiveFragmented/1x` | 49 040 ns | 5 411 ns | 9.06× |
| `BenchmarkReceiveFragmented/32x` | 54 060 ns | 10 560 ns | 5.12× |
| `BenchmarkReceiveFragmented/256x` | 80 250 ns | 31 740 ns | **2.53×** |

A CPU profile had attributed **88.99 %** of `BenchmarkReceive/binary/4096` to
`ApplyWSMask`, which caps the achievable speed-up at 9.1× and predicts 6.1× for
a transform 16× faster. It came out at 6.17×, so the profile's attribution was
right to two significant figures — and the arithmetic is worth keeping: 3 250 ns
minus 2 888 ns of masking leaves 362 ns of everything else, and 362 + 187 = 549
against a measured 526.

The last row is the same fact from the other end. The profile put only 62.18 %
on the mask for the most fragmented shape, which predicts 2.6×; it measured
2.53×. Two hundred and fifty-six frames carrying 64 KiB pay 256 header reads,
parses and fragmentation checks, and **that** is now what a fragmented message
costs. The per-frame rows above stopped being incidental the moment the per-byte
rows got cheap.

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

> **Numbers vary across machines**, and the per-byte rows more than most: they
> are memory bandwidth and the compiler's instruction selection, both of which
> differ sharply across CPUs. The allocation column is exact. What this report
> asserts is the **ratios** — word-vs-byte, mask-vs-validate, and
> ASCII-vs-multibyte.
>
> **Every published number is the median of three runs** at `-benchtime=1s`,
> except the two 1 MiB rows in §The word transform, which are the median of five
> runs taken in isolation for the reason given there. The in-situ table is the
> median of three alternating base/new rounds. Spreads were checked: every row
> in this file sits inside 4 % across its three runs apart from
> `AppendWSFrame, 4 KiB` (11 %) and the 1 MiB masking rows (14–23 %, diagnosed
> above).
>
> Machine load at measurement time was `load average: 0.65–1.53`, all of it this
> benchmark; no other job was running.

| Dimension | Value |
|---|---|
| CPU cores          | 8 |
| RAM                | 15 GiB |
| OS / kernel        | Linux 6.12.101+deb13-amd64 |
| Architecture       | amd64 |
| Go toolchain       | go1.27.1 linux/amd64 |
| Git branch         | jaimerias-que-tu-te-connect |
| Git commit         | 9d9c09f |
| Generated (UTC)    | 2026-09-10 |
| Bench wall-clock   | `-test.benchtime=1s`, `-test.count=3`, medians |

## Results

Medians of three runs. `wide` is the shipped transform, `byte_at_a_time` the
form it replaced (and the correctness oracle), `short_guarded` the alternative
measured and refused in §No short-payload branch. Sub-benchmark lengths are
zero-padded so they sort in numeric order.

```
goos: linux
goarch: amd64
pkg: github.com/kitsunium/sdk/internal/core/net
cpu: AMD EPYC 7351P 16-Core Processor
BenchmarkApplyWSMask_64B-8                          	     5.724 ns/op	11181.89 MB/s	0 B/op	0 allocs/op
BenchmarkApplyWSMask_4KiB-8                         	     186.8 ns/op	21927.90 MB/s	0 B/op	0 allocs/op
BenchmarkApplyWSMask_1MiB-8                         	     47902 ns/op	21890.08 MB/s	0 B/op	0 allocs/op
BenchmarkApplyWSMask/wide/0000000-8                 	     3.517 ns/op	0 B/op	0 allocs/op
BenchmarkApplyWSMask/wide/0000001-8                 	     4.196 ns/op	238.31 MB/s	0 B/op	0 allocs/op
BenchmarkApplyWSMask/wide/0000003-8                 	     6.292 ns/op	476.76 MB/s	0 B/op	0 allocs/op
BenchmarkApplyWSMask/wide/0000004-8                 	     7.343 ns/op	544.72 MB/s	0 B/op	0 allocs/op
BenchmarkApplyWSMask/wide/0000007-8                 	     10.53 ns/op	664.53 MB/s	0 B/op	0 allocs/op
BenchmarkApplyWSMask/wide/0000008-8                 	     4.193 ns/op	1907.98 MB/s	0 B/op	0 allocs/op
BenchmarkApplyWSMask/wide/0000015-8                 	     11.22 ns/op	1337.08 MB/s	0 B/op	0 allocs/op
BenchmarkApplyWSMask/wide/0000064-8                 	     5.911 ns/op	10827.42 MB/s	0 B/op	0 allocs/op
BenchmarkApplyWSMask/wide/0000125-8                 	     14.74 ns/op	8481.18 MB/s	0 B/op	0 allocs/op
BenchmarkApplyWSMask/wide/0004096-8                 	     186.8 ns/op	21923.18 MB/s	0 B/op	0 allocs/op
BenchmarkApplyWSMask/wide/0004097-8                 	     187.7 ns/op	21826.65 MB/s	0 B/op	0 allocs/op
BenchmarkApplyWSMask/wide/0065536-8                 	      2948 ns/op	22232.96 MB/s	0 B/op	0 allocs/op
BenchmarkApplyWSMask/wide/1048576-8                 	     63019 ns/op	16639.02 MB/s	0 B/op	0 allocs/op
BenchmarkApplyWSMask/byte_at_a_time/0000000-8       	     2.791 ns/op	0 B/op	0 allocs/op
BenchmarkApplyWSMask/byte_at_a_time/0000001-8       	     3.318 ns/op	301.39 MB/s	0 B/op	0 allocs/op
BenchmarkApplyWSMask/byte_at_a_time/0000003-8       	     4.206 ns/op	713.19 MB/s	0 B/op	0 allocs/op
BenchmarkApplyWSMask/byte_at_a_time/0000004-8       	     4.596 ns/op	870.37 MB/s	0 B/op	0 allocs/op
BenchmarkApplyWSMask/byte_at_a_time/0000007-8       	     6.563 ns/op	1066.56 MB/s	0 B/op	0 allocs/op
BenchmarkApplyWSMask/byte_at_a_time/0000008-8       	     7.081 ns/op	1129.83 MB/s	0 B/op	0 allocs/op
BenchmarkApplyWSMask/byte_at_a_time/0000015-8       	     12.23 ns/op	1226.49 MB/s	0 B/op	0 allocs/op
BenchmarkApplyWSMask/byte_at_a_time/0000064-8       	     56.84 ns/op	1125.96 MB/s	0 B/op	0 allocs/op
BenchmarkApplyWSMask/byte_at_a_time/0000125-8       	     103.3 ns/op	1209.55 MB/s	0 B/op	0 allocs/op
BenchmarkApplyWSMask/byte_at_a_time/0004096-8       	      3006 ns/op	1362.67 MB/s	0 B/op	0 allocs/op
BenchmarkApplyWSMask/byte_at_a_time/0004097-8       	      3007 ns/op	1362.46 MB/s	0 B/op	0 allocs/op
BenchmarkApplyWSMask/byte_at_a_time/0065536-8       	     48186 ns/op	1360.07 MB/s	0 B/op	0 allocs/op
BenchmarkApplyWSMask/byte_at_a_time/1048576-8       	    773112 ns/op	1356.31 MB/s	0 B/op	0 allocs/op
BenchmarkApplyWSMask/short_guarded/0000000-8        	     3.083 ns/op	0 B/op	0 allocs/op
BenchmarkApplyWSMask/short_guarded/0000001-8        	     3.585 ns/op	278.93 MB/s	0 B/op	0 allocs/op
BenchmarkApplyWSMask/short_guarded/0000003-8        	     4.582 ns/op	654.80 MB/s	0 B/op	0 allocs/op
BenchmarkApplyWSMask/short_guarded/0000004-8        	     5.147 ns/op	777.08 MB/s	0 B/op	0 allocs/op
BenchmarkApplyWSMask/short_guarded/0000007-8        	     7.035 ns/op	995.09 MB/s	0 B/op	0 allocs/op
BenchmarkApplyWSMask/short_guarded/0000008-8        	     7.013 ns/op	1140.80 MB/s	0 B/op	0 allocs/op
BenchmarkApplyWSMask/short_guarded/0000015-8        	     12.77 ns/op	1174.77 MB/s	0 B/op	0 allocs/op
BenchmarkApplyWSMask/short_guarded/0000064-8        	     7.935 ns/op	8065.72 MB/s	0 B/op	0 allocs/op
BenchmarkApplyWSMask/short_guarded/0000125-8        	     17.52 ns/op	7136.22 MB/s	0 B/op	0 allocs/op
BenchmarkApplyWSMask/short_guarded/0004096-8        	     189.7 ns/op	21593.80 MB/s	0 B/op	0 allocs/op
BenchmarkApplyWSMask/short_guarded/0004097-8        	     190.4 ns/op	21521.85 MB/s	0 B/op	0 allocs/op
BenchmarkApplyWSMask/short_guarded/0065536-8        	      2958 ns/op	22152.39 MB/s	0 B/op	0 allocs/op
BenchmarkApplyWSMask/short_guarded/1048576-8        	     51899 ns/op	20204.36 MB/s	0 B/op	0 allocs/op
BenchmarkValidateWSText_64B-8                       	     21.43 ns/op	2986.39 MB/s	0 B/op	0 allocs/op
BenchmarkValidateWSText_4KiB-8                      	     140.7 ns/op	29116.65 MB/s	0 B/op	0 allocs/op
BenchmarkValidateWSText_1MiB-8                      	     33043 ns/op	31733.83 MB/s	0 B/op	0 allocs/op
BenchmarkValidateWSText_Multibyte-8                 	      3891 ns/op	1052.36 MB/s	0 B/op	0 allocs/op
BenchmarkWSFrameHeaderLen-8                         	     2.955 ns/op	0 B/op	0 allocs/op
BenchmarkParseWSFrameHeader-8                       	     29.46 ns/op	0 B/op	0 allocs/op
BenchmarkAppendWSFrame_64B-8                        	     14.62 ns/op	4376.13 MB/s	0 B/op	0 allocs/op
BenchmarkAppendWSFrame_4KiB-8                       	     117.4 ns/op	34904.11 MB/s	0 B/op	0 allocs/op
BenchmarkAppendWSClosePayload-8                     	     20.83 ns/op	0 B/op	0 allocs/op
BenchmarkParseWSClosePayload-8                      	      44.6 ns/op	4 B/op	1 allocs/op
```

`wide/1048576` reads 63 019 ns here and 49 207 ns in §The word transform. That is
not a transcription error — it is the diagnosed row, measured inside the
39-benchmark sweep above and again in isolation. The section says which is
which and why they differ; nothing else in this file moves between runs.
