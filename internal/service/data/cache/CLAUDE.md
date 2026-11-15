<!-- updated: 2026-11-15T12:00:00Z -->
# internal/service/data/cache/

## Purpose

The concrete cache stores implementing `internal/core/data/cache` (ADR 0049):

- **`NewMemory[V]`** — the kernel LRU + TTL primitive (its `UnlockedMap`,
  under the stripe's one lock — #1059), a two-way tag index beside it, and a
  `kernel/concur/singleflight` group in front of the fill path.
  Implements `Store` + `EntryFetcher` + `Tagger` + `Loader`.
- **`NewChain[V]`** — puts one store in front of another (L1/L2), promoting a
  far hit into every nearer tier with its **remaining** TTL and its tags.

Stdlib-only, cross-OS. Code range `0.3.48.*` — `CACHE_CHAIN_MISCONFIGURED` and
`CACHE_TIER_FAILED`, which the chain emits, and `CACHE_LEASE_FAILED`, which
both stores' `LoadLeased` emits (#830) — declared since ADR 0160 with the
port's `0.2.18.*` in `internal/core/data/cache`, under the values this package
allocated. This package declares no code.

## Contents

| File | Surface |
|---|---|
| `config.go` | `MemoryConfig` + its ADR 0031 validation |
| `chain_config.go` | `ChainConfig` — the `OnPromoteError` hook and why it should be wired; the `Clock` and `LeasePoll` `LoadLeased` waits on (#830) |
| `lease.go` | `LoadLeased` on both stores — the cross-process lease from a `lock.Locker`, the waiting loop, `CACHE_LEASE_FAILED` (#830) |
| `memory.go` | `NewMemory` / `NewWeighedMemory` + `memoryStore[V]` and its `memoryStripe[V]`s — the store, its lock discipline, `Expire`, and `Load` with negative entries |
| `memory_bulk.go` | `FetchMany` / `SetMany` on the memory store — one lock per stripe per batch; `FetchMany` answers by position in the caller's slices, through one batched read of the primitive, with no map and, up to 64 keys, nothing allocated (#840) |
| `memory_refresh.go` | `LoadRefreshing` on the memory store — refresh-ahead, one background fill per key (#838) |
| `chain_bulk.go` | `Expire`, `FetchMany` (one pass per tier over the positions still missing, a repeated key read once — `firstOf`), `SetMany` and `LoadRefreshing` on the chain (#1059, #840, #838) |
| `record.go` | `record[V]` — what the primitive actually stores: the value, the tags and the negative entry's `absent`, seven words for a string value; the deadline and the weight are the primitive's (#1059) |
| `tagindex.go` | `tagIndex` — `byKey` / `byTag`, and why both exist |
| `chain.go` | `NewChain` + `chainStore[V]` — promotion, write order, delete order, and the guard that keeps a promotion from undoing a write (`announce` / `finish`, #460) |
| `tier.go` | `tier[V]` — one level, with its three capabilities resolved once |
| `refuse.go` | `validateEntry` / `primitiveTTL` / the `CACHE_ENTRY_REJECTED` and fill-error wrapping |
| `cache_compliance.go` | the compile-time assertions that both stores still answer all nine contracts |

## The lock discipline, and why `onEvict` must never lock

One `sync.Mutex` covers **both** the primitive and the tag index, and `Fetch`
takes it exclusively.

That is not an oversight. A hit promotes the entry and moves counters — there
was never a shared-read path to give up. Putting the tag index under the
**same** lock is what makes the index and the entries agree at every
observable moment. With two locks, a key would be briefly live but unindexed,
and an `InvalidateTag` crossing that window would report success while
leaving the entry fetchable.

It is the **only** lock (#1059). The primitive is the kernel's `UnlockedMap`,
the cache without its lock: the stripe's mutex already serialises every call
into it, and the locked `Cache` it replaced paid a second mutex, two deferred
calls and an eviction list on every hit for nothing.

The primitive calls `OnEvict` at the end of the call that evicted, once its
map is consistent, on the **caller's goroutine** — the goroutine that is inside
one of our methods, holding `s.mu`. A callback taking `s.mu` would deadlock
outright. So:

> **`onEvict` takes no lock. It unlinks the key from the stripe's tag index,
> which the mutex the caller holds already guards.**

`TestEvictionUnderTheStoreLockDoesNotDeadlock` provokes it. If the invariant
ever breaks, that test does not fail — it **hangs**, and the `go test` timeout
names it. A deadlock cannot be asserted against, only provoked.

## Four removal paths, one invariant

The reverse index must be unlinked on **every** path that drops an entry, and
they do not share a mechanism:

| Path | Unlinked by |
|---|---|
| capacity eviction | `OnEvict` → `onEvict` |
| TTL expiry (lazy, on `Fetch`, or on `FetchMany` at the end of its batch — #840) | `OnEvict` → `onEvict` |
| TTL expiry swept by `Expire` (#1059) | `OnEvict` → `onEvict` |
| overwrite (`Set` on an existing key) | `tagIndex.add` calls `remove` first |
| explicit `Delete` / `InvalidateTag` | direct `tagIndex.remove` |

The fourth is the trap: the primitive **deliberately does not fire `OnEvict`
for an explicit `Delete`**, so a store relying only on the callback would leak
every deleted key's tags. `TestTagIndexNeverNamesAKeyTheStoreNoLongerHolds`
runs a workload that exercises all four — its lazy expiry through `Fetch` and
through `FetchMany` alternately — and then asserts index ⊆ store.

## Why every tier must implement `EntryFetcher` and `Tagger`

Checked once, in `NewChain`, and refused with `CACHE_CHAIN_MISCONFIGURED`
rather than discovered per call.

Promoting a value into a nearer tier **without its tags** produces a copy
`InvalidateTag` can no longer reach: the invalidation reports success, the far
tier forgets the entry, and the near tier keeps serving it until its TTL runs
out. A tag invalidation that silently misses one copy is worse than one that
was never offered — so the chain refuses to be built rather than degrade at the
moment nobody is looking. `FetchEntry` also reports the **remaining** TTL, not
the original: promoting with the original would refresh the entry on every
move, so a popular entry would become immortal. What is left is the
primitive's deadline (`UnlockedMap.Deadline`), read under the lock that found
the entry, against the store's clock (#1059).

## ADR 0031 — where this domain clamps and where it refuses

**Refuses**, all of them:

- `MaxEntries <= 0` — a capacity is the whole statement of how much memory the
  caller will spend, so any number the SDK picked would be arbitrary; and the
  reading a zero would naturally get (unbounded, from the primitive) is the
  dangerous one, doubly so because an unbounded store grows an unbounded tag
  index.
- `DefaultTTL < 0` — the primitive would read it as "no expiry", so accepting
  it stores entries that never leave.
- empty key, empty tag, negative per-entry TTL other than `NoExpiry` — each
  produces an entry the caller can never deliberately fetch, invalidate or
  replace.
- a chain of fewer than two tiers, a nil tier, an incomplete tier.

**Clamps**: nothing. There is no knob here whose floor the SDK can supply
without inventing the caller's requirement.

## Keys reach the log

Error **fields** carry the key (log-only; ADR 0002 §Semantics keeps `Fields` out of
`Error()` and off the wire). A caller who puts a secret in a cache key has put
that secret in their logs. Stated here because it is the kind of thing nobody
discovers until an incident.

## Do NOT

- Give `onEvict` a lock, or call into the primitive without `s.mu`.
- Cache a failed fill. One bad minute at the origin would become a full TTL of
  confident wrong answers, and `TestAFailedFillStoresNothing` pins it.
- Relabel a typed fill error. ADR 0005 §origin wins: an error already carrying
  a dotted quad keeps it, and `TestATypedFillErrorKeepsItsOwnCode` pins that.
- Reduce an UNTYPED fill error to a string. It is labelled `CACHE_FILL_FAILED`
  with `errs.Wrap(cause, WrapParams{…})`, so the caller's
  `errors.Is(err, sql.ErrNoRows)` still answers beside `errs.HasCode`;
  `TestAnUntypedFillErrorKeepsItsIdentity` pins both, through the memory store
  and the chain.
- Reduce an UNTYPED tier error to a string either. `tierFailure` labels it
  `CACHE_TIER_FAILED` with the cause IN the chain, so a backend's own sentinel
  still answers `errors.Is` (`TestAnUntypedTierErrorKeepsItsIdentity`). A TYPED
  tier error keeps `CACHE_TIER_FAILED` as the code and travels as fields
  (`cause`, `cause_code`), because wrapping it would hand the code to the tier
  under origin-wins and lose the position, which is the one thing the caller
  cannot reconstruct. "Typed" means `errors.AsType` finds an `*errs.Error`,
  the test `Wrap` itself makes — a direct assertion missed one behind
  `fmt.Errorf`'s `%w` (`TestATypedTierErrorBehindAWrapperKeepsTheTierVerdict`).
- Add a Redis or memcached tier here. Distributed backends are exception 4 of
  the third-party doctrine and belong under `third-party/` — a separate change
  with its own dependency budget.
- Promise cross-replica stampede protection. See §"One process, and only one".
- Answer `FetchMany` through a map, or read a key twice in one batch (#840):
  it answers by position in the caller's slices, through one
  `UnlockedMap.FetchMany` a stripe, which records each hit as `Fetch` does
  under every policy. `TestFetchManyAllocatesNothing` (alloc lane) holds 16
  and 64 keys to zero allocations.

## Verification

```
cd internal/service && GOWORK=off go test -race ./data/cache/
bazel test --config=race //internal/service/data/cache:cache_test
```

## Reference in NOTES.md

Moved unchanged to `internal/service/data/cache/NOTES.md`, under the same titles, and read only by a change that needs its mechanics: `One process, and only one`; ``LoadLeased` takes the lease, from the lock domain (#830)`; `What the tag index costs, measured`; `The chain: nothing is atomic, and the ordering says which side is p...`; `A promotion never writes back what a write replaced (#460)`; `Stripes, bytes, policy (#808, #839, #834)`; `Sweeps, batches, refresh-ahead, negative entries (#1059, #840, #838)`; `What a hit costs, and why it is one call deep (#1059)`; `What a batch costs, and why it answers by position (#840)`.
