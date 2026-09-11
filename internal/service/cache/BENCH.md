<!-- generated from internal/service/cache/cache_bench_test.go — run `cd internal/service && GOWORK=off go test -run='^$' -bench=. -benchmem -benchtime=1s ./cache/` to refresh -->
# Benchmarks — `internal/service/cache`

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
(`internal/kernel/singleflight/BENCH.md` measures a leading call at ~2 µs), and
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

## What is deliberately not measured

- **Chained tiers.** A chain's cost is the sum of its tiers' plus one
  promotion, and both halves are already here. Benchmarking two memory tiers
  would measure a configuration nobody deploys.
- **Cross-process stampede protection.** There is none — see `CLAUDE.md`
  §"One process, and only one".
