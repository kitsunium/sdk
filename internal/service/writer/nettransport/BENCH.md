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
| RAM                | 15.3 GiB |
| OS / kernel        | Linux 6.12.107+deb13-amd64 (Debian 13 trixie) |
| Architecture       | amd64 |
| Go toolchain       | go1.27.1 linux/amd64 |
| Git branch         | fix/bench-127 |
| Git commit         | c08d730 |
| Generated (UTC)    | 2026-09-15 |
| Bench wall-clock   | `-benchtime=1s -count=5`, median of 5 runs |

> **What these numbers support.** `B/op` and `allocs/op` are exact — all 5
> repeats agreed on every cell — and they are **unchanged** from a go1.26.4 run
> of this same code on this same box (124 benchmarks compared SDK-wide, 44 of
> them allocating, zero counter moved). `ns/op` are medians and carry the
> `spread` shown, which is a **within-run** figure that understates run-to-run
> variance: re-running the identical binary on this box moved individual cells
> by up to 94 %. Read ns/op as an order of magnitude on this box, never as a
> cross-edition or cross-machine delta.

## Results

```
goos: linux
goarch: amd64
pkg: github.com/kitsunium/sdk/internal/service/writer/nettransport
cpu: 12th Gen Intel(R) Core(TM) i7-1255U

benchmark   median ns/op   spread   min–max         B/op   allocs/op
Write-12           28.49     7.4%   28.03 – 30.13      0           0
```

## How to read this

- **`0 allocs/op`, `0 B/op` — the conn (tcp/udp) Write path allocates nothing.**
  `netSink.Write` ships the caller's payload to the transport seam synchronously
  under a mutex; the payload is consumed before Write returns, so no defensive
  clone is needed even though the caller recycles the buffer. This preserves the
  zero-alloc invariant of the logger hot path (ADR 0014) all the way to a
  network terminal.
- **~28 ns/op is the mutex + indirect-call overhead**, not transport I/O — the
  real socket write happens inside the seam (measured here as a no-op). The
  async middleware composed around this sink keeps the producer off this path
  entirely; drops surface through `OnDrop`, never a blocked producer.
- **HTTP is the exception, by design.** The `http` seam allocates per request
  (request + body reader) — HTTP egress is not a 0-alloc path. The 0-alloc
  guarantee is the conn (tcp/udp) seam's; HTTP trades it for batched-collector
  compatibility (Loki/Datadog/Elastic).
