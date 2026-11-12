<!-- generated from internal/service/net/server/*_bench_test.go — run `cd internal/service && GOWORK=off go test -run='^$' -bench=. -benchmem -benchtime=3000x -count=5 ./net/server/` to refresh -->
# Benchmarks — `internal/service/net/server`

ADR 0029 §D9 commits this domain to *proving* its performance claims rather than
asserting them, and to reporting a measurement that contradicts the design.
These numbers discharge that commitment. Three of the four results are "no
measurable difference" — including one optimisation that buys nothing at all
here — and the fourth is smaller than the ADR implied. All four are stated
plainly below.

Every comparison runs the **identical handler body** on both sides, so the delta
is the domain's overhead and nothing else.

## Reproducibility envelope

> **Numbers vary across machines.** This report stamps the box that produced
> them so cross-machine deltas can be evaluated honestly.

| Dimension | Value |
|---|---|
| CPU | 12th Gen Intel(R) Core(TM) i7-1255U |
| CPU cores | 12 |
| RAM | 15 GiB |
| OS / kernel | Linux 6.12.107+deb13-amd64 |
| Architecture | amd64 |
| Go toolchain | go1.27.0 linux/amd64 |
| Git branch | feat/server-domain |
| Git commit | 841988a |
| Generated (UTC) | 2026-09-03 |
| Bench wall-clock | `-benchtime=3000x` (stream/HTTP/accept), `-benchtime=2000x` (datagram), `-count=5` |

Five runs are reported per benchmark because a single run on a loopback socket
is dominated by scheduler noise — the first three-run attempt showed the HTTP
adapter 27 % slower than native, which five runs revealed to be noise. Medians
are used below; the full spread is given so the reader can judge it.

## Results

### 1. Stream server vs a bare `net.Listener` loop

One full accept → serve → close cycle, echoing a single byte.

| Benchmark | ns/op (5 runs) | median | B/op | allocs/op |
|---|---|---|---|---|
| `ServeConn_SDK` | 82391, 87866, 89760, 95207, 105569 | **89 760** | 1060 | **25** |
| `ServeConn_BareListener` | 88752, 89581, 90791, 98186, 98968 | **90 791** | 1049 | 28 |

**The domain's surcharge is not measurable.** The medians differ by ~1 %, well
inside a spread of ±12 % on either side. The SDK path does *fewer* allocations
(25 vs 28) because it recycles the per-connection wrapper, where the hand-written
loop allocates a closure per connection — the pooling in `pool.go` pays for the
bookkeeping it adds.

What the SDK number includes that the baseline does not: the pooled wrapper,
middleware-chain resolution, per-phase deadline application, the live-socket
registry, the connection counters and the drain bookkeeping.

### 2. HTTP adapter vs stock `net/http`

One keep-alive `GET` against an identical handler.

| Benchmark | ns/op (5 runs) | median | B/op | allocs/op |
|---|---|---|---|---|
| `HTTP_Adapter` | 61169, 61527, 66054, 67487, 67849 | **66 054** | 5133 | 61 |
| `HTTP_Native` | 56904, 63075, 66913, 69623, 70529 | **66 913** | 5130 | 61 |

**Indistinguishable, with byte-for-byte identical allocation counts.** This is
the result ADR 0029 §D3 predicted and the reason it holds: the bridge hands over
a **connection**, not a request, so its cost amortises across every request on
that connection. `TestHTTPAdapterKeepsAliveAcrossRequests` pins the property
this measurement depends on — three keep-alive requests count as one accept.

### 3. Batched datagram read vs one `ReadFrom` per datagram

Reading 16 datagrams (512 B each) already queued on the socket.

| Benchmark | ns/op (5 runs) | median | per datagram | B/op | allocs/op |
|---|---|---|---|---|---|
| `DatagramRead_Batched` (`recvmmsg`) | 5878, 6036, 6115, 6762, 7566 | **6 115** | ~382 ns | 881 | 34 |
| `DatagramRead_Portable` (`ReadFrom`) | 7162, 7222, 7680, 7834, 7945 | **7 680** | ~480 ns | 832 | 32 |

**Batching wins, by ~20 % — real, but smaller than ADR 0029 implied.** The ADR
called batched reading "the major gain in datagram"; on this machine it is a
fifth, not a multiple. The honest reading:

- The win is a genuine syscall-count reduction: one `recvmmsg` replaces sixteen
  `recvfrom` calls. On a loopback socket the per-syscall cost is small, so the
  saving is proportionally modest. It should grow on a busier NIC and under
  higher queue depth, but **this report only claims what it measured.**
- The batched path allocates **two more** per iteration (34 vs 32). That is
  `sockaddr_linux.go` building a `net.UDPAddr` per datagram, which `ReadFrom`
  also does — the extra pair is the batched path's own bookkeeping. It is a
  candidate for future work, not a regression.
- The benchmark brackets the queue-filling with `StopTimer`/`StartTimer`, whose
  overhead compresses the measured difference. The real-world gap is therefore
  at least this large, not smaller.

### 4. `SO_REUSEPORT` sharded accept vs a single listener

One accept → serve → close cycle under `RunParallel`, so the clients actually
contend for the accept path. The only difference between the two is the shard
count.

| Benchmark | ns/op (5 runs) | median | B/op | allocs/op |
|---|---|---|---|---|
| `Accept_SingleListener` | 9358, 9553, 9621, 9744, 14705 | **9 621** | 1059 | 25 |
| `Accept_Sharded` (one listener per core) | 9396, 9397, 9474, 9563, 9569 | **9 474** | 1059 | 25 |

**No measurable gain on this machine, and ADR 0029 §D9 requires saying so.**
The medians differ by ~1.5 %, inside the noise — and the single-listener column
contains a 14 705 ns warm-up outlier that is itself larger than the entire
claimed difference.

This is a **real null result, not a broken comparison.** Sharding is genuinely
active during the run: `TestShardedListenerReportsItsShardCount` asserts that a
request for four shards is honoured and reported as four, and
`TestShardedListenerStillServes` proves the four listeners actually serve.
Without those, "no gain" could simply have meant the shards silently collapsed
to one and the benchmark was comparing a server against itself.

The honest interpretation: `SO_REUSEPORT` removes contention on a *single accept
queue*, and on a 12-core laptop driving loopback connections there is not enough
accept pressure for that contention to be the bottleneck. The mechanism is
sound — the kernel-level spike bound two listeners to one port, and the option
is genuinely set before bind — but **on this hardware it buys nothing, and the
default should not be assumed to be free elsewhere either.** It stays available
and off-by-default-sized (`Shards(0)` = one per core) rather than being removed,
because the regime it targets (a saturated NIC with many short-lived
connections) is one this laptop cannot produce.

## The merge gate's loads (L1, L2, L3)

`BenchmarkLoadL1`, `BenchmarkLoadL2` and `BenchmarkLoadL3` are the loads the
network plan's merge gate measures (plan P8; `.github/workflows/net-kernel-gate.yml`,
`tools/netgate`). Their numbers are not kept in this file: they are measured on
every pull request that touches the engines, ten interleaved rounds on one
GitHub-hosted runner, and the samples and the report are the run's
`net-bench-<sha>` artefact, tied to the commit measured. The loads are fixed in
the code — L1: a plaintext GET, 1 KiB answered, 1000 kept-alive connections;
L2: L1 with a Bearer token and routing; L3: 1200-byte UDP datagrams, a GSO
sender of 54 segments, 4 flows at 200 000 datagrams a second, over a veth pair
— and the metric of L1 and L2 is the server's CPU, measured in its own process,
so the client's cost never dilutes a ratio. Each pair of engines is measured
side by side, one child serving both in windows A, B, B, A, … (ADR 0190), and
a round is one sample of the ratio B/A. L3's CPU is the receiver's rusage
plus the sender's plus the irq and softirq time of `/proc/stat`, every window
of either engine metered by the same function (ADR 0192 §D6): what GRO saves —
each GSO send segmented again on receive — runs in softirq, charged mostly to
whatever task the CPU was running, and the receiver's rusage alone, dominated
by the copy-out both engines pay, missed it. Each part is reported beside as
`rcv-`, `snd-` and `irq-cpu-ns/datagram`. `kernel-drops/op` is the socket's own
SO_RXQ_OVFL count over the datagrams sent, beside `lost/op` (#973): the two agree
on engine A, and under GRO the kernel counts a coalesced train as one drop, so
the count stands below the loss; a round where it passes the loss is refused. P4a's gate and the calibration rows
are required; every other waits for the step that builds its engine B
(`netgate -list`).

One measurement, under Docker on a Mac (Linux 7.0 arm64, 8 CPUs, epoll since
Docker's profile refuses io_uring), one round at `-benchtime=100000x`, given
only as the order of magnitude the gate starts from:

| Benchmark | cpu-ns/req or /datagram |
|---|---|
| `LoadL1/engine=goroutine` | 16 419 |
| `LoadL1/engine=loop` | 4 282 |
| `LoadL2/engine=handwritten` | 6 403 |
| `LoadL3/engine=recvmmsg` (loopback, 55.7 datagrams a receive, receiver's rusage) | 621 |

L1h2 (#963, `load_gate_h2_linux_test.go`) is L1's request over h2c by prior
knowledge, 1 000 connections with eight streams in flight each, the client
writing pre-encoded frames: `BenchmarkLoadL1h2/pair=goroutine-h2:loop-h2` (P9),
`pair=loop-h2:loop-h2` (cal-L1h2) and `pair=loop:loop-h2` (h1-h2, reported). No
number is recorded here yet: its rows are the runner's (net-kernel-gate), and
all three stay inactive until cal-L1h2 has held within 1.05 on three runs.

P4a's pair on the veth pair, the same Docker (Linux 7.0.14), the container
held to 4 CPUs, ten paired rounds at `-benchtime=400000x`: medians of each
engine, then netgate's bound of the rounds' ratios B/A.

| Metric | `recvmmsg` (A) | `gro` (B) | bound | gate |
|---|---|---|---|---|
| `cpu-ns/datagram` | 5 400 | 3 021 | 0.623 (upper), median 0.552 | ≤ 0.75 — pass |
| of which `rcv-` / `snd-` / `irq-` | 896 / 1 925 / 2 557 | 512 / 1 723 / 761 | | reported |
| `datagrams/syscall` | 58.3 | 230 | 3.76 (lower), median 3.95 | ≥ 4, reported, not gated |
| `cal-L3` (`recvmmsg:recvmmsg`) `cpu-ns/datagram` | | | 0.968 to 1.08, median 1.04 | within 0.8 to 1.25 — pass |

On the pull request's GitHub-hosted runner (Linux 6.17, Azure, 4 CPUs, its
kernel without `CONFIG_IRQ_TIME_ACCOUNTING`, so the irq part is tick-sampled
and counted twice where a tick lands in the receiver or the sender, ADR 0192
§D6): `cpu-ns/datagram` 3 027 → 803 ns, upper bound 0.273 — pass; of which
`rcv-` 1 237 → 221, `snd-` 1 105 → 505, `irq-` 686 → 75. The two rusages
alone give about 0.31, so the double count does not decide the verdict.
These numbers, and the Docker ones above, were divided by the datagrams
received, before the CPU was divided by those sent (§D6); both runs lost
under 1 % of them, within the rounds' spread.

The irq part is where GRO saves most, as §D6 expected: 2 557 → 761 ns a
datagram. `datagrams/syscall` is reported, not gated (ADR 0192 §D7): A already
reads a GSO send's 54 datagrams a receive, so B's count is how many sends
queue before its reader wakes, the runner's scheduling.

Each engine is sent `b.N` datagrams a round, 400 000 at the job's
`-benchtime`, not half of it as on L1 and L2: `/proc/stat` counts in `USER_HZ`
ticks, 100 a second on every Linux ABI, and at 100 000 datagrams a tick was
100 ns a datagram — 2.5 % of A —, which alone pushed the bound past the
threshold; at 400 000 it is 25 ns. A round of both L3 pairs takes about 9 s,
ten rounds 1 min 30 s, inside the bench job's 30 minutes.

## What is NOT measured here

Nothing outstanding: every optimisation ADR 0029 §D5 lists now has a number
above, including the one that turned out not to help.

## How to read a regression here

- A jump in `ServeConn_SDK` **allocs/op** above 25 means the per-connection
  wrapper or its scratch buffer stopped being recycled — check `pool.go`.
- `HTTP_Adapter` diverging from `HTTP_Native` in **allocs/op** means the bridge
  started costing per request rather than per connection — check that
  `TestHTTPAdapterKeepsAliveAcrossRequests` still reports one accept.
- `DatagramRead_Batched` converging on `DatagramRead_Portable` means
  `newDatagramSource` silently fell back — `TestLinuxSelectsTheBatchedReader` is
  the guard that fails first.
- `Accept_Sharded` is only meaningful while `TestShardedListenerReportsItsShardCount`
  passes. If sharding collapses to one listener, this benchmark measures nothing
  and will happily report parity.
- `AcquireRelease_ReadBuffer16K` above 0 allocs/op means the scratch buffer
  stopped coming from `kernel/concur/buffer`'s 16 KiB class — check that
  `acquire` still calls `buffer.GetSize(size)`.

## Edition 2026-11-10 — the loop's counters (#975)

`BenchmarkCore_batch` (`internal/service/net/loop`), the commit before the ring
gained its counters (`fa2778cc7^`) against this one, one box (AMD EPYC 7351P,
4 vCPU, go1.27.3, `-benchtime=200000x -count=8`, benchstat):

| | ns/op (base) | ns/op (head) | B/op | allocs/op |
|---|---|---|---|---|
| `Core_batch/open=1` | 343.2 | 269.5 | 28 | 1 |
| `Core_batch/open=10000` | 342.7 | 243.8 (−28.84 %, p = 0.000, n = 8) | 28 | 1 |

The counters themselves are plain increments, on the loop's thread, of fields it
already owns: the head is not slower, and the difference is the rest of the
wave, not the counters. The portable backend's counters are atomics of its
lanes (`loop_portable.go`), filled the same way: `Bytes*`, `OutputRefused`,
`SpillClosed`, `Timeouts`, `IdleClosed` and `PendingOutput` in
`TestPortable_*`; `Slab*` stay zero there, its buffers being the Go heap's.

## Edition 2026-11-10 — response compression (#853)

`BenchmarkCompress` (`compress_external_test.go`) is one request served by
`Compress` over a recording writer, 64 KiB of `text/plain`, the box of the
edition below, `-benchtime=2000x`:

| Benchmark | ns/op | MB/s | B/op | allocs/op |
|---|---|---|---|---|
| `Compress/gzip` | 6 722 293 | 9.75 | 248 948 | 14 |
| `Compress/declined` (no `Accept-Encoding`) | 4 183 | 15 667 | 1 296 | 8 |

A declined request pays one header read and `net/http`'s own costs, which are
what `declined` holds. The http1 chain has no `Compress` link: its P2 gate (ADR
0186 §D10) would have to hold 1.05 cpu-ns/req on an uncompressible body, and no
bench has shown that yet.

## Edition 2026-11-10 — datagram sockets by shard (#977)

`BenchmarkLoadL3/pair=recvmmsg:recvmmsg+shards` is the paired ratio of one
socket against four SO_REUSEPORT sockets of one port, each with its own reader.
`TestLoadL3_wired/recvmmsg+shards` proves the wiring on loopback: the pair itself
runs where L3 runs, the veth lane (root, `scripts/ci/net-veth.sh`), so its numbers
are the CI's to record here, and `Shards(0)` stays the opt-in it is until the
1.8× saturation gate holds there.

## Edition 2026-11-11 — datagrams on io_uring's multishot RECVMSG (#852)

`BenchmarkLoadL3/pair=recvmmsg:uring-recvmsg` pairs the recvmmsg reader, GRO
off, with the io_uring datagram lane of `PacketGroup.HandleLoop`, GRO on
(`load_l3_uring_linux_internal_test.go`): its `datagrams/syscall` are datagrams
a batch, one `io_uring_enter` each. P4a's threshold, `cpu-ns/datagram` ≤ 0.75,
is reported first and gated once it holds, as #852 asks. It runs where L3 runs
(root and `scripts/ci/net-veth.sh`): no number is recorded here from a machine
without that lane. `TestLoadL3_wiredURing` reads one GSO send through the lane
over loopback and pins that it counts every datagram and cuts none.

## Edition 2026-11-10 — a PROXY protocol header (#841)

`BenchmarkProxyHeader` (`proxy_bench_test.go`): one v2 header read and parsed
from a stream, the connection's single cost on this path; no hot-path gate.
Measured on the box of the edition below, `-benchtime=20000x`: about 1 µs,
0 B/op, 0 allocs/op with the announced address held in the connection. A v1
line allocates for its fields, three or four strings, and is a balancer's legacy.

## Edition 2026-11-10 — a datagram's own destination (#972)

`BenchmarkLoadL3/pair=recvmmsg:recvmmsg+pktinfo` reports the cost of one control
message more per message, over the veth lane's paired rounds; it is not gated.
It runs where L3 runs (root and `scripts/ci/net-veth.sh`), so no number is
recorded here from a machine without that lane. `TestLoadL3_wired` reads the same
reader over loopback and pins that the widened control room cuts nothing.

## Edition 2026-11-10 — a handler's `io.Copy` reaches the socket's own copy (#970)

`BenchmarkCopyFile` (`copy_bench_test.go`) is `io.Copy(c, f)` of a 64 MiB file to
a loopback peer through a handler on a plaintext TCP group; `BenchmarkEcho` is
`io.Copy(c, c)` over a 1 MiB round. `before_generic` hides the copies behind a
plain `net.Conn`, which is what a handler got before this edition; every count
includes opening the file, the rest being the standard library's `SyscallConn`.
One box (AMD EPYC 7351P, 4 vCPU, go1.27.3, `-benchmem -benchtime=20x -count=3`):

| Benchmark | MB/s | B/op | allocs/op |
|---|---|---|---|
| `CopyFile/after_ReadFrom` (sendfile) | 3 808–4 583 | 272–319 | 7 |
| `CopyFile/before_generic` (32 KiB loop) | 1 133–1 236 | 32 957–32 980 | 4 |
| `Echo/after_splice` | 1 343–1 720 | 200–377 | 4 |
| `Echo/before_generic` | 1 005–1 183 | 200–250 | 4 |

The copy's own 32 KiB buffer is gone; what remains is the file's open and
`sendfile`'s standard-library set-up, a constant of the call, not of the bytes.
A group with a `WriteTimeout` keeps its per-write bound and the before column.

## Edition 2026-11-03 — the read buffer from its size class (#810)

`BenchmarkAcquireRelease_ReadBuffer16K` (`pool_bench_test.go`): a
connection's wrapper taken and given back for a group whose `ReadBufferSize`
is 16 KiB. One box, before and after (AMD EPYC 7351P, 8 vCPU, go1.27.3,
`GOMAXPROCS=4`, `-benchmem -count 4`):

| | ns/op | B/op | allocs/op |
|---|---|---|---|
| Before (`buffer.Get`, then its own 16 KiB when the 1 KiB one was short) | 6 422–6 971 | 17 487 | 4 |
| After (`buffer.GetSize(16 KiB)`, the 16 KiB class) | 158.7–164.6 | 0 | 0 |

#810's "acquire with ReadBufferSize = 16 KiB at 0 allocs/op in steady state"
is met: the class keeps the buffer the release returns, and no 1 KiB consumer
— the logger — is handed it in between.
