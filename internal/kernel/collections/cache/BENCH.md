<!-- generated from internal/kernel/collections/cache/cache_bench_test.go — run `cd internal/kernel && GOWORK=off go test -run='^$' -bench=. -benchmem -benchtime=1s ./collections/cache/` to refresh -->
# Benchmarks — `internal/kernel/collections/cache`

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
that is what the interface is for. Since #814 the SDK ships that `Clock` — `clock.NewCoarse` —
outside the primitive, which still owns no goroutine.

## 2. One mutex, taken exclusively, even on a read

`Fetch_Parallel` is 375.5 ns against 59.89 ns single-threaded — roughly 6× worse
under 8 goroutines rather than better. That is not a defect, it is the design
stated in numbers: a hit MUTATES the LRU list, so `Fetch` takes the write lock,
which is precisely why ADR 0025 named the method `Fetch` and not `Get`. There is
no read path that avoids it.

A caller whose workload is read-mostly and contended should shard: N caches keyed
by hash rather than one cache under N goroutines. Since #808 the sibling
`Striped` does it — the stripe count stays the caller's, because it is a
property of the caller's key distribution.

## Wave L6 (2026-11-02): expiry sweep, weights, S3-FIFO, stripes, coarse clock

Measured on the same 8-core AMD EPYC 7351P VM — shared with other jobs, so
noisier than the table below: read the ratios — with go1.27.3, branch
`wip/sdk-lot-6-cache` (pre-commit), `-cpu=8 -benchtime=200ms`. The base is
`ee1f48b`, measured interleaved with the new build (three rounds of two).

### What the existing rows cost now

```
                                │ kcache-base3.txt │           kcache-new3.txt           │
                                │      sec/op      │    sec/op     vs base               │
NewCache-8                            17.88µ ± 53%   17.03µ ± 32%        ~ (p=0.589 n=6)
Fetch_Hit-8                           64.45n ±  4%   69.98n ± 37%        ~ (p=0.394 n=6)
Fetch_HitWithTTL-8                    162.6n ± 16%   142.8n ± 66%        ~ (p=0.485 n=6)
Fetch_Miss-8                          45.22n ±  4%   45.85n ±  6%        ~ (p=0.905 n=6)
Set_NewKey-8                          262.1n ±  8%   298.7n ±  6%  +13.96% (p=0.002 n=6)
Set_Overwrite-8                       57.36n ± 15%   58.46n ±  9%        ~ (p=0.394 n=6)
Set_AtCapacity-8                      245.3n ± 16%   298.6n ± 12%  +21.75% (p=0.009 n=6)
Set_AtCapacityOnEvict-8               312.5n ± 13%   352.7n ±  8%  +12.88% (p=0.015 n=6)
Len-8                                 21.11n ±  7%   21.21n ±  6%        ~ (p=0.937 n=6)
Stats-8                               23.75n ±  6%   29.45n ±  3%  +24.00% (p=0.002 n=6)
Fetch_Hit_S3FIFO-8                                   63.27n ± 34%
Set_AtCapacity_S3FIFO-8                              484.5n ± 12%
Set_NewKey_WeightOf-8                                312.7n ± 17%
Expire_10kEntries_1pctExpired-8                      40.05µ ± 12%
Expire_NothingDue-8                                  42.91n ±  6%
Fetch_HitWithTTL_Coarse-8                            76.02n ± 22%
Striped_Fetch_Hit-8                                  93.80n ± 14%
geomean                               147.6n         197.4n         +6.23%
```

- **Hits are unchanged within noise** (`Fetch_Hit`, `Fetch_HitWithTTL`,
  `Fetch_Miss`, `Set_Overwrite`, `Len`): an entry without a deadline never
  touches the expiry heap, and an LRU hit still relinks one node.
- **A new key costs ~14 % more** (`Set_NewKey`, 170 → 186 B): the node carries
  its expiry-heap handle and a 32-bit weight, the S3-FIFO counter and its
  queue packed beside it, and the insert asks the policy where it goes. **An
  insert at capacity costs ~22 % more** (`Set_AtCapacity`): the eviction loop
  looks at the expiry heap's top first (#1059) and asks the policy for its
  victim. `Stats` copies two more fields. These are the price of #1038, #1059,
  #839 and #834 on the paths that have no use for them — #1038 asked for
  `Set_NewKey` within 5 %, and it is not.

### Hit ratio — S3-FIFO against LRU (#834)

A million keys drawn from a Zipf law (`s` 0.8, 1.0, 1.2), a cache of 1 % or
10 % of them, warmed with twice its capacity; `hit/op` is the share of reads
answered (a miss is followed by the `Set` a read-through cache makes). The
scan case alternates Zipf phases (s = 1.0, four times the capacity) with a
sweep of twice the capacity over keys never seen before, and counts the Zipf
reads only.

```
BenchmarkHitRatio/lru/zipf-s0.8/cap1%-8         	 3344206	       313.1 ns/op	         0.2320 hit/op	      61 B/op	       0 allocs/op
BenchmarkHitRatio/lru/zipf-s0.8/cap10%-8        	 2588185	       469.6 ns/op	         0.4870 hit/op	      42 B/op	       0 allocs/op
BenchmarkHitRatio/lru/zipf-s1.0/cap1%-8         	 6156710	       185.4 ns/op	         0.5847 hit/op	      33 B/op	       0 allocs/op
BenchmarkHitRatio/lru/zipf-s1.0/cap10%-8        	 4664016	       347.7 ns/op	         0.7786 hit/op	      18 B/op	       0 allocs/op
BenchmarkHitRatio/lru/zipf-s1.2/cap1%-8         	11316370	       108.1 ns/op	         0.8756 hit/op	       9 B/op	       0 allocs/op
BenchmarkHitRatio/lru/zipf-s1.2/cap10%-8        	11727729	       136.7 ns/op	         0.9484 hit/op	       4 B/op	       0 allocs/op
BenchmarkHitRatio/s3fifo/zipf-s0.8/cap1%-8      	 2686101	       449.2 ns/op	         0.3347 hit/op	      53 B/op	       0 allocs/op
BenchmarkHitRatio/s3fifo/zipf-s0.8/cap10%-8     	 1836152	       656.8 ns/op	         0.5453 hit/op	      44 B/op	       0 allocs/op
BenchmarkHitRatio/s3fifo/zipf-s1.0/cap1%-8      	 4714636	       257.6 ns/op	         0.6606 hit/op	      27 B/op	       0 allocs/op
BenchmarkHitRatio/s3fifo/zipf-s1.0/cap10%-8     	 4310767	       341.0 ns/op	         0.8177 hit/op	      18 B/op	       0 allocs/op
BenchmarkHitRatio/s3fifo/zipf-s1.2/cap1%-8      	11483523	       111.7 ns/op	         0.9021 hit/op	       7 B/op	       0 allocs/op
BenchmarkHitRatio/s3fifo/zipf-s1.2/cap10%-8     	14741167	       107.3 ns/op	         0.9583 hit/op	       4 B/op	       0 allocs/op
BenchmarkHitRatio_Scan/lru/cap10%-8             	 4416598	       314.8 ns/op	         0.7169 hit/op	      41 B/op	       0 allocs/op
BenchmarkHitRatio_Scan/s3fifo/cap10%-8          	 3077209	       514.2 ns/op	         0.7744 hit/op	      43 B/op	       0 allocs/op
```

| Workload | LRU | S3-FIFO | Δ (points) |
|---|---|---|---|
| s0.8, 1 % | 23.2 % | 33.5 % | +10.3 |
| s0.8, 10 % | 48.7 % | 54.5 % | +5.8 |
| s1.0, 1 % | 58.5 % | 66.1 % | +7.6 |
| s1.0, 10 % | 77.9 % | 81.8 % | +3.9 |
| s1.2, 1 % | 87.6 % | 90.2 % | +2.7 |
| s1.2, 10 % | 94.8 % | 95.8 % | +1.0 |
| scan, 10 % | 71.7 % | 77.4 % | +5.8 |

S3-FIFO wins everywhere, most where the cache is small against a flat
distribution. #834 asked for +5 points on s1.0/10 % and +10 on the scan case:
this implementation gives +3.9 and +5.8. The hit itself costs the same
(`Fetch_Hit_S3FIFO` against `Fetch_Hit`, within the +15 ns the issue allows); an
insert at capacity costs more (`Set_AtCapacity_S3FIFO`, the probation walk and
the ghost's hash). W-TinyLFU, the issue's second step, is not built.

### Expiry sweep (#1038, #1059)

`Expire_10kEntries_1pctExpired` pops the 100 expired entries of 10 000 that
all carry a deadline — ~40 µs, ~400 ns per reaped entry (heap removal, list
unlink, map delete), the 9 900 live ones never visited. `Expire_NothingDue`
(~43 ns, 0 allocs) is a lock round trip: no deadline, no clock read.

### Weights (#839)

`Set_NewKey_WeightOf` is within noise of `Set_NewKey` (the issue's bound is
+20 ns): the weigher is one call, the sums two additions.

### Striped (#808)

```
BenchmarkFetch_Parallel             	 2944399	        76.94 ns/op	       0 B/op	       0 allocs/op
BenchmarkFetch_Parallel-2           	 2709606	       104.8 ns/op	       0 B/op	       0 allocs/op
BenchmarkFetch_Parallel-4           	 1508265	       174.7 ns/op	       0 B/op	       0 allocs/op
BenchmarkFetch_Parallel-8           	 1000000	       336.2 ns/op	       0 B/op	       0 allocs/op
BenchmarkStriped_Fetch_Parallel     	 2267826	       157.1 ns/op	       0 B/op	       0 allocs/op
BenchmarkStriped_Fetch_Parallel-2   	 1221050	       186.0 ns/op	       0 B/op	       0 allocs/op
BenchmarkStriped_Fetch_Parallel-4   	 1891951	       183.2 ns/op	       0 B/op	       0 allocs/op
BenchmarkStriped_Fetch_Parallel-8   	 1442833	       179.6 ns/op	       0 B/op	       0 allocs/op
BenchmarkSet_Parallel               	 3442987	        66.60 ns/op	       0 B/op	       0 allocs/op
BenchmarkSet_Parallel-2             	 2114078	       108.3 ns/op	       0 B/op	       0 allocs/op
BenchmarkSet_Parallel-4             	 1000000	       252.8 ns/op	       0 B/op	       0 allocs/op
BenchmarkSet_Parallel-8             	 1099664	       311.3 ns/op	       0 B/op	       0 allocs/op
BenchmarkStriped_Set_Parallel       	 2860788	        81.69 ns/op	       0 B/op	       0 allocs/op
BenchmarkStriped_Set_Parallel-2     	 1000000	       216.8 ns/op	       0 B/op	       0 allocs/op
BenchmarkStriped_Set_Parallel-4     	 1000000	       234.0 ns/op	       0 B/op	       0 allocs/op
BenchmarkStriped_Set_Parallel-8     	 1603776	       164.9 ns/op	       0 B/op	       0 allocs/op
```

With each goroutine starting at its own offset in the key set, 16 stripes take
`Fetch_Parallel-8` from ~325 ns to ~181 ns and `Set_Parallel-8` from ~330 ns to
~164 ns — about half — but not to the issue's bound of twice a single-threaded
hit (~64 ns): on this shared VM a stripe's lock and its map still bounce between
cores. `Striped_Fetch_Hit` (~91 ns) is the single-goroutine price of the key's
hash (`maphash.Comparable`).

### Coarse clock (#814)

```
BenchmarkFetch_Hit-8                 	 4087004	        58.73 ns/op	       0 B/op	       0 allocs/op
BenchmarkFetch_HitWithTTL-8          	 1401721	       169.2 ns/op	       0 B/op	       0 allocs/op
BenchmarkFetch_HitWithTTL_Coarse-8   	 3558258	        82.26 ns/op	       0 B/op	       0 allocs/op
BenchmarkStriped_Fetch_Hit-8         	 2487843	        91.83 ns/op	       0 B/op	       0 allocs/op
```

With a `clock.CoarseClock` (1 ms) as the cache's clock, a TTL'd hit drops from
~160 ns to ~70–85 ns, 1.1–1.3× an untimed hit across runs (the issue's bound is
1.2×): the clock read is an atomic load (`clock` BENCH.md, `Coarse_Now` ~3.7 ns).

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
- **`Keys_1024`** (#816) — ~34 µs and **1 alloc** (18 432 B) for a whole walk of
  1024 keys, measured 2026-11-02 on the 4-CPU lane, go1.27.3,
  `-benchtime=300ms -count=4` (33.5–35.4 µs): the snapshot slice, filled under
  the read lock, and the loop over it with the lock released. The slice is
  the price of the lock rule — a body that calls `Set` cannot run under `mu`.

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
| Git commit         | e2fbe07 |
| Generated (UTC)    | 2026-09-10 |
| Bench wall-clock   | `-test.benchtime=1s`, single run |

## Results

```
goos: linux
goarch: amd64
pkg: github.com/kitsunium/sdk/internal/kernel/collections/cache
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
