# internal/service/cache/

## Purpose

The concrete cache stores implementing `internal/core/cache` (ADR 0049):

- **`NewMemory[V]`** — the kernel LRU + TTL primitive, a two-way tag index
  beside it, and a `kernel/singleflight` group in front of the fill path.
  Implements `Store` + `EntryFetcher` + `Tagger` + `Loader`.
- **`NewChain[V]`** — puts one store in front of another (L1/L2), promoting a
  far hit into every nearer tier with its **remaining** TTL and its tags.

Stdlib-only, cross-OS. Code range `0.3.48.*` (plus core sentinels `0.2.18.*`).

## Contents

| File | Surface |
|---|---|
| `config.go` | `MemoryConfig` + its ADR 0031 validation |
| `chain_config.go` | `ChainConfig` — the `OnPromoteError` hook and why it should be wired |
| `memory.go` | `NewMemory` + `memoryStore[V]` — the store, its lock discipline, and `Load` |
| `record.go` | `record[V]` — what the primitive actually stores, and why `expireAt` is duplicated |
| `tagindex.go` | `tagIndex` — `byKey` / `byTag`, and why both exist |
| `chain.go` | `NewChain` + `chainStore[V]` — promotion, write order, delete order |
| `tier.go` | `tier[V]` — one level, with its three capabilities resolved once |
| `refuse.go` | `validateEntry` / `primitiveTTL` / the `CACHE_ENTRY_REJECTED` and fill-error wrapping |
| `cache_compliance.go` | the compile-time assertions that both stores still answer all four contracts |
| `codes.go` / `errors.go` | `0.3.48.*` — CACHE_CHAIN_MISCONFIGURED, CACHE_TIER_FAILED |

## One process, and only one

**`Loader.Load` protects an origin from ONE process, not from a fleet.**

Ten replicas of a service each running this still send ten concurrent requests
to the origin the instant a hot key expires. The saving is the concurrency
factor *inside* an instance — which is real and often large — but it is not "my
origin sees one request", and a reader who takes it that way will size the
origin wrong by a factor of N.

Cross-process collapsing needs a shared lease or lock, which is a distributed
system with its own failure modes (a lease holder that dies mid-fill, a clock
skew that lets two holders coexist). This package does not have one and does
not pretend to. It is stated here, in the port's doc comment, in the `pkg/v1`
package documentation and in ADR 0049, because a limit that only appears in one
of four places is a limit most readers will miss.

## The lock discipline, and why `onEvict` must never lock

One `sync.Mutex` covers **both** the primitive and the tag index, and `Fetch`
takes it exclusively.

That is not an oversight. The primitive's own `Fetch` already takes a *write*
lock, because a hit promotes the entry and moves counters — there was never a
shared-read path to give up. Putting the tag index under the **same** lock is
what makes the index and the entries agree at every observable moment. With two
locks, a key would be briefly live but unindexed, and an `InvalidateTag`
crossing that window would report success while leaving the entry fetchable.

The primitive calls `OnEvict` after releasing its own lock but still on the
**caller's goroutine** — the goroutine that is inside one of our methods,
holding `s.mu`. A callback taking `s.mu` would deadlock outright. So:

> **`onEvict` takes no lock. It appends to `s.evicted`, a field reachable only
> while `s.mu` is held, and the surrounding method drains it.**

`TestEvictionUnderTheStoreLockDoesNotDeadlock` provokes it. If the invariant
ever breaks, that test does not fail — it **hangs**, and the `go test` timeout
names it. A deadlock cannot be asserted against, only provoked.

## Four removal paths, one invariant

The reverse index must be unlinked on **every** path that drops an entry, and
they do not share a mechanism:

| Path | Unlinked by |
|---|---|
| capacity eviction | `OnEvict` → `drainEvicted` |
| TTL expiry (lazy, on `Fetch`) | `OnEvict` → `drainEvicted` |
| overwrite (`Set` on an existing key) | `tagIndex.add` calls `remove` first |
| explicit `Delete` / `InvalidateTag` | direct `tagIndex.remove` |

The fourth is the trap: the primitive **deliberately does not fire `OnEvict`
for an explicit `Delete`**, so a store relying only on the callback would leak
every deleted key's tags. `TestTagIndexNeverNamesAKeyTheStoreNoLongerHolds`
runs a workload that exercises all four and then asserts index ⊆ store.

## What the tag index costs, measured

`byTag` answers "which keys carry this tag" in one map lookup. `byKey` exists
only to make removal O(T) in *that key's* tags rather than O(all tags).

`BENCH.md` measures both claims rather than estimating them:

| | Measured |
|---|---|
| Invalidate 100 entries in a **1 000**-entry store | 31.2 µs |
| Invalidate 100 entries in a **100 000**-entry store | 36.2 µs |
| `Set` with one tag, vs untagged | +760 ns, +17 B, +1 alloc |
| Each additional tag | ≈ +390 ns |

A 100× larger store costs **1.16×**, not 100×. A scanning implementation would
be near 3 ms. The residual 16 % is memory locality, not work. Tag *text* is not
duplicated between the two maps — Go strings are immutable, so both hold
headers over one backing array.

`removed` counts entries **dropped from the store**. An entry whose TTL had
passed but which nothing had touched is counted: expiry is lazy, such an entry
still occupies capacity, and removing it is a removal.

## The chain: nothing is atomic, and the ordering says which side is protected

A chain is two independent stores with no transaction between them. Every
multi-tier operation has a window in which they disagree. The choice is not
whether the window exists but which side of it is safer:

| Operation | Order | Because |
|---|---|---|
| `Set` | far → near, stop on failure | a near tier holding a value the far tier refused is a lie that outlives the error |
| `Delete` | far → near, **attempt every tier** | near-first is strictly worse: a failed far delete would let the next read miss near, hit far, and **promote the value back** — undoing the delete permanently. Far-first bounds the damage to one near TTL |
| `Fetch` | near → far, promote on a far hit | a broken tier **stops the walk**: continuing would serve a staler answer from behind it and call that a hit |
| promotion failure | reported, never fatal | the caller already has the value; failing the read turns a degraded cache into a degraded service |
| `InvalidateTag` | far → near, attempt every tier | a near tier cleared first would repopulate itself from the far tier on the very next read |

`InvalidateTag`'s total counts **removals, not distinct keys** — an entry in
two tiers counts twice. Deduplicating would need each tier to return the keys
it dropped, which no tier does; a number whose unit is stated beats one that is
quietly approximate.

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
move, so a popular entry would become immortal.

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

Error **fields** carry the key (log-only; ADR 0005 §4 keeps `Fields` out of
`Error()` and off the wire). A caller who puts a secret in a cache key has put
that secret in their logs. Stated here because it is the kind of thing nobody
discovers until an incident.

## Do NOT

- Give `onEvict` a lock, or call into the primitive without `s.mu`.
- Cache a failed fill. One bad minute at the origin would become a full TTL of
  confident wrong answers, and `TestAFailedFillStoresNothing` pins it.
- Relabel a typed fill error. ADR 0005 §origin wins: an error already carrying
  a dotted quad keeps it, and `TestATypedFillErrorKeepsItsOwnCode` pins that.
- Add a Redis or memcached tier here. Distributed backends are exception 4 of
  the third-party doctrine and belong under `third-party/` — a separate change
  with its own dependency budget.
- Promise cross-replica stampede protection. See §"One process, and only one".

## Verification

```
cd internal/service && GOWORK=off go test -race ./cache/
bazel test --config=race //internal/service/cache:cache_test
```
