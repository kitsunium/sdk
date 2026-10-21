<!-- generated from internal/service/app/forward/forward_bench_test.go — run `cd internal && go test -run=NONE -bench=. -benchmem -benchtime=1s ./service/app/forward` to refresh -->
# Benchmarks — `internal/service/app/forward`

The producer's path: what one `Enqueue` costs — the bound, the full policy and
the publication into the queue (ADR 0185). It is the cost a trace `BatchSink`
pays per batch once a forwarder stands where the network exporter was. The
consumer's path is a network round trip and is not measured here.

## Reproducibility envelope

> **Numbers vary across machines.** The `file` figure is a property of *this*
> device — a VM on overlayfs — where one publication is a write, an fsync and a
> rename. The shape travels: the forwarder's own work is microseconds, the
> durable write is milliseconds.

| Dimension | Value |
|---|---|
| CPU cores          | 8 (AMD EPYC 7351P 16-Core Processor) |
| RAM                | 14 GiB |
| OS / kernel        | Linux 6.12.101+deb13-amd64 |
| Architecture       | amd64 |
| Go toolchain       | go1.27.3 linux/amd64 |
| Git branch         | `feat/observe-store-and-forward` |
| Git commit         | `1ed3998` (pre-commit) |
| Generated (UTC)    | 2026-10-21 |
| Bench wall-clock   | `-benchtime=1s`, single run, 4 s total |

## Results

A 16 KiB batch — a few dozen spans as OTLP/JSON.

```
BenchmarkEnqueue/memory-8      	   53151	     25794 ns/op	 635.18 MB/s	   16714 B/op	       6 allocs/op
BenchmarkEnqueue/file-8        	     379	   3176990 ns/op	   5.16 MB/s	    2096 B/op	      42 allocs/op
BenchmarkEnqueueDropOldest-8   	   65396	     16340 ns/op	1002.69 MB/s	   33431 B/op	      10 allocs/op
```

## Reading

- **memory** isolates the forwarder: the reservation, the two locks and the
  broker's copy of the payload, which is the 16 KiB of the 16.7 KB allocated.
- **file** is what a durable export costs: ~3 ms, the fsync of this device,
  where an OTLP/HTTP export to a collector is a round trip plus the
  collector's own work — and, while the collector is down, an error.
- **DropOldest** on a full queue: a `Receive` and an `Ack` of the oldest batch
  before the publication. It is cheaper per op than the empty queue here only
  because the memory broker's queue stays at 64 messages instead of growing
  with the run.
