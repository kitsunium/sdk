<!-- generated from internal/service/net/server/*_bench_test.go — run `cd internal/service && GOWORK=off go test -run='^$' -bench=. -benchmem -benchtime=3000x -count=5 ./net/server/` to refresh -->
# Benchmarks — `internal/service/net/server`

ADR 0029 §D9 commits this domain to *proving* its performance claims rather than
asserting them, and to reporting a measurement that contradicts the design.
These numbers discharge that commitment. Two of the three results are "no
measurable difference", and one optimisation is smaller than the ADR implied —
both are stated plainly below.

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
| Git commit | f9a0bb2 |
| Generated (UTC) | 2026-09-03 |
| Bench wall-clock | `-benchtime=3000x` (stream/HTTP), `-benchtime=2000x` (datagram), `-count=5` |

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

## What is NOT measured here

**`SO_REUSEPORT` sharded accept is not implemented yet**, so there is no number
for it. ADR 0029 §D5 lists it as an optimisation to implement *and* measure, and
§D9 promises that if it shows no gain on this machine, the report will say so.
Neither the implementation nor the measurement exists as of commit `f9a0bb2`;
the `Shards` limit is accepted and reported through `State` as a single shard.
This section will state the result — including a null one — when it lands.

The kernel-level spike that validated the mechanism did run green (two listeners
bound to one port), so the mechanism is known to work; what is missing is the
engine integration and the throughput comparison.

## How to read a regression here

- A jump in `ServeConn_SDK` **allocs/op** above 25 means the per-connection
  wrapper or its scratch buffer stopped being recycled — check `pool.go`.
- `HTTP_Adapter` diverging from `HTTP_Native` in **allocs/op** means the bridge
  started costing per request rather than per connection — check that
  `TestHTTPAdapterKeepsAliveAcrossRequests` still reports one accept.
- `DatagramRead_Batched` converging on `DatagramRead_Portable` means
  `newDatagramSource` silently fell back — `TestLinuxSelectsTheBatchedReader` is
  the guard that fails first.
