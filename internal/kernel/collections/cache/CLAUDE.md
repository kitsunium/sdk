<!-- updated: 2026-09-28T19:19:15Z -->
# internal/kernel/collections/cache/

## Purpose

Generic, concurrency-safe **LRU (or S3-FIFO) + TTL cache** `Cache[K comparable, V any]` — a
kernel primitive (stdlib-only AND generic, no domain vocabulary). Admitted by
**ADR 0025**: any domain (HTTP middleware, DNS resolver, codec scratch, config
hot-reload) reaches for an LRU/TTL cache, so it belongs in the kernel alongside
`ring`/`recycler`/`snapshot`. Reuses the kernel `clock` so TTL expiry is
testable. Emits **no error codes** (`Fetch` returns `(V, bool)`).

## Contents

| File | Surface |
|---|---|
| `cache.go` | `Cache[K,V]` + `NewCache` + `Fetch`/`Set`/`SetTTL`/`Delete`/`Expire`/`Keys`/`Len`/`Purge`/`Stats`, the expiry heap and the eviction loop |
| `entry.go` | `entry[K,V]` — intrusive list node, with its weight, its expiry handle and its S3-FIFO counter |
| `list.go` | `entryList[K,V]` — the intrusive doubly-linked list each queue is, with its count and summed weight |
| `s3fifo.go` | admission, the hit record and the victim walk of both policies (`admit`, `touch`, `evictOne`, the S3-FIFO small/main walk) |
| `ghost.go` | `ghostSet[K]` — S3-FIFO's ghost: a bounded FIFO of key hashes (`hash/maphash.Comparable`) |
| `policy.go` | `Policy` — `PolicyLRU` (the zero) / `PolicyS3FIFO` |
| `striped.go` | `Striped[K,V]` + `NewStriped` — N independent Caches behind one hash, each with its own lock (#808) |
| `eviction.go` | `evictionValue[K,V]` — an evicted key/value pair, copied out of the locked section for `OnEvict` |
| `config.go` | `Config[K,V]` — MaxEntries / DefaultTTL / Clock / OnEvict / MaxWeight / WeightOf / Policy |
| `stats.go` | `StatsValue` — Hits / Misses / Evictions / Weight / Policy |

## Conventions

- **`Fetch`, not `Get`**: a hit mutates state (LRU promotion or the S3-FIFO
  counter, plus the hit counter), so the read verb is `Fetch` — `Get` is
  reserved for pure accessors (KTN-GETTER-PURE).
- **`sync.RWMutex`**: read-only `Len`/`Stats` take `RLock`; everything else `Lock`.
- **Expiry: lazy, plus a sweep the CALLER drives** (#1038). An expired entry is
  reaped by the next `Fetch` of its key, by a capacity eviction, or by
  `Expire()`. Every entry carrying a deadline sits in an expiry heap
  (`kernel/collections/heap.IndexedHeap`, #1036) keyed by `expireAt`: `SetTTL`
  re-keys its node, `Delete` and every eviction remove it, so `Expire` pops
  exactly the k entries due — O(k log n), the clock read once, nothing read for
  a cache with no deadline. The primitive starts no goroutine (ADR 0025 and
  ADR 0049 refused one inside it): the caller sweeps on its own cadence.
- **A dead entry goes before a live one** (#1059). When a bound is exceeded the
  eviction loop first looks at the heap's top: an entry whose deadline has
  passed is evicted before the policy's live victim, so a corpse never costs a
  live entry its slot.
- **Two bounds, both optional** (#839). `MaxEntries` counts entries;
  `MaxWeight` sums `WeightOf(key, val)` (nil = 1 per entry, so it is then a
  count). Either exceeded evicts. An entry heavier than `MaxWeight` ALONE is
  stored and evicts every other entry — the kernel has no error to refuse it
  with; the domain store refuses it (`CACHE_ENTRY_REJECTED`). An overwrite
  re-weighs and may evict others; it never evicts the entry it wrote.
  `StatsValue.Weight` is 0 for a cache configured without weights, so a
  count-bounded cache's stats are what they always were.
- **Two policies** (#834). `PolicyLRU` (the zero, today's behaviour) moves a hit
  to the front and evicts the tail. `PolicyS3FIFO` (Yang et al., SOSP 2023)
  admits a new key into a small FIFO holding a tenth of the bound; at the small
  tail an entry hit since admission moves to main with its counter cleared, one
  never hit is evicted and its key's hash remembered by a ghost FIFO bounded
  like main; a key the ghost remembers is admitted straight into main; main is
  a clock — a tail with hits left goes back to the head with one fewer (the
  counter saturates at 3). A hit moves nothing. An unknown `Policy` value runs
  as LRU and `Stats().Policy` says which policy runs — the kernel has no error
  surface (rule 1). The entry a `Set` just wrote is never its own victim, under
  either policy. Every behavioural table of `cache_external_test.go` runs under
  both policies (`forEachPolicy`); `BENCH.md` measures the hit ratio of each on
  Zipf traces and through scans.
- **Striping is a sibling** (#808). `Striped[K,V]` holds N Caches, picks one by
  `hash(key) % N` (nil hash = `maphash.Comparable` over a per-`Striped` seed),
  and divides `MaxEntries`/`MaxWeight` between them (remainder to the first
  stripes, at least one unit each when the bound is set). The policy is exact
  within a stripe, approximate across them; with one stripe it is one Cache.
  `Len`/`Stats`/`Expire`/`Purge`/`Keys` walk the stripes one lock at a time — never
  two locks at once.
- **`Keys` yields a snapshot** (#816). The keys present and unexpired are
  copied under the read lock and yielded after it is released — the rule
  OnEvict keeps: no caller code runs under `mu` — so the loop's body may
  Set, Delete or Fetch. A walk is not a read: nothing is promoted, no counter
  moves. One slice per loop (`BenchmarkKeys_1024`: 1 alloc, ~34 µs).
- **OnEvict** fires on capacity/expiry removal only — never on `Delete`/overwrite/`Purge`.
  It runs after the mutex is released, so the callback may call back into the cache.
- **Injectable clock**: `Config.Clock` (nil → `clock.System`) makes TTL
  deterministic in tests. A `clock.CoarseClock` (#814) makes a TTL'd hit's
  clock read an atomic load, at the price of expiry late by up to its
  resolution — the caller's trade, not the primitive's.
- Cross-OS: 100 % portable (map/sync/time/hash/maphash) — no OS-specific code.

## Do NOT

- Rename `Fetch` to `Get` — the side effect makes a pure getter a lie.
- Add error codes — the cache has no failure surface.
- Start a goroutine in the primitive — `Expire` is the caller's to schedule.

## Verification

```
bazel test --config=race //internal/kernel/collections/cache:cache_test
```
