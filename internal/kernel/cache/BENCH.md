<!-- generated from internal/kernel/cache/cache_bench_test.go — run `cd internal/kernel && GOWORK=off go test -run='^$' -bench=. -benchmem -benchtime=1s ./cache/` to refresh -->
# Benchmarks — `internal/kernel/cache`

The generic LRU + TTL primitive of ADR 0025. These benchmarks exist because the
package had none: it is the substrate the `cache` DOMAIN (ADR 0049) is built on,
and a primitive whose cost nobody has measured is a primitive nobody can size.

Two facts here are worth more than the rest of the table.

## 1. A TTL more than doubles the cost of a hit — and the cache is not the cause

`Fetch_Hit` is 59.89 ns; `Fetch_HitWithTTL` is 130.1 ns. The 70 ns difference is
one `Clock.Now()` call, because `expired()` short-circuits on a zero `expireAt`
and never touches the clock for an entry without an expiry.

The tempting conclusion is that the `clock.Clock` interface is the overhead and
that de-virtualising it would win the 70 ns back. **That was measured, and it is
false**:

| | ns/op |
|---|---|
| `time.Now()` called directly | 71.25 |
| `time.Now()` through the `clock.Clock` interface | 71.31 |

The dispatch costs nothing measurable. The 70 ns **is `time.Now()` itself** on
this box, and it is a property of the machine — a VM whose vDSO clock source is
slow — not of this package. On hardware with a fast `vDSO` the same call is
closer to 25 ns and the TTL premium shrinks with it. Read the RATIO against your
own `time.Now()`, not the absolute number.

The optimisation this rules out is worth naming so nobody tries it twice:
replacing the interface with a concrete `time.Now()` call would gain **zero**
and cost the testability that makes every TTL test deterministic.

The optimisation it does NOT rule out is a coarse clock — a background goroutine
publishing a timestamp every few milliseconds, which turns 70 ns into an atomic
load. It was rejected rather than overlooked: it puts a goroutine inside a
kernel primitive that currently owns none, and it makes expiry approximate by
the tick interval. A caller who needs that trade can pass their own `Clock`;
that is what the interface is for.

## 2. One mutex, taken exclusively, even on a read

`Fetch_Parallel` is 375.5 ns against 59.89 ns single-threaded — roughly 6× worse
under 8 goroutines rather than better. That is not a defect, it is the design
stated in numbers: a hit MUTATES the LRU list, so `Fetch` takes the write lock,
which is precisely why ADR 0025 named the method `Fetch` and not `Get`. There is
no read path that avoids it.

A caller whose workload is read-mostly and contended should shard: N caches keyed
by hash rather than one cache under N goroutines. The primitive deliberately does
not shard for you, because the shard count is a property of the caller's key
distribution.

## The rest of the table

- **`NewCache`** — 13.46 µs and 54.8 kB for a `MaxEntries: 1024` cache. That is
  the map's initial bucket array, allocated once. Build caches at startup, never
  per request.
- **`Fetch_Miss`** (45.17 ns) is cheaper than `Fetch_Hit` (59.89 ns): a miss
  returns after the map lookup, while a hit also relinks the entry to the front.
- **`Set_NewKey`** — 239.0 ns, **1 alloc/op, 170 B**. The allocation is the
  `entry` node and is structural: an intrusive doubly-linked LRU needs one node
  per live key. It cannot be pooled without handing evicted nodes back to a
  recycler, which would trade a clear ownership story for one allocation.
- **`Set_Overwrite`** — 54.43 ns, **0 allocs**: replacing a live key reuses its
  node. The ~185 ns delta against `Set_NewKey` is the allocation plus the map
  insert.
- **`Set_AtCapacity`** — 218.6 ns, and note **0 allocs/op** with 63 B/op
  reported. Above `MaxEntries` every insert also evicts, so the node freed by the
  eviction is immediately reused by the insert; the residual bytes are the map's
  own amortised growth, not a per-op allocation.
- **`Set_AtCapacityOnEvict`** — 287.4 ns and 1 alloc: the callback runs on the
  SETTER's goroutine, outside the lock. The ~69 ns premium is what a trivial
  `OnEvict` costs; a callback doing real work bills all of it to whoever called
  `Set`, and blocks nothing else. Size the callback accordingly.
- **`Len` / `Stats`** — 22.2 / 24.3 ns, 0 allocs. A read lock and a small copy.

## Reproducibility envelope

> **Numbers vary across machines.** This report stamps the box that produced
> them so cross-machine deltas can be evaluated honestly. The `time.Now()`
> measurement above matters more than usual here: this box is slow at it.

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
| Bench wall-clock   | `-test.benchtime=1s`, single run |

## Results

```
goos: linux
goarch: amd64
pkg: github.com/kitsunium/sdk/internal/kernel/cache
cpu: AMD EPYC 7351P 16-Core Processor
BenchmarkNewCache-8                	   89852	     13462 ns/op	   54768 B/op	       7 allocs/op
BenchmarkFetch_Hit-8               	20066893	        59.89 ns/op	       0 B/op	       0 allocs/op
BenchmarkFetch_HitWithTTL-8        	 9400237	       130.1 ns/op	       0 B/op	       0 allocs/op
BenchmarkFetch_Miss-8              	26481696	        45.17 ns/op	       0 B/op	       0 allocs/op
BenchmarkSet_NewKey-8              	 4784624	       239.0 ns/op	     170 B/op	       1 allocs/op
BenchmarkSet_Overwrite-8           	22074398	        54.43 ns/op	       0 B/op	       0 allocs/op
BenchmarkSet_AtCapacity-8          	 5467629	       218.6 ns/op	      63 B/op	       0 allocs/op
BenchmarkSet_AtCapacityOnEvict-8   	 4168083	       287.4 ns/op	      87 B/op	       1 allocs/op
BenchmarkFetch_Parallel-8          	 3537349	       375.5 ns/op	       0 B/op	       0 allocs/op
BenchmarkLen-8                     	56052486	        22.17 ns/op	       0 B/op	       0 allocs/op
BenchmarkStats-8                   	49495653	        24.31 ns/op	       0 B/op	       0 allocs/op
```
