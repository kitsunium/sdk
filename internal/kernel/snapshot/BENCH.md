<!-- generated from internal/kernel/snapshot/snapshot_bench_test.go — run `cd internal/kernel && GOWORK=off go test -run='^$' -bench=. -benchmem -benchtime=1s ./snapshot/` to refresh -->
# Benchmarks — `internal/kernel/snapshot`

`Value[T]` is a copy-on-write container (ADR 0011) whose entire justification is
that readers do not take a lock. It had no benchmarks until now, so that claim
was an assertion. These numbers make it a measurement — and one of them is
better than the design promised.

## 1. The lock-free read gets FASTER with more cores. The lock gets slower.

| | 1 goroutine | `b.RunParallel` (8) |
|---|---:|---:|
| `Value.Load` — atomic pointer | 2.818 ns | **0.3159 ns** |
| `RWMutex.RLock` baseline | 19.92 ns | 30.46 ns |
| ratio | 7.1× | **96×** |

Read the diagonal, not the rows. Adding cores takes the atomic load from 2.82 ns
to 0.32 ns — it scales, because each core reads a shared cache line nobody is
writing. The same work behind an `RWMutex` goes from 19.92 ns to 30.46 ns — it
*anti*-scales, because `RLock` writes to the reader count and every core then
fights over that line.

This is the whole argument for the primitive, and it only becomes visible under
contention. A single-threaded microbenchmark would have shown a 7.1× win and
undersold it by more than a factor of thirteen.

## 2. A writer running flat out costs readers 2.8 ns

`Load_UnderWriter` is 5.346 ns against 2.818 ns idle: one goroutine publishing
in a tight `Store` loop — far past any realistic write rate for read-mostly
state — slows readers by 2.5 ns and blocks none of them. Compare an
`RWMutex`, where a writer excludes every reader outright for the duration.

That is the guarantee the container is chosen for, and it now has a number.

## 3. Copy-on-write means the WRITE carries the cost, and it is large

| | ns/op | allocs |
|---|---:|---:|
| `Load` | 2.818 | 0 |
| `Store` / `Swap` / `Update_NoOp` | ~32.3 | 0 |
| `Update_WithClone` (64-entry map) | **4 035** | 5 |

`Store`, `Swap` and a no-op `Update` are ~32 ns and allocate nothing — the mutex
plus an atomic publish. The container itself never copies.

`Update_WithClone` is what a real mutation costs, because copy-on-write means
the CALLER rebuilds the whole value: **1 432× a read**, at 3 552 B. That ratio
is the constraint, stated as a number rather than as the word "read-mostly" in
a doc comment. At a 1:1000 write:read ratio the clone still costs about
half again the total read traffic it protects.

The clone here uses `maps.Copy`, and that is not cosmetic: the hand-rolled
`for k, v := range` it replaced measured **9 113 ns**, so the linter rule that
asked for it (`KTN-MDRNZ-MAPSLOOP`) is worth **2.3×** on this path.

Two consequences worth stating plainly:

- **The clone scales with the value, the publish does not.** A 64-entry map
  costs 4.0 µs; a 10 000-entry one costs proportionally more, while `Store`
  stays at 32 ns. If writes are frequent enough that this hurts, the answer is
  not a faster container — it is a different data structure (a sharded map, or
  a persistent tree with structural sharing).
- **`Update_NoOp` at 32.21 ns is genuinely cheap**, which makes the documented
  "return the current pointer to abort" pattern a real option: a conditional
  update that decides not to fire pays 32 ns, not 4 µs.

## 4. `LoadAndRead` — do not mistake the Load for the cost of using the data

`LoadAndRead` (load, then one map lookup) is 16.95 ns against 2.82 ns for the
load alone. The lookup is 5× the load. The container is already so far below the
cost of *reading through* the snapshot that optimising the read path further
would be optimising 17 % of the caller's actual work.

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
BenchmarkLoad-8                         	425849398	         2.818 ns/op	       0 B/op	       0 allocs/op
BenchmarkLoad_MutexBaseline-8           	60472248	        19.92 ns/op	       0 B/op	       0 allocs/op
BenchmarkLoad_Parallel-8                	1000000000	         0.3159 ns/op	       0 B/op	       0 allocs/op
BenchmarkLoad_ParallelMutexBaseline-8   	39454285	        30.46 ns/op	       0 B/op	       0 allocs/op
BenchmarkLoadAndRead-8                  	71320592	        16.95 ns/op	       0 B/op	       0 allocs/op
BenchmarkStore-8                        	37464098	        32.06 ns/op	       0 B/op	       0 allocs/op
BenchmarkSwap-8                         	36932539	        32.49 ns/op	       0 B/op	       0 allocs/op
BenchmarkUpdate_NoOp-8                  	36710956	        32.21 ns/op	       0 B/op	       0 allocs/op
BenchmarkUpdate_WithClone-8             	  291166	      4035 ns/op	    3552 B/op	       5 allocs/op
BenchmarkLoad_UnderWriter-8             	223427228	         5.346 ns/op	       0 B/op	       0 allocs/op
```
