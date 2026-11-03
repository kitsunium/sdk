<!-- generated from internal/service/observe/logger/internal/datagram/datagram_bench_test.go — run `cd internal && go test -run=NONE -bench=. -benchmem -count=3 ./service/observe/logger/internal/datagram` to refresh -->
# Benchmarks — `internal/service/observe/logger/internal/datagram`

What a backlog of sixteen log records costs to leave as sixteen datagrams on
a connected udp socket: through `Send` — one `sendmmsg(2)` on Linux — and,
as the baseline, one `Write` (one `sendto(2)`) per record, which is what the
udp writer did before #807. The peer is a loopback socket nobody reads, so the
figure is the sender's own.

## Reproducibility envelope

| Dimension | Value |
|---|---|
| CPU                | AMD EPYC 7351P 16-Core Processor, 4 of its cores (`GOMAXPROCS=4`) |
| RAM                | 14 GiB |
| OS / kernel        | Linux 6.12.101+deb13-amd64 |
| Go toolchain       | go1.27.3 linux/amd64 |
| Git branch         | `wip/sdk-lot-5-obs` |
| Generated          | 2026-11-03 |
| Bench wall-clock   | `-benchtime=1s`, `-count=3` |

## Results

```
BenchmarkSend_UDP_Batch16-4   15328   76961 ns/op   4810 ns/record   0 B/op   0 allocs/op
BenchmarkSend_UDP_Batch16-4   24393   49778 ns/op   3111 ns/record   0 B/op   0 allocs/op
BenchmarkSend_UDP_Batch16-4   24904   48674 ns/op   3042 ns/record   0 B/op   0 allocs/op
BenchmarkSend_UDP_Each16-4    22704   52754 ns/op   3297 ns/record   0 B/op   0 allocs/op
BenchmarkSend_UDP_Each16-4    21980   53586 ns/op   3349 ns/record   0 B/op   0 allocs/op
BenchmarkSend_UDP_Each16-4    21451   54010 ns/op   3376 ns/record   0 B/op   0 allocs/op
```

## Reading

- **One system call per sixteen datagrams instead of sixteen**, pinned by
  `TestLinuxSendsABacklogInOneCallPerMaxBatch` rather than by strace: the
  saving is the fifteen syscall entries, ~8 % per record here, because on
  this VM the kernel's own per-datagram work — the udp and IP layers, the
  loopback delivery — is most of the 3 µs. A host where a syscall entry costs
  more (mitigations on, a hypervisor trapping it) saves more.
- **Zero allocations either way.** The headers and iovecs are the sender's,
  allocated once; the poller callback is a method value bound at
  construction, not a closure per call.
- The first `Batch16` run is the outlier the VM's neighbours give; the other
  two are the steady state.
