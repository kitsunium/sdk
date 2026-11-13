<!-- generated from internal/service/net/loop/*_bench_test.go — run `cd internal && GOWORK=off go test -run='^$' -bench=. -benchmem -count=6 -benchtime=300ms ./service/net/loop/` to refresh; not `make bench`, which regenerates the codec's report only. Each figure below is the median of the six runs, computed from that output by a script, never typed -->
# Benchmarks — `internal/service/net/loop`

The loop engine's own cost: what one batch pays, what a `Post` from outside
costs to reach its loop, the timer wheel, the slab and `LoopIO.Write` — on a
fake driver, so every platform runs them — and the io_uring backend's rows,
which need the kernel and skip where `io_uring` is absent. The report exists
because ADR 0189 §D1 makes every class B promotion depend on "its package's
BENCH.md", and ADR 0165 asks a number to come with its envelope. It is hand-run,
as `net/http1`'s is: `scripts/pre-commit/check-bench-md.sh` finds a benchmarked
package by its `*_bench_test.go` file names, so the loop's benchmarks live in
files of that name (#981), and the check fails until this report is beside them.

## Reproducibility envelope

> **Numbers vary across machines.** These were taken on a virtual machine
> (`GOMAXPROCS=4` of 8 logical CPUs, load average 1.5 when the run ended). The rows measured in the same process, and the pairs a
> row has (`bundle=true`/`false`, `mode=inc`/`plain`, `rings=1`/`3`), are the
> trustworthy part; an absolute ns/op is this host's.

| Dimension | Value |
|---|---|
| CPU                | AMD EPYC 7351P 16-Core Processor (a VM, 8 logical CPUs) |
| OS / kernel        | Linux 6.12.101+deb13-amd64 |
| Architecture       | amd64 |
| Go toolchain       | go1.27.3 linux/amd64 |
| Backends           | io_uring: every `URing*` row, direct descriptors; epoll: no benchmark drives it — the `Core_*`, `Post`, `Wheel`, `Slab` and `Write` rows run on a fake driver and are the same on both backends |
| Git branch         | `wip/sdk-lot-2-l` |
| Git commit         | base `05fb22dd9` (the benchmark files of #981 uncommitted when measured) |
| Generated (UTC)    | 2026-11-10 |
| Bench wall-clock   | `-benchtime=300ms -count=6`, 125 s |
| Rows               | 30, none skipped on this kernel |

## Results

Median of six runs; the ± is half the spread between the fastest and the slowest
run, as a share of the median. `other` is the benchmark's own metric. The
`URingBatchBudget` rows run once per sample (a scenario of about 70 ms), their
`first`, `last` and `p50` figures being the echo latencies of one wave.

### The core, on every platform (a fake driver, no kernel)

| Benchmark | ns/op | B/op | allocs/op | other |
|---|---|---|---|---|
| `Core_batch/open=1` | 246.3 ± 2% | 30 | 1 |  |
| `Core_batch/open=10000` | 250.8 ± 2% | 31 | 1 |  |
| `Core_batchClock` | 116.2 ± 2% | 0 | 0 |  |
| `Post/wake=none` | 81.4 ± 0% | 0 | 0 |  |
| `Post/wake=eventfd` | 690.3 ± 1% | 0 | 0 |  |
| `Wheel/arm+disarm` | 18.2 ± 1% | 0 | 0 |  |
| `Wheel/tick/timers=10000` | 233.0 ± 3% | 0 | 0 |  |
| `Slab/alloc+free/2KiB` | 9.0 ± 3% | 0 | 0 |  |
| `Slab/alloc+free/16KiB` | 9.3 ± 2% | 0 | 0 |  |
| `Slab/alloc+free/64KiB` | 9.2 ± 2% | 0 | 0 |  |
| `Slab/alloc+free/1MiB` | 9.2 ± 3% | 0 | 0 |  |
| `Slab/growOut/2KiB-to-8KiB` | 62.6 ± 3% | 0 | 0 |  |
| `Write/bytes=240` | 34.9 ± 2% | 0 | 0 |  |
| `Write/bytes=8192` | 220.6 ± 3% | 0 | 0 |  |

### io_uring rows (the kernel is load-bearing; they skip where io_uring is absent)

| Benchmark | ns/op | B/op | allocs/op | other |
|---|---|---|---|---|
| `URingBatchBudget/budget=0s` | 72 460 218 ± 1% | 60928 | 1896 | 41.4 first-ms, 45.3 last-ms, 43.4 p50-ms |
| `URingBatchBudget/budget=1ms` | 71 271 184 ± 1% | 60946 | 1896 | 1.9 first-ms, 44.8 last-ms, 23.5 p50-ms |
| `URingBatchBudget/budget=2ms` | 71 743 470 ± 1% | 60868 | 1895 | 3.5 first-ms, 45.2 last-ms, 25.3 p50-ms |
| `URingBatchBudget/budget=5ms` | 71 297 992 ± 1% | 60963 | 1896 | 6.7 first-ms, 45.1 last-ms, 28.5 p50-ms |
| `URingRecvBundle/bytes=240/bundle=true` | 1 271 676 ± 5% | 0 | 0 | 19 870 ns/req |
| `URingRecvBundle/bytes=240/bundle=false` | 1 232 042 ± 5% | 0 | 0 | 19 250 ns/req |
| `URingRecvBundle/bytes=8192/bundle=true` | 1 682 247 ± 5% | 52 | 0 | 26 285 ns/req |
| `URingRecvBundle/bytes=8192/bundle=false` | 1 684 245 ± 7% | 53 | 0 | 26 316 ns/req |
| `URingRings_fill/rings=1` | 1 224 028 ± 4% | 0 | 0 | 19 126 ns/req |
| `URingRings_fill/rings=3` | 1 271 823 ± 6% | 0 | 0 | 19 872 ns/req |
| `URingPbufInc/bytes=16/mode=inc` | 1 223 417 ± 3% | 0 | 0 | 19 116 ns/req |
| `URingPbufInc/bytes=16/mode=plain` | 1 218 818 ± 5% | 0 | 0 | 19 044 ns/req |
| `URingPbufInc/bytes=240/mode=inc` | 1 192 860 ± 5% | 0 | 0 | 18 638 ns/req |
| `URingPbufInc/bytes=240/mode=plain` | 1 219 767 ± 10% | 0 | 0 | 19 059 ns/req |
| `URingWait_ready` | 10.1 ± 0% | 0 | 0 |  |
| `URingWatchdog_quiet` | 42.6 ± 1% | 0 | 0 |  |

Three things the rows state and nothing more:

- `Write` and `Post` allocate nothing, as the design's budget says
  (`allocs/op` 0 in every row of both). `Core_batch`'s one allocation a batch is
  the same one the base has (`allocs/op` 1 there too, measured against it for
  #975); `Core_batchClock` allocates none.
- A batch costs the slots it touched, not the slots it holds: `Core_batch` at
  `open=1` and at `open=10000` differ by under 2 %, inside their spreads.
- `Post/wake=eventfd` adds, to `wake=none`, the eventfd write of the wake-up and
  the loop's read that follows it, both on this one goroutine: the cost of the
  kernel's wake-up, not of a wake-up that crosses two threads.

## Class B rows (ADR 0189 §D1)

A class B capability joins class A-code only once this report shows the
threshold its own ADR sets, and `caps.go` repeats it in each row's reason. One
line each; "no row yet" means the report has no benchmark a promotion could read.

| Row | The benchmark its ADR will read |
|---|---|
| `uring-bundle` (send bundles) | no row yet; the receive bundle is class A-code's `uring-recv-bundle`, read in `URingRecvBundle` (ADR 0205) |
| `uring-zcrx` | no row yet (#980): `BenchmarkLoadL1/pair=loop:loop-zcrx`, cpu-ns/req and copied bytes a request, on a 6.15 host with a NIC whose queue splits headers, under `CAP_NET_ADMIN` — no lane of the SDK's has one (ADR 0189 amended 2026-11-11) |
| `tcp-fastopen` | no row yet (#857): declared; its gate is a repeat-client connect benchmark on a host whose `net.ipv4.tcp_fastopen` has bit 2 |
| `uring-min-timeout` | no row yet: ADR 0199 §D3 and ADR 0202 §D5 measured `min_wait_usec` on a 7.0 kernel, a measurement no benchmark of this package reproduces; `URingWait_ready` measures the wait when a completion is already there, not the minimum wait |

`uring-send-zc` left this list for class A-code (#969, ADR 0189 amended 2026-11-11): opted into through `LoopValue.Capabilities`, a flight of `SendZCMin` bytes or more sent by `IORING_OP_SEND_ZC`. Its paired row is `BenchmarkLoadL1big/pair=loop:loop-sendzc` (64 KiB bodies), reported by `tools/netgate`'s `send-zc` row; its numbers are in `internal/service/net/server/BENCH.md` (edition 2026-11-13), not in this file.

`reuseport-cbpf` left this list for class A-code (#977): the datagram side's `Steer` opts into it and server reports its row; a loop group refuses it (#857).

`loop-affinity`, `so-incoming-cpu` and `tcp-notsent-lowat` are class A-code (#857): they leave it once a host bench shows 10 % of cpu-ns/req (P6's paired loop:loop+affinity; the sparse keep-alive bench's context switches); no number for them is in this file.

`reuseport-loops` is class A-code, opted into by `Shards(0)` (#856): its gate before it may leave class A-code, a paired `BenchmarkConnectRate` of the loop against loop+shards (ratio ≤ 0.9, 50 000 connects a round, a row of `net/server`'s report, ADR 0190), has not been built: this file holds no number for it.

`busy-poll` left this list for class A-code (#979): its gate before it may leave class A-code — `BenchmarkSparseKeepAlive` paired loop against loop+busy-poll on the veth lane with GRO on, so its sockets carry NAPI ids, reading the request's p99 and cpu-ns/req (ADR 0189's amendment of 2026-11-11 sets the thresholds) — has not been built: loopback has no NAPI id, and this file holds no number for it.

`tcp-defer-accept` left this list for class A-code (#971): its row is a socket
option with no throughput threshold, and the conformance suite is its proof.

## Not in this report

- The loop under load against `net/http`'s engines and the C reference: that is
  `net/server`'s report and ADR 0190's paired gate, which read `pair=` lines, not
  this file.
- A kernel older or newer than the one above: every `URing*` row is this kernel's
  (`uname -r` above), and a row that needs a newer one skips. The CI's kernel lanes
  run the conformance suites, not these benchmarks.
- An epoll benchmark of the backend itself.
