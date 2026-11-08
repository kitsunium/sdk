<!-- generated from internal/service/proc/ipc/rpc/rpc_bench_test.go and gen_perf_test.go — run `go test -run='^$' -bench=. -benchmem -benchtime=300ms -count=6 ./internal/service/proc/ipc/rpc/` to refresh -->
# Benchmarks — `internal/service/proc/ipc/rpc`

| | |
|---|---|
| Machine | AMD EPYC 7351P 16-Core Processor, 8 vCPU, 15 GiB |
| OS | Linux 6.12.101+deb13-amd64 |
| Go toolchain | go1.27.3 linux/amd64, `GOMAXPROCS=4` |
| Base commit | `4216ea6e` (+ the change measured below) |
| Measured | 2026-11-07, `-benchtime=300ms -count=6`, medians |

> **Numbers vary across machines.** Compare deltas measured on one machine,
> never figures from two.

| benchmark | ns/op | B/op | allocs/op |
|---|---:|---:|---:|
| `CallRoundTrip` (a 3-field record over a `net.Pipe`, json both ways, echoed by a handler) | 37 460 | 1 453 | 21 |
| `PerfCallRoundTrip` (the same call through the budget's fixture, kit's `gen_perf_test.go`) | 37 650 | 1 487 | 22 |
| `RawWriteRoundTrip` (64 bytes written and echoed back on the same pipe: no frame, no codec, no goroutine per call) | 3 850 | 0 | 0 |

A call costs about ten raw round trips of its bytes. The time is the hand-offs,
not the bytes: the caller's goroutine writes the request, the server's reader
reads it and starts the handler's goroutine, which writes the reply that the
client's reader hands back to the caller — four goroutine switches where the
raw echo has two, each a `net.Pipe` rendezvous. The allocations, all on the
happy path: the two frames (one buffer each, built in place through the
codec's `Appender`), the two frames read (one allocation each, made only once
the length was checked), the method's string on the server, the call's record,
its context and cancel, the client's pending call, its answer channel and its
`CallTimeout` timer, and json's own work both ways.

The budget in `design/sdk.yaml` holds the fixture's count — 22, the total over
30 000 calls with `GOMAXPROCS=1` and the collector off, process-wide, so the
server's allocations are counted with the client's — in the race-off lane
(`TestPerfAllocsCallRoundTrip`). A first version built each frame in a 4 KiB
buffer: 9 260 B and 48 µs a call, against 1 453 B and 37 µs since the frames
start at 256 bytes and grow; a stream's abandonment channel is no longer made
for a plain call.
