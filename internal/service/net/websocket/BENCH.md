<!-- generated from internal/service/net/websocket/websocket_bench_test.go — run `cd internal/service && GOWORK=off go test -run='^$' -bench=. -benchmem -benchtime=1s -count=5 ./net/websocket/` to refresh -->
# Benchmarks — `internal/service/net/websocket`

RFC 6455's server side, measured along the four axes a caller and an **attacker**
each control: payload size, fragment count, text versus binary, and which of the
MUST-fails a peer chooses to trip.

The refusals are half the report, and that is the point of running this campaign
on this package at all. Every MUST-fail here is a branch the peer picks, so a
refusal that costs more than the acceptance it aborts is a denial-of-service
lever wearing a conformance justification. The question is answered with a
number, not with an argument:

> **What does it cost this server to refuse, against what it costs to comply —
> and against what it costs the attacker to get there?**

## Reproducibility envelope

> **Numbers vary across machines.** This report stamps the box that produced
> them so cross-machine deltas can be evaluated honestly. This is a
> memory-ballooned VM: 15 GiB provisioned against a balloon that the host may
> reclaim mid-run, so nothing else was running and every family was measured
> serially.

| Dimension | Value |
|---|---|
| CPU | AMD EPYC 7351P 16-Core Processor |
| CPU cores | 8 |
| RAM | 15 GiB (ballooned VM — see above) |
| OS / kernel | Linux 6.12.101+deb13-amd64 |
| Architecture | amd64 |
| Go toolchain | go1.27.1 linux/amd64 |
| Git branch | `jaimerias-que-tu-te-connect` |
| Git commit | `9d9c09f` |
| Generated (UTC) | 2026-09-10 |
| Load average | 0.42 / 0.57 / 0.86 at start, 1.35 / 1.04 / 0.99 at end |
| Bench wall-clock | `-benchtime=1s`, `-count=5`, 251 s for the main suite |

**Every published number is the MEDIAN OF FIVE RUNS**, except the fragmentation
family, which is the median of NINE (it is the one family whose finding needed
the extra resolution — see §4). The full five values are given for every row so
the reader can judge the spread rather than trust the median. Spread is
max−min as a percentage of the median; it is under 3 % for every row outside the
fragmentation family, which runs 1.6–6.8 % and is discussed in §4, and
`LoopbackUpgrade`, which opens a real TCP connection per iteration and runs
12.2 %.

**Spread within one five-run set is not the same as drift between two sets.**
`Receive/binary/64` measured 186.0 ns in one session and 198.5 ns in another
fifteen minutes later, on byte-identical code — 6.3 % apart, with each set's own
spread under 3 %. That is this box, and it is why **no before/after comparison
in §6 is made against the table above**: each optimisation was measured against
its own baseline inside one contiguous session, and only matched pairs are
quoted.

## What the harness measures, and what it does not

The receive and send families run against `benchSocket`, an in-memory
`net.Conn` replaying a fixed script of hand-assembled client frames. That is
deliberate: the subject is the frame reader, and a loopback round trip buries it
under the scheduler — §7 shows by how much. Three things the fake socket makes
free, stated here rather than discovered later:

- `Write` is a no-op, so the **send rows are a floor** and not a wire figure.
- `SetWriteDeadline` returns immediately, where a real socket updates the
  runtime poller once per frame.
- `SetDeadline` at the upgrade is likewise free.

Nothing on the **inbound** path writes, so the receive rows are unaffected by
all three. Every benchmark runs with `WithoutPing()`; that starts no heartbeat
goroutine but changes no code on the read path — `rx.Add(1)` runs per frame
either way — so the numbers hold for a default-configuration connection.

## Results

```
BenchmarkReceive/binary/64-8                       198.5 ns/op    322.4 MB/s      0 B/op   0 allocs/op
BenchmarkReceive/binary/4096-8                    3274   ns/op   1251.2 MB/s      0 B/op   0 allocs/op
BenchmarkReceive/binary/65536-8                  48528   ns/op   1350.5 MB/s      0 B/op   0 allocs/op
BenchmarkReceive/binary/1048576-8               784955   ns/op   1335.8 MB/s      0 B/op   0 allocs/op
BenchmarkReceive/text/64-8                         240.5 ns/op    266.1 MB/s      0 B/op   0 allocs/op
BenchmarkReceive/text/4096-8                      3426   ns/op   1195.5 MB/s      0 B/op   0 allocs/op
BenchmarkReceive/text/65536-8                    50989   ns/op   1285.3 MB/s      0 B/op   0 allocs/op
BenchmarkReceive/text/1048576-8                 823286   ns/op   1273.7 MB/s      0 B/op   0 allocs/op

BenchmarkReceiveFragmented/1x-8                  48957   ns/op   1338.7 MB/s      0 B/op   0 allocs/op
BenchmarkReceiveFragmented/2x-8                  48729   ns/op   1344.9 MB/s      0 B/op   0 allocs/op
BenchmarkReceiveFragmented/8x-8                  50376   ns/op   1301.0 MB/s      0 B/op   0 allocs/op
BenchmarkReceiveFragmented/16x-8                 52504   ns/op   1248.2 MB/s      0 B/op   0 allocs/op
BenchmarkReceiveFragmented/32x-8                 54396   ns/op   1204.8 MB/s      0 B/op   0 allocs/op
BenchmarkReceiveFragmented/256x-8                79899   ns/op    820.2 MB/s      0 B/op   0 allocs/op

BenchmarkReceiveControl/none-8                     516.5 ns/op    495.7 MB/s      0 B/op   0 allocs/op
BenchmarkReceiveControl/ping_empty-8               726.7 ns/op    352.3 MB/s      0 B/op   0 allocs/op
BenchmarkReceiveControl/ping_125B-8                861.5 ns/op    297.1 MB/s      0 B/op   0 allocs/op
BenchmarkReceiveControl/pong_empty-8               587.2 ns/op    435.9 MB/s      0 B/op   0 allocs/op

BenchmarkSend/binary/64-8                          124.0 ns/op    516.1 MB/s      0 B/op   0 allocs/op
BenchmarkSend/binary/4096-8                        232.4 ns/op  17625.7 MB/s      0 B/op   0 allocs/op
BenchmarkSend/binary/65536-8                      2394   ns/op  27372.8 MB/s      0 B/op   0 allocs/op
BenchmarkSend/text/64-8                            167.6 ns/op    381.9 MB/s      0 B/op   0 allocs/op
BenchmarkSend/text/4096-8                          396.5 ns/op  10329.8 MB/s      0 B/op   0 allocs/op
BenchmarkSend/text/65536-8                        4391   ns/op  14926.3 MB/s      0 B/op   0 allocs/op

BenchmarkFirstFrame/baseline-8                     119.9 ns/op                   160 B/op   2 allocs/op
BenchmarkFirstFrame/accept_binary/64-8             600.2 ns/op                   640 B/op   4 allocs/op
BenchmarkFirstFrame/accept_binary/4096-8          4885   ns/op                  4672 B/op   4 allocs/op
BenchmarkFirstFrame/accept_text/4096-8            5051   ns/op                  4672 B/op   4 allocs/op
BenchmarkFirstFrame/accept_bounded/4096-8         4860   ns/op                  4672 B/op   4 allocs/op
BenchmarkConnLifecycle-8                          2307   ns/op                  1408 B/op   9 allocs/op

BenchmarkRefusal/unmasked_client_frame-8           896.4 ns/op                   848 B/op   5 allocs/op
BenchmarkRefusal/reserved_bit-8                    880.7 ns/op                   848 B/op   5 allocs/op
BenchmarkRefusal/reserved_opcode-8                 881.5 ns/op                   848 B/op   5 allocs/op
BenchmarkRefusal/oversized_control_frame-8         958.7 ns/op                   912 B/op   5 allocs/op
BenchmarkRefusal/fragmented_control_frame-8        900.1 ns/op                   848 B/op   5 allocs/op
BenchmarkRefusal/non_minimal_length_16-8           906.8 ns/op                   848 B/op   5 allocs/op
BenchmarkRefusal/non_minimal_length_64-8           901.2 ns/op                   848 B/op   5 allocs/op
BenchmarkRefusal/orphan_continuation-8             931.6 ns/op                   784 B/op   5 allocs/op
BenchmarkRefusal/interrupted_fragmentation-8      1121   ns/op                   856 B/op   6 allocs/op
BenchmarkRefusal/frame_ceiling-8                   959.0 ns/op                   912 B/op   5 allocs/op
BenchmarkRefusal/message_ceiling-8                5705   ns/op                  5008 B/op   6 allocs/op
BenchmarkRefusal/invalid_utf8/4096-8              5656   ns/op                  4944 B/op   6 allocs/op

BenchmarkLoopback/64-8                           16698   ns/op      3.8 MB/s      80 B/op   3 allocs/op
BenchmarkLoopback/4096-8                         27012   ns/op    151.6 MB/s    4112 B/op   3 allocs/op
BenchmarkLoopbackUpgrade-8                      262653   ns/op                 20167 B/op 112 allocs/op
```

### Every run behind every median

| Benchmark | runs (ns/op) | median | spread |
|---|---|---|---|
| `Receive/binary/64` | 197.1, 199.6, 198.2, 198.5, 200.2 | **198.5** | 1.6 % |
| `Receive/binary/4096` | 3 245.0, 3 242.0, 3 283.0, 3 325.0, 3 274.0 | **3 274.0** | 2.5 % |
| `Receive/binary/65536` | 48 519, 48 753, 49 182, 48 465, 48 528 | **48 528** | 1.5 % |
| `Receive/binary/1048576` | 776 182, 784 981, 784 955, 780 616, 790 634 | **784 955** | 1.8 % |
| `Receive/text/64` | 238.6, 241.6, 240.5, 243.3, 240.5 | **240.5** | 2.0 % |
| `Receive/text/4096` | 3 474.0, 3 409.0, 3 426.0, 3 404.0, 3 429.0 | **3 426.0** | 2.0 % |
| `Receive/text/65536` | 51 272, 50 816, 50 920, 52 560, 50 989 | **50 989** | 3.4 % |
| `Receive/text/1048576` | 837 742, 827 113, 823 286, 820 408, 818 459 | **823 286** | 2.3 % |
| `ReceiveControl/none` | 517.7, 523.8, 512.5, 516.5, 514.8 | **516.5** | 2.2 % |
| `ReceiveControl/ping_empty` | 723.0, 728.9, 722.0, 726.7, 730.4 | **726.7** | 1.2 % |
| `ReceiveControl/ping_125B` | 865.5, 865.4, 856.1, 854.6, 861.5 | **861.5** | 1.3 % |
| `ReceiveControl/pong_empty` | 593.1, 586.7, 585.4, 587.2, 590.0 | **587.2** | 1.3 % |
| `Send/binary/64` | 124.0, 123.8, 124.3, 124.9, 123.0 | **124.0** | 1.5 % |
| `Send/binary/4096` | 231.5, 232.4, 231.7, 232.9, 235.9 | **232.4** | 1.9 % |
| `Send/binary/65536` | 2 429.0, 2 386.0, 2 394.0, 2 388.0, 2 431.0 | **2 394.0** | 1.9 % |
| `Send/text/64` | 168.5, 167.5, 167.0, 167.6, 167.7 | **167.6** | 0.9 % |
| `Send/text/4096` | 392.4, 396.5, 392.9, 396.5, 397.2 | **396.5** | 1.2 % |
| `Send/text/65536` | 4 402.0, 4 452.0, 4 391.0, 4 380.0, 4 378.0 | **4 391.0** | 1.7 % |
| `FirstFrame/baseline` | 121.9, 118.1, 120.1, 119.9, 119.8 | **119.9** | 3.2 % |
| `FirstFrame/accept_binary/64` | 580.7, 600.2, 602.2, 605.9, 593.1 | **600.2** | 4.2 % |
| `FirstFrame/accept_binary/4096` | 4 907.0, 4 847.0, 4 885.0, 4 905.0, 4 866.0 | **4 885.0** | 1.2 % |
| `FirstFrame/accept_text/4096` | 5 082.0, 5 017.0, 5 051.0, 5 095.0, 5 045.0 | **5 051.0** | 1.5 % |
| `FirstFrame/accept_bounded/4096` | 4 851.0, 4 851.0, 4 860.0, 4 906.0, 4 877.0 | **4 860.0** | 1.1 % |
| `ConnLifecycle` | 2 264.0, 2 317.0, 2 307.0, 2 300.0, 2 363.0 | **2 307.0** | 4.3 % |
| `Refusal/unmasked_client_frame` | 892.1, 900.6, 901.7, 896.4, 886.8 | **896.4** | 1.7 % |
| `Refusal/reserved_bit` | 884.6, 875.7, 883.2, 880.7, 875.5 | **880.7** | 1.0 % |
| `Refusal/reserved_opcode` | 888.1, 878.3, 872.2, 881.5, 888.6 | **881.5** | 1.9 % |
| `Refusal/oversized_control_frame` | 958.7, 960.9, 960.2, 942.8, 952.7 | **958.7** | 1.9 % |
| `Refusal/fragmented_control_frame` | 894.1, 901.2, 902.5, 897.7, 900.1 | **900.1** | 0.9 % |
| `Refusal/non_minimal_length_16` | 896.8, 912.9, 905.2, 906.8, 917.4 | **906.8** | 2.3 % |
| `Refusal/non_minimal_length_64` | 917.4, 897.5, 901.2, 905.1, 899.3 | **901.2** | 2.2 % |
| `Refusal/orphan_continuation` | 931.6, 941.1, 934.0, 929.4, 923.5 | **931.6** | 1.9 % |
| `Refusal/interrupted_fragmentation` | 1 122.0, 1 133.0, 1 121.0, 1 120.0, 1 121.0 | **1 121.0** | 1.2 % |
| `Refusal/frame_ceiling` | 952.7, 963.7, 952.6, 968.4, 959.0 | **959.0** | 1.6 % |
| `Refusal/message_ceiling` | 5 714.0, 5 711.0, 5 705.0, 5 660.0, 5 643.0 | **5 705.0** | 1.2 % |
| `Refusal/invalid_utf8/4096` | 5 618.0, 5 656.0, 5 688.0, 5 656.0, 5 735.0 | **5 656.0** | 2.1 % |
| `Loopback/64` | 16 698, 16 742, 16 658, 16 737, 16 680 | **16 698** | 0.5 % |
| `Loopback/4096` | 27 012, 26 863, 26 606, 27 245, 27 081 | **27 012** | 2.4 % |
| `LoopbackUpgrade` | 285 498, 266 479, 253 522, 261 178, 262 653 | **262 653** | 12.2 % |

The fragmentation family, nine runs:

| Benchmark | runs (ns/op) | median | spread |
|---|---|---|---|
| `ReceiveFragmented/1x` | 48 408, 48 437, 48 868, 48 957, 49 005, 48 595, 50 341, 50 513, 49 228 | **48 957** | 4.3 % |
| `ReceiveFragmented/2x` | 48 859, 48 498, 48 635, 48 687, 48 729, 49 221, 48 839, 48 507, 49 269 | **48 729** | 1.6 % |
| `ReceiveFragmented/8x` | 50 110, 50 433, 50 237, 51 305, 50 671, 51 133, 50 147, 50 100, 50 376 | **50 376** | 2.4 % |
| `ReceiveFragmented/16x` | 53 123, 51 674, 51 786, 52 504, 51 868, 52 609, 52 598, 51 875, 52 609 | **52 504** | 2.8 % |
| `ReceiveFragmented/32x` | 54 268, 54 396, 53 870, 57 580, 55 200, 55 127, 54 095, 53 951, 55 218 | **54 396** | 6.8 % |
| `ReceiveFragmented/256x` | 79 899, 79 976, 82 543, 80 675, 79 099, 80 767, 79 675, 79 658, 79 725 | **79 899** | 4.3 % |

Worst spreads: `LoopbackUpgrade` 12.2 % — a real TCP connect per iteration, used
below only as an order-of-magnitude denominator, which is all 12 % supports —
then `ConnLifecycle` 4.3 %, `accept_binary/64` 4.2 %, `text/65536` 3.4 %,
`baseline` 3.2 %. Everything else is under 3 %.

## The profile came first

No change was made to production code, and this is why. Quoted verbatim from
`go tool pprof -top`, over `BenchmarkReceive/binary/4096` at
`-benchtime=3s`:

```
Duration: 3.36s, Total samples = 3.36s (99.85%)
Showing nodes accounting for 3.32s, 98.81% of 3.36s total
      flat  flat%   sum%        cum   cum%
     2.99s 88.99% 88.99%      2.99s 88.99%  github.com/kitsunium/sdk/internal/core/net.ApplyWSMask (inline)
     0.16s  4.76% 93.75%      0.16s  4.76%  runtime.memmove
     0.05s  1.49% 95.24%      0.15s  4.46%  ….service/net/websocket.(*benchSocket).Read
     0.04s  1.19% 96.43%      0.25s  7.44%  bufio.(*Reader).Read
     0.02s   0.6% 97.02%      0.02s   0.6%  ….core/net.WSFrameHeaderValue.ValidateFromClient
     0.02s   0.6% 97.62%      3.33s 99.11%  ….service/net/websocket.(*Conn).Receive
     0.01s   0.3% 98.51%      3.11s 92.56%  ….service/net/websocket.(*Conn).readDataPayload
         0     0% 98.81%      0.18s  5.36%  ….service/net/websocket.(*Conn).nextHeader
```

And over `BenchmarkReceiveFragmented/256x`, the most header-heavy shape in the
suite — 256 frames for one message, i.e. the case deliberately chosen to make
this package's own per-frame work as large as it can get:

```
Duration: 3.57s, Total samples = 3.57s (100%)
      flat  flat%   sum%        cum   cum%
     2.22s 62.18% 62.18%      2.22s 62.18%  github.com/kitsunium/sdk/internal/core/net.ApplyWSMask (inline)
     0.23s  6.44% 68.63%      0.37s 10.36%  github.com/kitsunium/sdk/internal/core/net.ParseWSFrameHeader
     0.19s  5.32% 73.95%      0.19s  5.32%  runtime.memmove
     0.17s  4.76% 78.71%      3.57s   100%  ….service/net/websocket.(*Conn).Receive
     0.15s  4.20% 82.91%      0.74s 20.73%  ….service/net/websocket.(*Conn).nextHeader
     0.14s  3.92% 86.83%      2.60s 72.83%  ….service/net/websocket.(*Conn).readDataPayload
     0.11s  3.08% 89.92%      0.30s  8.40%  bufio.(*Reader).Read
```

The allocation profile (`-sample_index=alloc_space`, same run) attributes
exactly one entry to this package — `slices.Grow`, 532 kB across a million
iterations, i.e. the reassembly buffer growing once and never again. Everything
above it is `runtime/pprof` and `testing` itself.

**The conclusion is that there is nothing here to optimise.** Between 62 % and
89 % of a received message is one per-byte XOR in `internal/core/net`, and the
rest is `memmove`. This package's own flat time is 4.76 % + 4.20 % + 3.92 % =
12.9 % of the *worst* case and about 2 % of the ordinary one, spread across a
loop, a bounds check and an atomic increment. Two candidate changes were tested
anyway, on the principle that a refusal with a number attached is worth more
than a hunch; both were refused, in §6.

### The cost that dominates is in another package

`ApplyWSMask` lives in `internal/core/net/websocket_frame.go`, outside this
package. It is a byte-at-a-time XOR, and it is not optional: RFC 6455 §5.1
requires every client-to-server frame to be masked, so it runs over **every
inbound byte this server will ever see**.

Sized in a scratch module outside the repository, so that nothing here was
modified to measure it (3 runs each, `-benchtime=1s`):

| 4 KiB payload | ns/op | throughput |
|---|---|---|
| byte-at-a-time (today) | 3 013 | 1 359 MB/s |
| eight bytes at a time  |   552 | 7 417 MB/s |

| 64 KiB payload | ns/op | throughput |
|---|---|---|
| byte-at-a-time (today) | 48 385 | 1 354 MB/s |
| eight bytes at a time  |  8 644 | 7 582 MB/s |

**5.5×.** The isolated 3 013 ns at 4 KiB agrees to within 2 % with the 2.99 s of
3.36 s the in-situ profile attributes to `ApplyWSMask` on the same size, which
is what makes the number trustworthy rather than a microbenchmark artefact.
Applied, `Receive/binary/4096` would fall from 3 274 ns to roughly 810 ns — a
**4× speedup of the whole receive path**.

It is recorded and **not taken**: the function is not in this package, changing
it would touch a frozen contract layer's documented behaviour, and a per-byte
transform on a security-relevant path is not something to rewrite as a side
effect of a benchmarking pass. It belongs to whoever owns `internal/core/net`.

## 1. Refusing is cheaper than complying — by one to three orders of magnitude

Subtract `FirstFrame/baseline` (119.9 ns — the per-iteration connection state
and nothing read) from both sides, and the comparison is like for like:

| what the peer sent | total | reader cost | vs accepting it |
|---|---|---|---|
| `accept_binary/64`  |   600.2 ns |   480.3 ns | — |
| `accept_binary/4096`|  4 885 ns  | 4 765.1 ns | — |
| **every framing refusal** | **880.7 – 958.7 ns** | **760.8 – 838.8 ns** | 1.6–1.7× a 64 B accept, **16–18 % of a 4 KiB accept** |
| `interrupted_fragmentation` | 1 121 ns | 1 001.1 ns | reads *two* frames |
| `frame_ceiling` | 959.0 ns | 839.1 ns | **17.6 % of accepting a 4 KiB frame** |

`frame_ceiling` is the row that answers the actual attack. The script is a
fourteen-byte header announcing **2 MiB** and carrying no payload at all — the
cheapest thing a hostile peer can put on a socket. Refusing it costs 959 ns.
Accepting 1 MiB costs 784 955 ns, so accepting the 2 MiB it claimed would cost
roughly 1.6 ms: **refusing is 0.06 % of the work refused.** The bound is checked
against the length the peer *announced*, before a byte is read or a byte
allocated, and that is what makes the ratio look like this rather than like 1.

That property is not asserted here, it is **enforced by the benchmark itself**.
The `frame_ceiling` and `message_ceiling` scripts carry a header and no payload,
and `benchSocket` is set to report `io.EOF` rather than wrap when the script
runs out. A ceiling check moved to *after* the read would therefore find EOF and
report `WSConnClosed` (`0.2.11.32`), and `benchAssertOutcome` fails the row
instead of publishing a number for a path that is not the one named.

### The refusals are indistinguishable from each other, and that is the finding

| refusal | RFC section | total ns |
|---|---|---|
| `reserved_bit` | §5.2 (and the permessage-deflate refusal) | 880.7 |
| `reserved_opcode` | §5.2 | 881.5 |
| `unmasked_client_frame` | §5.1 | 896.4 |
| `fragmented_control_frame` | §5.5 | 900.1 |
| `non_minimal_length_64` | §5.2 | 901.2 |
| `non_minimal_length_16` | §5.2 | 906.8 |
| `orphan_continuation` | §5.4 | 931.6 |
| `oversized_control_frame` | §5.5 | 958.7 |
| `frame_ceiling` | ADR 0047 ceiling | 959.0 |

Nine refusals, detected at nine different depths — the first inside
`ParseWSFrameHeader`'s opening bit test, the last after the whole header has
been parsed and validated — spanning **8.9 %** end to end, against per-row
spreads of 0.9–2.3 %. Where the check sits is not what the number is made of.
What the number is made of is the two things every refusal shares: building the
typed `errs` error (one allocation more than an acceptance, 848 B against 640 B)
and putting the §7.1.7 courtesy Close frame on the wire.

Which means the Close frame — the thing that turns "the server hung up" into a
diagnosable event on the other side — is most of the price of conformance here,
and it costs a few hundred nanoseconds. There is no refusal worth making
cheaper, and no ordering of the checks worth rearranging for speed.

### Against what it costs the attacker to arrive

`LoopbackUpgrade` opens a TCP connection over loopback, completes the RFC 6455
§4.2 handshake including the SHA-1 accept digest, and closes: **262 653 ns.**

A refusal is **0.37 %** of that, on loopback, which is the most favourable
network an attacker will ever have. Framing refusals are not a lever.

## 2. Text costs 0.037 ns per byte, and it is the same 0.037 in both directions

| size | binary | text | delta | per byte |
|---|---|---|---|---|
| 64 B    |    198.5 ns |    240.5 ns |     +42.0 ns | (fixed call cost) |
| 4 KiB   |  3 274 ns   |  3 426 ns   |    +152 ns   | 0.0371 ns/B |
| 64 KiB  | 48 528 ns   | 50 989 ns   |  +2 461 ns   | 0.0376 ns/B |
| 1 MiB   |784 955 ns   |823 286 ns   | +38 331 ns   | 0.0366 ns/B |

`ValidateWSText` runs once over the **reassembled** message — ADR 0047's
correctness rule, because a rune may straddle a fragment — and costs about
27 GB/s, which is `utf8.Valid`'s ASCII fast path. At 64 bytes the delta is a
call, not a scan.

The send side agrees: `Send/text` minus `Send/binary` is 0.0407 ns/B at 4 KiB
and 0.0325 ns/B at 64 KiB. It has to agree — it is the same function, called on
the way out so this endpoint never emits what it would refuse to receive — and
the fact that two independently-built families land on the same per-byte figure
is the strongest internal check in this report.

**Text is 4.6 % of a 4 KiB message.** The UTF-8 rule is not a cost centre.

## 3. Steady-state Receive and Send allocate nothing, and now something proves it

Every row in `BenchmarkReceive`, `BenchmarkReceiveFragmented`,
`BenchmarkReceiveControl` and `BenchmarkSend` reports **0 B/op, 0 allocs/op**.
The reassembly buffer is grown once and reused for the connection's life (which
is why `Receive`'s result aliases it and is valid only until the next call), and
the write buffer likewise.

Both claims were made in prose in three places and guarded by nothing. They now
have `TestSteadyStateReceiveAllocatesNothing` and
`TestSteadyStateSendAllocatesNothing`, `//go:build !race` — the race detector
allocates shadow state per access, so `AllocsPerRun` under `-race` measures the
detector — with
`//internal/service/net/websocket:websocket_test` added to
`tools/alloc-lane-targets.txt` in the same change, because that lane is then
their only gate (rule 12).

**Both were mutation-checked, and the observed failures are in their doc
comments.** Changing `trackFragment`'s `c.msg = c.msg[:0]` to `c.msg = nil`
leaves every other test in the suite green — the messages still arrive intact —
and produces `steady-state Receive allocated 1.0 times per message, want 0`.
Changing `writeLocked`'s `AppendWSFrame(c.wbuf[:0], …)` to
`AppendWSFrame(nil, …)` produces `steady-state Send allocated 2.0 times per
message, want 0`. Two, not one, because appending into a nil slice allocates for
the header byte and reallocates for the payload — which is why the doc comment
carries the number the test printed rather than the number that was predicted.

The first-frame rows are the other half of the picture and do not contradict it:
`accept_binary/4096` costs 4 885 ns against the steady state's 3 274 ns, and
4 672 B against 640 B. The 1 611 ns and 4 032 B difference is the reassembly
buffer being allocated for the first time. A connection pays that once.

## 4. An extra frame costs ~115 ns — and the row that says 270 ns is `bufio`

One 64 KiB message, varying only how many frames carry it. Fragmentation is the
sender's private choice, so a server cannot refuse it; this is the axis a peer
can turn for free. **Nine runs**, because five did not resolve it.

| frames | fragment size | ns/op | spread |
|---|---|---|---|
| 1   | 64 KiB | 48 957 | 4.3 % |
| 2   | 32 KiB | 48 729 | 1.6 % |
| 8   |  8 KiB | 50 376 | 2.4 % |
| 16  |  4 KiB | 52 504 | 2.8 % |
| 32  |  2 KiB | 54 396 | 6.8 % |
| 256 |   256 B | 79 899 | 4.3 % |

Marginal cost is taken over the widest span inside each regime rather than
between adjacent rows, because a two-row delta divided by six frames inherits
the whole spread of both rows:

| span | frames added | ns per extra frame | spread carried |
|---|---|---|---|
| 2 → 16   |  14 | **269.6** | ± ~105 ns/frame |
| 32 → 256 | 224 | **113.9** | ± ~15 ns/frame |

The two are separable — 269.6 ± 105 does not reach 113.9 ± 15 — where the
adjacent-row deltas on their own are not: 8 → 16 gives 266.0 ± 184, which
proves nothing by itself.

**This row-set was nearly thrown away.** With only `{1, 2, 16, 256}` measured at
five runs, the marginal cost came out at 171, 273 and 121 ns per frame — three
answers to one question, with 16× sitting 4.5 % above the line the other three
defined. A table whose rows contradict arithmetically is noise until the cause
is named, so the cause was looked for instead of the median being published.

Adding 8× and 32× shows it is not noise: there are two regimes, and the break is
between a 4 KiB fragment and a 2 KiB one — exactly the size of the
`bufio.Reader` the hijack hands over (4096 bytes, `net/http`'s default).

That was then **tested rather than assumed**. Re-running the same family against
a connection built with `bufio.NewReaderSize(socket, 8192)`, the break moves
with the buffer: the expensive regime becomes fragments of 8 KiB and larger
(2→8 costs 424 ns per frame), and 4 KiB fragments join the cheap side (8→16
costs 123 ns per frame). The regime boundary tracks the buffer size, so the
effect is `bufio.Read`'s direct-read path — when a read is at least as large as
the buffer and the buffer is empty, `bufio` reads straight into the destination
instead of amortising a fill — and not this package's per-frame work.

So the honest figure for what an extra frame costs the reader is the one from
the regime where `bufio` is behaving normally: **≈115 ns per additional frame**.
At 256 fragments that is 255 × 115 = 29 325 ns, i.e. **37 % of the 79 899 ns
row** — fragmentation is the one axis on this page where a peer can make the
reader's own work dominate, and it takes a 256-byte fragment size to do it. The
270 ns figure is a property of how the underlying reader chunks its returns, and
is reported here rather than averaged into a single misleading mean.

The consistency check that validates the whole harness lives in this family:
`ReceiveFragmented/1x` (48 957 ns) and `Receive/binary/65536` (48 528 ns) are
the same script assembled by two different code paths in the benchmark file, and
they agree to 0.9 % — inside both rows' spread.

## 5. A control frame between two fragments costs 210 ns, of which 140 is the answer

The message is 256 bytes in two fragments and only the frame between them
changes, so the delta is exactly one served control frame.

| between the fragments | ns/op | delta |
|---|---|---|
| nothing | 516.5 | — |
| `Pong`, empty | 587.2 | **+70.7** |
| `Ping`, empty | 726.7 | **+210.2** |
| `Ping`, 125 B | 861.5 | **+345.0** |

A `Pong` costs 70.7 ns: parse the header, count the frame, return. A `Ping`
costs 210.2 ns, so **§5.5.2's obligation to answer is 139.5 ns** — the write
lock, the frame encode, the deadline refresh and the socket write. Filling the
Ping to its §5.5 maximum of 125 bytes adds 134.8 ns for reading and unmasking
125 bytes inbound and echoing them back out, which is the same application data
the RFC requires the Pong to carry.

**The message this benchmark carries is deliberately unrealistic at 256 bytes.**
The first version carried 32 KiB, which put the entire quantity being measured
at one per cent of the row: `ping_empty` came out 293 ns above `none` while
`none`'s own five runs spanned 533 ns. That row-set was discarded — the number
was noise with a decimal point — and the message shrunk until the answer was
legible. A control frame costs what it costs regardless of the message it
interrupts.

## 6. Two optimisations tested and refused

### `bufio.Peek`/`Discard` in `nextHeader`, instead of two `io.ReadFull` copies

`nextHeader` reads two bytes, learns the header's full length, reads the rest,
and parses out of the connection's `hdr` scratch. Peeking twice and discarding
avoids copying ≤14 bytes per frame. Five runs each:

| row | before | after | change |
|---|---|---|---|
| `Receive/binary/64`    |    186.0 ns |    188.0 ns | **+1.1 % slower** |
| `Receive/binary/4096`  |  3 330 ns   |  3 365 ns   | **+1.1 % slower** |
| `ReceiveFragmented/256x` | 76 446 ns | 74 616 ns   | −2.4 % faster |

**Refused.** It buys 2.4 % — about 7 ns per frame — only on a message split into
256 fragments of 256 bytes, a shape no client produces, and it is neutral to
slightly negative on every realistic one. Against that it costs a robustness
regression on a security-relevant read path: `bufio.Reader.Peek(n)` returns
`ErrBufferFull` when `n` exceeds the buffer, where `io.ReadFull` cannot. A
14-byte header is safe only because `bufio`'s minimum buffer is 16 bytes — a
stdlib constant this package would silently start depending on to read a frame
header at all. Seven nanoseconds is not worth that.

### Skipping the per-frame `rx.Add(1)` when the heartbeat is disabled

`Receive` increments an atomic per frame so the heartbeat can tell a silent peer
from a dead one. Deleting it entirely, five runs each:

| row | with the atomic | without | change |
|---|---|---|---|
| `Receive/binary/64`      |    186.0 ns |    193.2 ns | +3.9 % *slower* |
| `Receive/binary/4096`    |  3 330 ns   |  3 250 ns   | −2.4 % |
| `ReceiveFragmented/256x` | 76 446 ns   | 77 799 ns   | +1.8 % *slower* |

**Refused, and the numbers are why.** Two of three rows got *slower* when work
was removed, including the 256-fragment row that performs 256 of these
increments per operation. Removing code cannot slow it down except through code
layout, so the honest reading is that an uncontended `LOCK XADD` does not
register against the memory traffic already in flight — even at 256 per
operation. There is nothing to gate.

Had it registered, it would still have been refused: the counter feeds the
connection's **only** liveness check, the thing that turns a peer which vanished
without closing into an ending rather than a parked goroutine. And gating it on
`noPing` would have been self-serving, because every benchmark in this file sets
`WithoutPing()` — the optimisation would have shown up in every row here and in
no production connection.

### Nothing was refused on security grounds

Worth stating explicitly, since it is the failure mode this package exists to
avoid: no candidate was considered that would have made a refusal conditional,
moved a check off the read path, or checked a bound after the allocation it
guards. The frame ceiling is checked against the announced length before any
read, UTF-8 is judged on the reassembled message, and every MUST-fail is
unconditional — and §1 says those choices cost nothing worth reclaiming.

## 7. What the kernel costs, and what this package is a fraction of

| | in-memory | loopback TCP | this package's share |
|---|---|---|---|
| 64 B echo   | 322.5 ns (198.5 receive + 124.0 send) | 16 698 ns | **1.9 %** |
| 4 KiB echo  | 3 506.4 ns (3 274 + 232.4) | 27 012 ns | **13.0 %** |
| upgrade | — | 262 653 ns | — |

The loopback rows go through the real `Upgrade`, a real hijack and a real
socket. At 64 bytes the frame work is **one fiftieth** of a round trip; the rest
is two context switches, two socket writes and two reads. At 4 KiB it reaches
13 %, because the per-byte work finally starts to matter and the syscall count
does not change.

The practical consequence, stated because it is what gets sized wrong: **on
small messages this package is not where the time goes, and on large ones the
time is one XOR in `internal/core/net`.** Neither is fixed by changing anything
in `internal/service/net/websocket`. A connection serving small messages is
bounded by syscalls — batching at the application level is the lever — and one
serving large ones is bounded by the masking pass, whose 5.5× is quantified
above and belongs to the contract layer.

`ConnLifecycle` (2 307 ns, 9 allocations) is the last piece: constructing a
`Conn`, starting the drain watcher goroutine, terminating and joining it. Add it
to any `FirstFrame` row for the cost of a connection that serves exactly one
message and ends. It is 0.9 % of the 262 653 ns handshake that precedes it.

## Reference

- ADR 0047 — `docs/adr/0047-sdk-net-websocket.md`
- The wire format these numbers run over — `internal/core/net/BENCH.md`
- The engine underneath the loopback rows — `internal/service/net/server/BENCH.md`
- SDK-wide rule 9 (every benchmark package ships its numbers) and rule 12
  (every `!race` test names its lane) — root `CLAUDE.md`
