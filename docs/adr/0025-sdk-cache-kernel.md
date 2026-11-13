# ADR 0025 — Generic LRU+TTL cache as a kernel primitive (`cache`)

- **Status**: Accepted — **amended by [ADR 0049](0049-cache-becomes-a-domain.md)**; amended 2026-11-02 (the L6 wave: an expiry sweep, weights, S3-FIFO, a striped sibling, a coarse clock); amended 2026-11-13 (#1038: a `sync.Mutex`, the `UnlockedMap` core, a 64-byte node)
- **Date**: 2026-06-24
- **Deciders**: SDK maintainers
- **Related**: ADR 0010 (recycler), ADR 0011 (snapshot), ADR 0006 (ring) — the kernel-primitive precedents; ADR 0024 (Phase-B wave)

> **Amendment (2026-09-09, [ADR 0049](0049-cache-becomes-a-domain.md)).** The
> placement decided below stands: `Cache[K,V]` remains a kernel primitive and
> nothing about it moved. ADR 0049 adds a DOMAIN *above* it —
> `internal/core/cache` (`Store[V]` + siblings, block `0.2.18.*`) and
> `internal/service/cache` (tagged memory store + L1/L2 chain, block
> `0.3.48.*`) — carrying the three things a primitive deliberately does not
> have: invalidation by tag, stampede protection, and tier chaining. It also
> notes, as a fact checked rather than assumed, that this package had **zero**
> in-tree consumers besides `pkg/v1/cache` between the two ADRs.

## Context

Phase B calls for a `cache`. Unlike `id`/`metrics`/`resilience`/`config` (which
carry domain vocabulary and become core siblings), a generic `Cache[K,V]` LRU +
TTL is **domain-neutral**: an HTTP middleware, a DNS resolver, codec scratch, or
hot-reloaded config all reach for the same mechanism. By the kernel gate
("stdlib-only AND generic, no domain vocabulary"), it belongs in
`internal/kernel`, alongside `ring`/`recycler`/`snapshot`.

## Decision

1. **Add `internal/kernel/cache`** — `Cache[K comparable, V any]` with
   `NewCache(Config)`, `Fetch`/`Set`/`SetTTL`/`Delete`/`Len`/`Purge`/`Stats`.
   LRU via an intrusive doubly-linked list; TTL via the injectable kernel
   `clock` (lazy expiry on `Fetch`). `sync.RWMutex` (read-only `Len`/`Stats`
   take `RLock`). `Config` carries `MaxEntries`/`DefaultTTL`/`Clock`/`OnEvict`.
2. **`Fetch`, not `Get`** — a hit mutates state (LRU promotion + counters), so
   the read verb is `Fetch`; `Get` is reserved for pure accessors (KTN-GETTER-PURE).
3. **No error codes** — the cache has no failure surface (`Fetch` returns
   `(V, bool)`; bad options clamp to sane defaults). It therefore imports no
   `errs` and needs no `audit_srcs`.
4. **`pkg/v1/cache`** re-exports via generic type aliases (`Cache[K,V] =
   kernel/cache.Cache[K,V]`, `Stats = StatsValue`) + a `New` wrapper.

## Consequences

- An 8th kernel package; the kernel inventory table + `internal/kernel/CLAUDE.md`
  audit ("who stays") gain a `cache` row. `pkg/v1` gains a dep-light `cache`
  facade (stdlib only). Docs updated per rule 11.
- `Cache` does NOT use `snapshot` (that is for read-mostly immutable maps; an LRU
  is mutable). It DOES reuse `clock`. The earlier "snapshot/recycler reuse"
  sketch was aspirational; the honest reuse is `clock`. Entry pooling via
  `recycler` is a deferred optimization (the `Fetch` hit path already allocates
  nothing).

## Alternatives considered

- **core sibling** — rejected: a generic cache has no domain vocabulary; the
  kernel gate places it with `ring`/`recycler`/`snapshot`.
- **`Get` verb** — rejected: KTN-GETTER-PURE forbids a mutating getter, and an
  LRU read mutates order; `Fetch` is honest.
- **recycler-backed entries in v1** — deferred: the hit path is already
  alloc-free; pooling helps only Set churn and adds clear-on-Put complexity.

## Deferred

- `recycler.Pool[*entry]` to cut Set-time allocation churn.
- A `snapshot`-backed read-mostly cache variant (frozen maps).
- Active TTL sweeper (`worker.Every`) instead of purely lazy expiry.

## References

- Impl: `internal/kernel/cache/`, `pkg/v1/cache/`.
- ADR 0010/0011/0006 (kernel primitives), ADR 0024 (Phase-B wave).

## Amendment — 2026-11-02: a sweep the caller drives, weights, a second policy, stripes, and a coarse clock ([#1038](https://github.com/kitsunium/sdk/issues/1038), [#1059](https://github.com/kitsunium/sdk/issues/1059), [#839](https://github.com/kitsunium/sdk/issues/839), [#834](https://github.com/kitsunium/sdk/issues/834), [#808](https://github.com/kitsunium/sdk/issues/808), [#814](https://github.com/kitsunium/sdk/issues/814))

The placement stands: `Cache[K,V]` is a kernel primitive, stdlib-only, with no
error surface and **no goroutine of its own**. Every addition below keeps
those three properties, and every one is opt-in — a `Config` that names none
of them builds the cache this ADR described, entry for entry.

- **`Expire() int` — the count reaped — resolves the deferred "active TTL sweeper"
  without a sweeper.** Expiry stayed lazy, and an idle key outlived its TTL
  for good (#1038): in a secret store, an opened key unwiped long after it
  should have been. The sweep is a method the CALLER drives on its own clock
  and cadence; the primitive still owns no cadence, which was the objection
  to `worker.Every` inside it. It runs over an expiry heap — the indexed
  sibling of `kernel/collections/heap` (#1036): `SetTTL` updates an entry's
  handle, `Delete` removes it — so a sweep costs O(k log N) for k due entries,
  reads the clock at most once, and costs nothing for an entry without a
  deadline. The capacity eviction looks at the heap's top before the LRU
  tail, so a dead entry never costs a live one its slot (#1059).
- **`MaxWeight` and `WeightOf` (#839)** bound what the entries weigh, beside
  `MaxEntries`; a nil `WeightOf` weighs every entry 1. An entry heavier than
  `MaxWeight` on its own is stored and evicts everything else — stated,
  because the primitive has no error to refuse it with; the domain store,
  which has one, refuses it.
- **`Policy` (#834)**: `PolicyLRU` is the zero and today's behaviour;
  `PolicyS3FIFO` keeps a hot set through a scan (small, main and ghost FIFO
  queues, the ghost a bounded set of `hash/maphash.Comparable` hashes). An
  unknown value runs as LRU and `Stats` says which policy runs. W-TinyLFU is
  left to its own issue, as #834 proposed.
- **`Striped[K,V]` (#808)** is the shard this ADR's BENCH told callers to
  build: N `Cache`s behind their own locks, the bounds divided between them,
  LRU exact within a stripe and approximate across them. The stripe count
  stays the caller's: it is a property of their key distribution.
- **`clock.NewCoarse` (#814)** is the coarse clock this package's BENCH
  "rejected rather than overlooked": it lives in `kernel/clock`, which owns
  its ticker, and a caller passes it as the cache's `Clock`. The trade —
  every expiry late by up to one resolution — is the caller's to make.

The measured price on the paths that use none of it is in `BENCH.md`: hits
unchanged within noise, a new key and an insert at capacity ~14 % and ~22 %
dearer (the node carries its heap handle, weight and policy counter). #1038
asked for 5 %; the gap is stated there, not hidden.

## Amendment — 2026-11-13: a `sync.Mutex`, the core without its lock, and a 64-byte node ([#1038](https://github.com/kitsunium/sdk/issues/1038))

The owner kept #1038's bound — `Set_NewKey`, `Fetch_Hit` and
`Fetch_HitWithTTL` within 5 % of `ee1f48ba2` — rather than the 14 % the wave
above stated. What the compiler made of the wave, read before any change
(`-gcflags=-m=2` and the disassembly of `setTTL` and `Fetch` at both commits):
the node had moved from the 64-byte size class to the 80-byte one
(`mallocgcSmallScanNoHeaderSC6` → `SC7`), and a new key's `setTTL` called
`admit`, `setDeadline` and `evictIfNeeded` where the base called
`evictIfNeeded` alone; a hit called `touch` and then `moveFront`. Three
decisions follow, none of which changes what a caller observes.

- **§1's `sync.RWMutex` becomes a `sync.Mutex`.** Every `Fetch` and `Set`
  takes the lock exclusively anyway; the read lock only let two `Len` or
  `Stats` calls overlap, and its write lock costs four atomic operations
  where a `Mutex` costs two, inlined.
- **`UnlockedMap[K,V]`, the cache without its lock.** `Cache` is now a `Mutex`
  around one; every mechanism — policy, deadlines, bounds, sweep, counters —
  is the `UnlockedMap`'s. A caller that already serialises under its own lock
  (the cache domain's stripes, #1059) uses it and pays one lock instead of
  two. It is not safe for concurrent use, like `heap.IndexedHeap`; its
  `OnEvict` runs at the end of the call, once the cache is consistent.
- **The node is 64 bytes again.** A deadline is an `int64` of nanoseconds
  since the cache's epoch, taken by `time.Time.Sub` — the monotonic readings
  when both carry one, as `Before` compared them — and saturating past the
  `int64` range; the expiry heap is the package's own, 4-ary, the deadline
  inline in each cell and each entry's 32-bit slot its position, so a sift
  compares two integers and calls nothing. `heap.IndexedHeap` stays for its
  other users. The insertion and the hit call only what their configuration
  needs, and an LRU hit relinks without rewriting the list's count and weight.

Recycling the nodes stays deferred, for the reason given above. The verdict
on the bound is the interleaved A/B of `bench-ab.yml` on the runner, the
upper end of the 95 % interval against 1.05; the figures go to `BENCH.md`.
