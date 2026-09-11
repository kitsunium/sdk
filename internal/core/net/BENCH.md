<!-- generated from internal/core/net/{websocket,sse}_bench_test.go — run `cd internal/core && GOWORK=off go test -run='^$' -bench=. -benchmem -benchtime=1s -count=3 ./net/` to refresh; every published number is the MEDIAN of the three -->
# Benchmarks — `internal/core/net`

The two wire formats this package encodes and decodes per byte: the WebSocket
frame (ADR 0047) and the Server-Sent Events frame (ADR 0029). Everything below
is **zero allocations** except one row, and each half's most useful fact is a
comparison nobody would guess.

Both halves turned out to have the same defect, found the same way and fixed
the same way: a per-byte loop written with the obvious standard-library call,
where the standard library's obvious call is the slow one. WebSocket's was
`ApplyWSMask` at one byte per iteration; SSE's was `strings.IndexAny`, which
below is **91 % of encoding a frame**. Neither was visible without a profile,
and neither is exotic — they are what a careful person writes.

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

## SSE: 91 % of encoding a frame was one standard-library call

An event stream's per-byte work is finding the line terminators in `Data`,
because the SSE format has no escape: a terminator SPLITS the payload into
another `data:` line. `cutSSELine` asked `strings.IndexAny(s, "\n\r")` for the
first one, which is the obvious call and reads like the cheap one.

It is not. `IndexAny` has no `bytealg` path: for `len(s) > 8` it builds a
32-byte ASCII set **per call** and then walks the string one byte at a time
through `asciiSet.contains`; for `len(s) <= 8` it decodes a RUNE per byte and
calls `IndexRune` on each. `strings.IndexByte` is the assembly-backed
primitive — measured over 64 KiB it is ~30 GB/s per pass against `IndexAny`'s
2.4 — and the common SSE event, a single-line JSON payload with no terminator
anywhere in it, pays a full scan of the whole payload at the slow rate purely to
prove the absence.

The CPU profile of one 4 KiB single-line frame, before, verbatim:

```
Showing nodes accounting for 3.57s, 100% of 3.57s total
      flat  flat%   sum%        cum   cum%
     2.77s 77.59% 77.59%      2.77s 77.59%  strings.(*asciiSet).contains (inline)
     0.51s 14.29% 91.88%      3.28s 91.88%  strings.IndexAny
     0.26s  7.28% 99.16%      0.26s  7.28%  runtime.memmove
     0.02s  0.56% 99.72%      0.28s  7.84%  github.com/kitsunium/sdk/internal/core/net.appendSSEField (inline)
     0.01s  0.28%   100%      3.57s   100%  github.com/kitsunium/sdk/internal/core/net.SSEEventValue.AppendTo
         0     0%   100%      3.53s 98.88%  github.com/kitsunium/sdk/internal/core/net.appendSSEData
         0     0%   100%      3.25s 91.04%  github.com/kitsunium/sdk/internal/core/net.cutSSELine
```

`runtime.memmove` — copying the payload, which is the only work the function
actually has to do — is **7.28 %**. The scan is 91 %.

And after, on the same benchmark:

```
Showing nodes accounting for 3.61s, 99.72% of 3.62s total
      flat  flat%   sum%        cum   cum%
     2.36s 65.19% 65.19%      2.36s 65.19%  indexbytebody
     1.02s 28.18% 93.37%      1.02s 28.18%  runtime.memmove
     0.08s  2.21% 95.58%      3.54s 97.79%  github.com/kitsunium/sdk/internal/core/net.appendSSEData
     0.04s  1.10% 96.69%      0.04s  1.10%  internal/bytealg.IndexByteString
     0.03s  0.83% 97.51%      1.05s 29.01%  github.com/kitsunium/sdk/internal/core/net.appendSSEField (inline)
     0.02s  0.55% 98.07%      0.02s  0.55%  github.com/kitsunium/sdk/internal/core/net.validateSSELine
```

The scan is still the largest entry, and now it should be: two assembly passes
over the payload is the floor for proving two bytes are absent from it. The
copy went from 7 % to 28 % of a function that got 4.5× faster, which is the
same statement.

## Encoding a frame: 1.6× to 4.6×, and where each number comes from

Medians of three, `-benchtime=1s`. Zero allocations on every row, before and
after — this was never an allocation problem, which is exactly why nothing in
the tree had noticed it.

| `SSEEventValue.AppendTo` | before | after | | after |
|---|---:|---:|---:|---:|
| single-line, 64 B | 104.5 ns | **40.80 ns** | **2.56×** | 1 569 MB/s |
| single-line, 256 B | 193.1 ns | **57.85 ns** | **3.34×** | 4 425 MB/s |
| single-line, 1 KiB | 538.8 ns | **147.1 ns** | **3.66×** | 6 961 MB/s |
| single-line, 4 KiB | 1 945 ns | **430.8 ns** | **4.51×** | 9 507 MB/s |
| single-line, 64 KiB | 30 397 ns | **6 582 ns** | **4.62×** | 9 956 MB/s |
| LF every 64 B, 4 KiB | 5 195 ns | **1 624 ns** | 3.20× | 2 522 MB/s |
| LF every 64 B, 64 KiB | 81 836 ns | **25 122 ns** | 3.26× | 2 609 MB/s |
| CRLF every 64 B, 4 KiB | 5 238 ns | **2 153 ns** | 2.43× | 1 903 MB/s |
| CRLF every 64 B, 64 KiB | 81 465 ns | **33 653 ns** | 2.42× | 1 947 MB/s |
| id + event + 256 B payload | 276.6 ns | **98.32 ns** | **2.81×** | — |
| `Validate`, id + event | 62.96 ns | **36.33 ns** | 1.73× | — |
| `Validate`, data only | 20.86 ns | **12.16 ns** | 1.72× | — |
| `AppendSSEComment` (keep-alive) | 31.85 ns | **19.99 ns** | 1.59× | — |

The single-line rows are the ones that matter: a payload with no terminator is
the overwhelmingly common event, and it is also the worst case for the scan,
which cannot stop early. A multi-line payload gains less because the CRLF rows
refresh both cursors on every line — see the next section.

The arithmetic cross-checks against the isolated scan rows below. At 64 KiB the
scan alone is 4 329 ns and the profile puts `memmove` at 28 % of 6 582, i.e.
≈ 1 840 ns; 4 329 + 1 840 = 6 169 against 6 582 measured, a 6 % gap that is the
field framing. At 4 KiB: 296.3 + ≈ 120 = 416 against 430.8, a 3 % gap.

`Validate` moved for a second, smaller reason. It calls `validateSSELine` for
`id` and for `event` on EVERY frame, present or not, and `strings.ContainsAny`
is `IndexAny` again. The substitution is the same one
`internal/service/proc/sdnotify` already documents ("two byte searches, not
ContainsAny … 24 % of this function"). One extra guard earned its place there:
on the EMPTY string `ContainsAny` returns without looking at anything while two
`IndexByte` calls still happen, so the naive substitution made a data-only
`Validate` **0.80×** — 26.2 ns against 20.9. Guarding on `value != ""` first
took it to 12.2 ns and costs nothing measurable where the value is present.
That row is published because it was the one row of this change that measured
SLOWER, and it stayed slower until it was looked at.

## Two obvious scans are quadratic, in mirror-image halves

`IndexAny` finds the first of two bytes in one pass. `IndexByte` finds one byte,
so replacing it means two calls — and where those two calls go decides whether
the walk stays linear.

`cutSSELine` used to be called once per LINE, over the remaining payload. Two
unbounded `IndexByte` calls per line means the scan for a byte that is **not in
the payload at all** re-reads the whole tail on every line. Bounding the CR scan
by where the LF was found fixes the LF-terminated payload and leaves the exact
mirror broken, because now it is the LF scan that is unbounded when there is no
LF. Both were measured before either was believed:

| 64 KiB payload, whole-payload split | no terminator | LF every 64 B | CRLF every 64 B | CR every 64 B |
|---|---:|---:|---:|---:|
| `index_any` (the form replaced) | 27 737 ns | 67 534 ns | 67 210 ns | 69 034 ns |
| `two_index_byte` | 4 346 ns | **1 115 791 ns** | 20 920 ns | **1 109 329 ns** |
| `bounded_index_byte` | 4 347 ns | 18 601 ns | 28 181 ns | **1 114 228 ns** |
| `cursor` (shipped) | **4 329 ns** | **19 730 ns** | **27 311 ns** | **19 715 ns** |

The three bold blow-ups are 16× SLOWER than the code being replaced, on inputs
a caller supplies. `Data` is application data, so "a payload of LF-terminated
lines" is a log tail and "a payload of CR-terminated lines" is something a
stranger can send; neither is exotic and both are quadratic.

The shipped form keeps a cursor per terminator byte over the whole payload and
only ever moves each one FORWARD, re-scanning a cursor only when the cut just
made consumed or overtook it. Each byte is therefore examined at most once by
each of the two searches — two linear passes, whatever the shape:

| whole-payload split, `cursor` vs `index_any` | 64 B | 256 B | 1 KiB | 4 KiB | 64 KiB |
|---|---:|---:|---:|---:|---:|
| no terminator | 3.76× | 4.92× | 5.98× | 6.00× | **6.41×** |
| LF every 64 B | 2.68× | 3.22× | 3.20× | 3.35× | 3.42× |
| CRLF every 64 B | 2.14× | 2.31× | 2.33× | 2.46× | 2.46× |
| CR every 64 B | 2.64× | 3.18× | 3.35× | 3.38× | 3.50× |

It costs about **3 % more than the two naive forms on the common case** (18.67
against 18.08 ns at 64 B; at 64 KiB it is 4 329 against 4 346, i.e. inside the
noise), and that is the whole price of not having a quadratic corpus. CRLF is
the slowest shape because a CRLF cut consumes BOTH cursors and therefore
refreshes both — two `IndexByte` calls per line instead of one. It is still
linear; the row is there so nobody reads 2.46× as a defect.

Equivalence is not assumed. `TestSSELineSplitStrategiesAgreeExhaustively`
judges all four strategies against the `index_any` oracle over every string of
length 0 to 9 in `{'a', '\n', '\r'}` — 29 524 payloads, which is every
arrangement of LF, CR, CRLF, LFCR, a leading terminator, a trailing one and a
run of them — plus hand-written multi-byte cases, and the bound is 9 rather
than 8 because `IndexAny` itself changes strategy at `len(s) > 8`.
`TestAppendToMatchesTheIndexAnyOracle` then re-encodes the same corpus from the
ORACLE's lines and requires byte-identical frames, because agreeing on a split
proves nothing if the encoder does not use that split.

## What was refused

- **A single pass that finds either byte.** There is no `bytealg` primitive for
  "first of two bytes", and a hand-written SWAR pass in pure Go would be
  competing with assembly that already runs at 15 GB/s. Two passes at that rate
  beat one pass at 2.2.
- **Skipping the scan when `Data` is known to be single-line.** Proving the
  absence IS the scan; there is nothing cheaper to check first.
- **Escaping a terminator instead of splitting on it.** The format has no
  escape (ADR 0029), and this is a benchmark file, not a licence to change the
  wire.

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
> above). The SSE rows added in 2026-09 sit inside 6 % apart from
> `AppendSSEComment` before (4.7 %) and `AppendTo single_line/64 B` after
> (5.6 %), both of which are tens of nanoseconds where the timer's own
> resolution is a visible share.
>
> **One SSE row-set was thrown away**, and the cause is worth recording because
> nothing about the numbers looked wrong: the "before" arm reported IDENTICAL
> medians to the "after" arm on all nineteen rows, 0.98×–1.02×. The backup the
> revert restored from had been taken AFTER the patch, so both arms ran the new
> code. It was caught by arithmetic and not by inspection — a 4.5× change had
> already been measured in a single-run pass, and a comparison that says 1.00×
> against a known 4.5× is reporting on the harness. The re-run restored the
> original from `git show HEAD:` instead of from a local copy.
>
> Machine load at measurement time was `load average: 0.28–1.40`, all of it this
> benchmark; no other job was running. The clock source is `kvm-clock` — a
> virtualised host — which matters for the consumer of these numbers rather than
> for them: `time.Now()` is ~72 ns here against ~20 ns on a bare-metal TSC host,
> and it is the single largest entry in the service layer's deadline-enabled
> send path.

| Dimension | Value |
|---|---|
| CPU cores          | 8 |
| RAM                | 15 GiB |
| OS / kernel        | Linux 6.12.101+deb13-amd64 |
| Architecture       | amd64 |
| Go toolchain       | go1.27.1 linux/amd64 |
| Git branch         | jaimerias-que-tu-te-connect |
| Git commit         | a486bb3 |
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


### SSE frame encoding

Medians of three at `-benchtime=1s`. The `before` block is `git show
HEAD:internal/core/net/sse.go` restored into the tree and re-measured on the
same binary; the `after` block is what ships.

```
goos: linux
goarch: amd64
pkg: github.com/kitsunium/sdk/internal/core/net
cpu: AMD EPYC 7351P 16-Core Processor
                                            BEFORE          AFTER
BenchmarkSSEAppendTo/single_line/0000064      104.5 ns/op    40.80 ns/op   0 B/op  0 allocs/op
BenchmarkSSEAppendTo/single_line/0000256      193.1 ns/op    57.85 ns/op   0 B/op  0 allocs/op
BenchmarkSSEAppendTo/single_line/0001024      538.8 ns/op    147.1 ns/op   0 B/op  0 allocs/op
BenchmarkSSEAppendTo/single_line/0004096       1945 ns/op    430.8 ns/op   0 B/op  0 allocs/op
BenchmarkSSEAppendTo/single_line/0065536      30397 ns/op     6582 ns/op   0 B/op  0 allocs/op
BenchmarkSSEAppendTo/multi_line_lf/0000064    137.6 ns/op    57.99 ns/op   0 B/op  0 allocs/op
BenchmarkSSEAppendTo/multi_line_lf/0000256    378.2 ns/op    151.4 ns/op   0 B/op  0 allocs/op
BenchmarkSSEAppendTo/multi_line_lf/0001024     1357 ns/op    441.7 ns/op   0 B/op  0 allocs/op
BenchmarkSSEAppendTo/multi_line_lf/0004096     5195 ns/op     1624 ns/op   0 B/op  0 allocs/op
BenchmarkSSEAppendTo/multi_line_lf/0065536    81836 ns/op    25122 ns/op   0 B/op  0 allocs/op
BenchmarkSSEAppendTo/multi_line_crlf/0000064  136.9 ns/op    66.08 ns/op   0 B/op  0 allocs/op
BenchmarkSSEAppendTo/multi_line_crlf/0000256  360.7 ns/op    190.6 ns/op   0 B/op  0 allocs/op
BenchmarkSSEAppendTo/multi_line_crlf/0001024   1336 ns/op    589.7 ns/op   0 B/op  0 allocs/op
BenchmarkSSEAppendTo/multi_line_crlf/0004096   5238 ns/op     2153 ns/op   0 B/op  0 allocs/op
BenchmarkSSEAppendTo/multi_line_crlf/0065536  81465 ns/op    33653 ns/op   0 B/op  0 allocs/op
BenchmarkSSEAppendToFullFrame                 276.6 ns/op    98.32 ns/op   0 B/op  0 allocs/op
BenchmarkSSEValidate/id_and_name              62.96 ns/op    36.33 ns/op   0 B/op  0 allocs/op
BenchmarkSSEValidate/data_only                20.86 ns/op    12.16 ns/op   0 B/op  0 allocs/op
BenchmarkAppendSSEComment                     31.85 ns/op    19.99 ns/op   0 B/op  0 allocs/op
```

The whole-payload line-split sweep — four strategies × four corpus shapes ×
five sizes, 80 rows, all zero-allocation — is summarised in §Two obvious scans
are quadratic. The 64 KiB column is reproduced there in full; the smaller sizes
scale linearly for every strategy except the three diagnosed blow-ups, which
scale with the SQUARE of the payload and are the reason the sweep exists.
