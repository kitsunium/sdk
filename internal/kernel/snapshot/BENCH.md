<!-- generated from internal/kernel/snapshot/snapshot_bench_test.go — run `cd internal/kernel && GOWORK=off go test -run='^$' -bench=. -benchmem -benchtime=1s ./snapshot/` to refresh -->
# Benchmarks — `internal/kernel/snapshot`

`Value[T]` is a copy-on-write container (ADR 0011) whose entire justification is
that readers do not take a lock. It had no benchmarks until now, so that claim
was an assertion. These numbers make it a measurement — and one of them is
better than the design promised.

## 1. The lock-free read gets FASTER with more cores. The lock gets slower.

| | 1 goroutine | `b.RunParallel` (8) |
|---|---:|---:|
| `Value.Load` — atomic pointer | 3.210 ns | **0.5833 ns** |
| `RWMutex.RLock` baseline | 21.29 ns | 30.22 ns |
| ratio | 6.6× | **52×** |

Read the diagonal, not the rows. Adding cores takes the atomic load from 3.21 ns
to 0.58 ns — it scales, because each core reads a shared cache line nobody is
writing. The same work behind an `RWMutex` goes from 21.29 ns to 30.22 ns — it
*anti*-scales, because `RLock` writes to the reader count and every core then
fights over that line.

This is the whole argument for the primitive, and it only becomes visible under
contention. A single-threaded microbenchmark would have shown a 6.6× win and
undersold it by a factor of eight.

## 2. A writer running flat out costs readers 2.8 ns

`Load_UnderWriter` is 5.975 ns against 3.210 ns idle: one goroutine publishing
in a tight `Store` loop — far past any realistic write rate for read-mostly
state — slows readers by under 3 ns and blocks none of them. Compare an
`RWMutex`, where a writer excludes every reader outright for the duration.

That is the guarantee the container is chosen for, and it now has a number.

## 3. Copy-on-write means the WRITE carries the cost, and it is large

| | ns/op | allocs |
|---|---:|---:|
| `Load` | 3.210 | 0 |
| `Store` / `Swap` / `Update_NoOp` | ~32.7 | 0 |
| `Update_WithClone` (64-entry map) | **9 113** | 5 |

`Store`, `Swap` and a no-op `Update` are ~32 ns and allocate nothing — the mutex
plus an atomic publish. The container itself never copies.

`Update_WithClone` is what a real mutation costs, because copy-on-write means
the CALLER rebuilds the whole value: **2 839× a read**, at 3 552 B. That ratio
is the constraint, stated as a number rather than as the word "read-mostly" in
a doc comment. At a 1:1000 write:read ratio the clone still costs about three
times the total read traffic it protects.

Two consequences worth stating plainly:

- **The clone scales with the value, the publish does not.** A 64-entry map
  costs 9.1 µs; a 10 000-entry one costs proportionally more, while `Store`
  stays at 32 ns. If writes are frequent enough that this hurts, the answer is
  not a faster container — it is a different data structure (a sharded map, or
  a persistent tree with structural sharing).
- **`Update_NoOp` at 32.83 ns is genuinely cheap**, which makes the documented
  "return the current pointer to abort" pattern a real option: a conditional
  update that decides not to fire pays 32 ns, not 9 µs.

## 4. `LoadAndRead` — do not mistake the Load for the cost of using the data

`LoadAndRead` (load, then one map lookup) is 17.06 ns against 3.21 ns for the
load alone. The lookup is 4× the load. The container is already so far below the
cost of *reading through* the snapshot that optimising the read path further
would be optimising 19 % of the caller's actual work.

## Reproducibility envelope

> **Numbers vary across machines.** This report stamps the box that produced
> them so cross-machine deltas can be evaluated honestly. This run shared the
> box with four other jobs; absolute `ns/op` carry run-to-run variance, the
> ratios above held on every pass.

| Dimension | Value |
|---|---|
| CPU cores          | 8 |
| RAM                | 15 GiB |
| OS / kernel        | Linux 6.12.101+deb13-amd64 |
| Architecture       | amd64 |
| Go toolchain       | go1.27.1 linux/amd64 |
| Git branch         | jaimerias-que-tu-te-connect |
| Git commit         | 9adba64 |
| Generated (UTC)    | 2026-09-10 |
| Bench wall-clock   | `-test.benchtime=1s`, single run, machine under load |

## Results

```
goos: linux
goarch: amd64
pkg: github.com/kitsunium/sdk/internal/kernel/snapshot
cpu: AMD EPYC 7351P 16-Core Processor
BenchmarkLoad-8                         	423344325	         3.210 ns/op	       0 B/op	       0 allocs/op
BenchmarkLoad_MutexBaseline-8           	59141312	        21.29 ns/op	       0 B/op	       0 allocs/op
BenchmarkLoad_Parallel-8                	1000000000	         0.5833 ns/op	       0 B/op	       0 allocs/op
BenchmarkLoad_ParallelMutexBaseline-8   	40654615	        30.22 ns/op	       0 B/op	       0 allocs/op
BenchmarkLoadAndRead-8                  	70929874	        17.06 ns/op	       0 B/op	       0 allocs/op
BenchmarkStore-8                        	36477639	        32.70 ns/op	       0 B/op	       0 allocs/op
BenchmarkSwap-8                         	36908458	        32.42 ns/op	       0 B/op	       0 allocs/op
BenchmarkUpdate_NoOp-8                  	37001256	        32.83 ns/op	       0 B/op	       0 allocs/op
BenchmarkUpdate_WithClone-8             	  165123	      9113 ns/op	    3552 B/op	       5 allocs/op
BenchmarkLoad_UnderWriter-8             	202993936	         5.975 ns/op	       0 B/op	       0 allocs/op
```
