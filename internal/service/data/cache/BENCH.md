<!-- generated from internal/service/data/cache/cache_bench_test.go — run `cd internal/service && GOWORK=off go test -run='^$' -bench=. -benchmem -benchtime=1s ./data/cache/` to refresh -->
# Benchmarks — `internal/service/data/cache`

The cache domain (ADR 0049). These benchmarks exist to check two claims this
package makes in prose, because both of them are the kind that sound right and
are frequently false:

1. **Tag invalidation is O(k) in the entries carrying the tag, not O(N) in the
   store.**
2. **`Load` on a HIT does not enter the singleflight group at all.**

## Reproducibility envelope

> **Numbers vary across machines.** This report stamps the box that produced
> them so cross-machine deltas can be evaluated honestly.

| Dimension | Value |
|---|---|
| CPU cores          | 8 (AMD EPYC 7351P) |
| RAM                | 15 GiB |
| OS / kernel        | Linux 6.12.101+deb13-amd64 |
| Architecture       | amd64 |
| Go toolchain       | go1.27.1 linux/amd64 |
| Git branch         | `jaimerias-que-tu-te-connect` |
| Git commit         | `c76ec45` (pre-commit) |
| Generated (UTC)    | 2026-09-09 |
| Bench wall-clock   | `-test.benchtime=1s`, single run |

## Results

```
BenchmarkSet_Untagged-8           1758442    655.0 ns/op    215 B/op   2 allocs/op
BenchmarkSet_OneTag-8             1000000   1414   ns/op    232 B/op   3 allocs/op
BenchmarkSet_ThreeTags-8           671035   2224   ns/op    266 B/op   3 allocs/op
BenchmarkInvalidateTag_In1k-8       39220  31194   ns/op   1792 B/op   1 allocs/op
BenchmarkInvalidateTag_In100k-8     33176  36166   ns/op   1792 B/op   1 allocs/op
BenchmarkFetch_Hit-8              7122810    171.3 ns/op      0 B/op   0 allocs/op
BenchmarkLoad_Hit-8               7085799    166.3 ns/op      0 B/op   0 allocs/op
```

## Claim 1 — invalidation does not scan

Both invalidation benchmarks remove **exactly 100 entries carrying one tag**.
The only difference is how many OTHER entries the store holds: 900 versus
99 900, each carrying a tag of its own so the reverse index is genuinely
populated rather than trivially small.

| Store size | Entries removed | Time | Per removed entry |
|---|---|---|---|
| 1 000 | 100 | 31.2 µs | 312 ns |
| 100 000 | 100 | 36.2 µs | 362 ns |

**A 100× larger store costs 1.16× more.** A scanning implementation would cost
about 100× — roughly 3 ms instead of 36 µs. The residual 16 % is not work: it
is memory locality, since the same operation walks bigger maps and misses the
CPU cache more often.

The one allocation per call (1792 B) is `keysFor`'s copy of the tag bucket,
which exists because the loop that follows deletes from the very map it would
otherwise be ranging over. It is proportional to k, not to N.

## Claim 2 — a hit costs a Fetch and nothing more

`BenchmarkLoad_Hit` (166.3 ns/op, 0 allocs) is within noise of
`BenchmarkFetch_Hit` (171.3 ns/op, 0 allocs), so the early-return in `Load`
really does keep the hit path out of the singleflight group. Joining a group
costs a mutex acquisition, a map lookup and a channel receive
(`internal/kernel/concur/singleflight/BENCH.md` measures a leading call at ~2 µs), and
a cache whose hits paid that would be a cache that made things slower.

Both are allocation-free.

## What tagging costs

| | Time | Bytes | Allocs | Δ vs untagged |
|---|---|---|---|---|
| `Set` untagged | 655 ns | 215 B | 2 | — |
| `Set` + 1 tag | 1414 ns | 232 B | 3 | +759 ns, +17 B, +1 alloc |
| `Set` + 3 tags | 2224 ns | 266 B | 3 | +1569 ns, +51 B, +1 alloc |

So roughly **+760 ns for the first tag and +390 ns for each additional one**,
against a ~17 B/tag steady-state memory cost. The first tag is dearer because
it may create the reverse bucket; subsequent tags reuse the same shapes.

The byte figures are steady-state under eviction (the benchmark store holds
4096 entries and is continuously over-filled), so they are amortised across
insert *and* removal — which is what a running cache actually pays, and is
lower than a naive per-entry accounting would suggest. The tag TEXT is not
duplicated between the two index maps: Go strings are immutable, so both hold
headers pointing at one backing array.

## Wave L6 (2026-11-02): stripes, bytes, policy, sweep, batches, the chain

Measured on the shared 8-core AMD EPYC 7351P VM, go1.27.3, branch
`wip/sdk-lot-6-cache` (pre-commit), `-cpu=8 -benchtime=200ms`; base `ee1f48b`.

### The existing rows

```
                               │ svc-base.txt │             svc-new.txt             │
                               │    sec/op    │    sec/op     vs base               │
Set_Untagged-8                   717.3n ±  4%   865.4n ±  7%  +20.65% (p=0.002 n=6)
Set_OneTag-8                     1.405µ ±  8%   1.681µ ± 14%  +19.72% (p=0.002 n=6)
Set_ThreeTags-8                  2.187µ ±  1%   2.379µ ± 18%   +8.83% (p=0.002 n=6)
InvalidateTag_In1k-8             35.35µ ±  2%   37.26µ ± 18%   +5.39% (p=0.041 n=6)
InvalidateTag_In100k-8           41.34µ ±  9%   44.48µ ±  7%        ~ (p=0.132 n=6)
Fetch_Hit-8                      180.8n ± 14%   222.1n ±  5%  +22.84% (p=0.002 n=6)
Load_Hit-8                       207.9n ± 11%   213.0n ± 13%   +2.45% (p=0.026 n=6)
InvalidateTag_In100k_Striped-8                  49.60µ ± 30%
FetchMany_16-8                                  4.514µ ± 11%
Expire_10kDeadIn100k-8                          11.23m ± 10%
Chain_NearHit-8                                 379.8n ±  3%
Chain_FarHitPromote-8                           6.222µ ±  8%
geomean                          1.984µ         5.852µ        +12.23%
```

A hit costs ~15–25 % more and an untagged `Set` ~20 % more (214 → 278 B): the
record carries the negative entry's error and the measured weight (#838,
#839), the primitive's node its expiry handle, weight and policy counter
(#1038, #834), and every operation finds its stripe first (#808). Interleaved
re-runs put `Fetch_Hit` at +18 % to +29 % and `Load_Hit` at +15 % to +20 % —
over the 10 % #1059 allowed. Still 0 allocations on a hit. Tag invalidation
keeps its O(k) shape (`InvalidateTag_In1k` and `_In100k` within ~5 % of each
other and of the base).

### Stripes (#808)

```
BenchmarkFetch_Parallel/stripes=1           	  993510	       267.1 ns/op	       0 B/op	       0 allocs/op
BenchmarkFetch_Parallel/stripes=1-8         	  370041	       598.0 ns/op	       0 B/op	       0 allocs/op
BenchmarkFetch_Parallel/stripes=8           	  876453	       291.9 ns/op	       0 B/op	       0 allocs/op
BenchmarkFetch_Parallel/stripes=8-8         	  639080	       362.0 ns/op	       0 B/op	       0 allocs/op
BenchmarkFetch_Parallel/stripes=64          	  856066	       256.6 ns/op	       0 B/op	       0 allocs/op
BenchmarkFetch_Parallel/stripes=64-8        	 1000000	       202.4 ns/op	       0 B/op	       0 allocs/op
BenchmarkLoad_Parallel/stripes=1            	 1000000	       227.1 ns/op	       0 B/op	       0 allocs/op
BenchmarkLoad_Parallel/stripes=1-8          	  358414	       595.4 ns/op	       0 B/op	       0 allocs/op
BenchmarkLoad_Parallel/stripes=8            	  943200	       259.0 ns/op	       0 B/op	       0 allocs/op
BenchmarkLoad_Parallel/stripes=8-8          	  830887	       309.5 ns/op	       0 B/op	       0 allocs/op
BenchmarkLoad_Parallel/stripes=64           	  951540	       250.9 ns/op	       0 B/op	       0 allocs/op
BenchmarkLoad_Parallel/stripes=64-8         	 1632507	       213.5 ns/op	       0 B/op	       0 allocs/op
BenchmarkSet_Parallel/stripes=1             	 1727719	       138.5 ns/op	       0 B/op	       0 allocs/op
BenchmarkSet_Parallel/stripes=1-8           	  500049	       415.1 ns/op	       0 B/op	       0 allocs/op
BenchmarkSet_Parallel/stripes=8             	 1000000	       218.6 ns/op	       0 B/op	       0 allocs/op
BenchmarkSet_Parallel/stripes=8-8           	  767685	       327.8 ns/op	       0 B/op	       0 allocs/op
BenchmarkSet_Parallel/stripes=64            	 1352448	       177.9 ns/op	       0 B/op	       0 allocs/op
BenchmarkSet_Parallel/stripes=64-8          	 1521158	       199.0 ns/op	       0 B/op	       0 allocs/op
```

Each goroutine starts at its own offset in the key set. At 8 goroutines one
stripe serialises every hit behind the store's lock (`Fetch_Parallel/stripes=1-8`
~560 ns, 2.5× a single-threaded `Fetch_Hit`); 8 stripes bring it to ~360 ns
(1.6×, within #808's bound of 2×) and 64 stripes to ~200 ns, below a
single-threaded hit. `Load` (all hits) and `Set` follow. `InvalidateTag` walks
the stripes one lock at a time: `InvalidateTag_In100k_Striped` (8 stripes)
against `InvalidateTag_In100k` is the walk's cost.

### Batches, the sweep, the miss storm

```
BenchmarkInvalidateTag_In100k_Striped-8   	    5551	     48664 ns/op	    4480 B/op	      41 allocs/op
BenchmarkFetchMany_16-8                   	   49605	      5022 ns/op	       313.9 ns/key	    1240 B/op	       4 allocs/op
BenchmarkExpire_10kDeadIn100k-8           	      21	  11121788 ns/op	 4559747 B/op	      20 allocs/op
```

- **`FetchMany_16`** (~4.5 µs for 16 keys, ~280 ns a key, 4 allocations — the
  result map) is NOT below a single `Fetch_Hit` per key on the memory store:
  #840 asked for 40 %, and a lock round trip is not where a memory hit spends
  its time — the map lookup, the relink and the result map are. The batch pays
  where a key costs a round trip: a remote tier, or the chain below.
- **`Expire_10kDeadIn100k`** sweeps 10 000 dead entries among 100 000 in
  ~11 ms (~1.1 µs each: the heap pop, the list unlink, the map delete and the
  tag-index unlink); the 90 000 live ones are never visited.
- **`Load_MissParallel`** (every `Load` a new key, 8 stripes) is the miss storm
  #1035 took the singleflight group's lock off: ~4.1 µs at 8 goroutines
  against ~5.9 µs at one.

### The chain (#1121)

Two memory tiers — the configuration that isolates the chain's own cost, its
promotion lock (#460), from any tier's.

```
BenchmarkChain_NearHit-8                  	  601123	       384.4 ns/op	       0 B/op	       0 allocs/op
BenchmarkChain_FarHitPromote-8            	   33732	      6384 ns/op	    1176 B/op	       7 allocs/op
BenchmarkLoad_MissParallel               	   40593	      6112 ns/op	     613 B/op	       8 allocs/op
BenchmarkLoad_MissParallel-8             	   56611	      4314 ns/op	     613 B/op	       9 allocs/op
BenchmarkChain_SetUnderPromotions        	  228904	      1736 ns/op	       8 B/op	       0 allocs/op
BenchmarkChain_SetUnderPromotions-8      	   98811	      2858 ns/op	       9 B/op	       0 allocs/op
BenchmarkChain_DeleteUnderPromotions     	  615752	       566.5 ns/op	       3 B/op	       0 allocs/op
BenchmarkChain_DeleteUnderPromotions-8   	  485044	       580.2 ns/op	       0 B/op	       0 allocs/op
```

- **`Chain_NearHit`** (~380 ns, 0 allocs) is a near-tier hit: the far tier and
  the promotion lock are never touched; the delta against a memory
  `Fetch_Hit` is the walk and `FetchEntry`'s tag copy.
- **`Chain_FarHitPromote`** (~6.2 µs) misses near, hits far and promotes: the
  far `FetchEntry` (its tags cloned), the promotion lock taken for reading, and
  a `Set` into the near tier — the tag index and a new node there. Its 7
  allocations (~1.2 KiB) are the tag slice `FetchEntry` clones, the near tier's
  node, and the near index's owned copy of the tags and the tag's bucket, which
  the untimed `Delete` of every iteration had emptied.
- **`Chain_SetUnderPromotions`** — writers racing a goroutine that keeps
  promoting far hits — reads ~1.7 µs at one goroutine and ~2.9 µs at eight:
  every `Set` announces itself by taking the promotion lock for writing, so it
  waits out the promotion in flight, and eight writers queue on that one
  `RWMutex`. That is the lock #460 added, as a number.
  **`Chain_DeleteUnderPromotions`** (~0.6–0.7 µs) is the same race for `Delete`,
  cheaper because removing writes no node.

## What is deliberately not measured

- **Cross-process stampede protection** (`LoadLeased`, #830). Its hit path is
  `Load`'s, measured above; its miss path is the locker's — a `flock(2)`
  attempt per step on the file locker — and the poll step the caller chose,
  neither of them this package's cost. Its correctness is pinned across a real
  process boundary instead (`lease_process_external_test.go`, `CLAUDE.md`
  §"`LoadLeased` takes the lease").
