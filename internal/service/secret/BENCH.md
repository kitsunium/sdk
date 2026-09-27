<!-- generated from internal/service/secret/subjectkeys_bench_test.go — run `cd internal/service && GOWORK=off go test -run='^$' -bench='SubjectKeys|CryptoSealBaseline' -benchmem -benchtime=1s -count=5 ./secret/` to refresh -->
# Benchmarks — `internal/service/secret`

These numbers answer the questions ADR 0142 is asked about subject keys:

> **What does sealing under a subject's key cost over a bare key, and what
> does a rotation of the root cost per subject?**

A cached subject key costs one cache lookup and one key copy over the
AES-256-GCM seal underneath. A subject not cached costs one read of each store
and three HKDF derivations, once per `CacheTTL`. A rotation costs one unwrap,
one wrap and one compare-and-swap per subject, and never touches a box.

## Reproducibility envelope

> **Numbers vary across machines, and across runs on this one.** The machine
> was shared with concurrent builds while the suite ran, so each figure below
> is the MEDIAN of five runs, with the fastest beside it. The *shape* is what
> travels: a cached key is within a few hundred nanoseconds of a bare key, and
> a re-wrap is linear in the subjects.

| Dimension | Value |
|---|---|
| CPU cores          | 10 (Apple M1 Pro) |
| RAM                | 16 GiB |
| OS / kernel        | macOS 26.6.2 (Darwin 25.6.0) |
| Architecture       | arm64 |
| Go toolchain       | go1.27.1 darwin/arm64 |
| Git branch         | `feat/subject-keys` |
| Git commit         | `12539d5` (pre-commit) |
| Generated (UTC)    | 2026-09-27 |
| Bench wall-clock   | `-test.benchtime=1s -test.count=5`, 57 s total |

Every store is in memory: the figures are the engine's own cost. A store that
persists adds its own read to a miss and its own durable write to each
re-wrapped key.

## Results

Median of five runs; `ns/key` is the pass divided by the subjects it moved.

```
BenchmarkSubjectKeysSeal/cache=1024-10          1136 ns/op     1664 B/op      9 allocs/op   (fastest  984)
BenchmarkSubjectKeysSeal/cache=0-10             6473 ns/op     7648 B/op     76 allocs/op   (fastest 4583)
BenchmarkSubjectKeysOpen/cache=1024-10           675 ns/op     1496 B/op      7 allocs/op   (fastest  631)
BenchmarkSubjectKeysOpen/cache=0-10             7689 ns/op     7482 B/op     74 allocs/op   (fastest 4265)
BenchmarkCryptoSealBaseline-10                   773 ns/op     1448 B/op      6 allocs/op   (fastest  765)
BenchmarkSubjectKeysRewrap/subjects=100-10        1828 ns/key    331106 B/op      1661 allocs/op
BenchmarkSubjectKeysRewrap/subjects=10000-10      2458 ns/key  33281382 B/op    160072 allocs/op
BenchmarkSubjectKeysOldestRoot-10                  295 ns/key   1867583 B/op     10027 allocs/op   (10 000 subjects)
```

## What the numbers say

### A cached subject key is nearly a bare key

| 64-byte value | cached | bare AES-256-GCM seal |
|---|---|---|
| seal | 1 136 ns, 9 allocs | 773 ns, 6 allocs |
| open | 675 ns, 7 allocs | — |

The three allocations a subject seal adds over the bare seal are the header,
the associated data built from the binding parts, and the COPY of the cached
key each call takes and zeroizes — the copy is what keeps a concurrent
eviction from zeroing a key a seal is using (ADR 0142 §7), and it costs one
32-byte allocation.

### A miss costs one read of each store and three derivations

Without a cache, every call reads the key store, reads the root's versions,
derives the root's wrap key, unwraps, and derives the subject's AEAD key and
identifier: about 7 µs and 75 allocations here, over two stores in memory. A
cache turns that into a once-per-`CacheTTL` cost per subject, which is why the
cache exists — and why its TTL, which bounds a cross-process erasure, is the
caller's to choose rather than a default.

### A rotation is linear in the subjects, and the store sets its price

`Rewrap` moves 100 keys at 1.8 µs each and 10 000 at 2.5 µs each: the memory
store's snapshot of every key before the pass is the difference. A million
subjects cost the engine about 2.5 s. A persistent store adds one durable write
per key: on a document store on disk that is `WriteAtomic`'s two flushes, about
11 ms on this device (`internal/service/docstore/BENCH.md`), so ten thousand
subjects take about two minutes and a million about three hours — in the
background, while `RotatorConfig.InUse` keeps the old root version until the
pass is done.

`OldestRoot`, the question a rotation asks before it prunes, reads headers
only: 0.3 µs per subject, a third of a second for a million.
