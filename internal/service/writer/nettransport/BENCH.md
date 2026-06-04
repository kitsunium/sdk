<!-- generated from internal/service/writer/nettransport/netsink_bench_test.go — run `cd internal/service && GOWORK=off go test -run='^$' -bench=. -benchmem -benchtime=1s ./writer/nettransport/` to refresh -->
# Benchmarks — `internal/service/writer/nettransport`

Stdlib network writer (tcp/udp/http) — ADR 0015. The benchmark isolates the
per-record **Write** path of `netSink` with a no-op send seam, so the reported
cost is the SDK overhead alone (one mutex round-trip + the seam call) with no
real socket.

## Reproducibility envelope

> **Numbers vary across machines.** This report stamps the box that produced
> them so cross-machine deltas can be evaluated honestly.

| Dimension | Value |
|---|---|
| CPU                | 12th Gen Intel(R) Core(TM) i7-1255U |
| CPU cores          | 12 |
| RAM                | 15 GiB |
| OS / kernel        | Linux 6.12.90+deb13-amd64 |
| Architecture       | amd64 |
| Go toolchain       | go1.26.3 linux/amd64 |
| Git branch         | feat/logger-perfection |
| Git commit         | (current HEAD, pre-commit) |
| Generated (UTC)    | 2026-06-02 |
| Bench wall-clock   | `-test.benchtime=1s`, single run |

## Results

```
BenchmarkWrite-12    	66839397	        16.19 ns/op	       0 B/op	       0 allocs/op
```

## How to read this

- **`0 allocs/op`, `0 B/op` — the conn (tcp/udp) Write path allocates nothing.**
  `netSink.Write` ships the caller's payload to the transport seam synchronously
  under a mutex; the payload is consumed before Write returns, so no defensive
  clone is needed even though the caller recycles the buffer. This preserves the
  zero-alloc invariant of the logger hot path (ADR 0014) all the way to a
  network terminal.
- **~16 ns/op is the mutex + indirect-call overhead**, not transport I/O — the
  real socket write happens inside the seam (measured here as a no-op). The
  async middleware composed around this sink keeps the producer off this path
  entirely; drops surface through `OnDrop`, never a blocked producer.
- **HTTP is the exception, by design.** The `http` seam allocates per request
  (request + body reader) — HTTP egress is not a 0-alloc path. The 0-alloc
  guarantee is the conn (tcp/udp) seam's; HTTP trades it for batched-collector
  compatibility (Loki/Datadog/Elastic).
