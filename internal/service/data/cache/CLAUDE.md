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

## One process, and only one

**`Loader.Load` protects an origin from ONE process, not from a fleet.**

Ten replicas of a service each running this still send ten concurrent requests
to the origin the instant a hot key expires. The saving is the concurrency
factor *inside* an instance — which is real and often large — but it is not "my
origin sees one request", and a reader who takes it that way will size the
origin wrong by a factor of N.

Cross-process collapsing needs a shared lease or lock, which is a distributed
system with its own failure modes (a lease holder that dies mid-fill, a clock
skew that lets two holders coexist). `Load` does not take one and does not
pretend to. It is stated here, in the port's doc comment, in the `pkg/v1`
package documentation and in ADR 0049, because a limit that only appears in one
of four places is a limit most readers will miss.

### `LoadLeased` takes the lease, from the lock domain (#830)

`corecache.Leaser` is the sibling that crosses the boundary, on BOTH stores
(`lease.go`). On a miss, after the in-process collapse, it takes a lease named
`"cache:" + key` from a `lock.Locker` the processes share — the lock domain's
file locker on one machine, whose `flock(2)` dies with the process holding it.
The holder reads the store again, fills, stores and releases (on a context
that cannot be cancelled: a lease left behind keeps every other process
waiting until it dies). A process that finds the lease held fills NOTHING: it
reads the store again every `LeasePoll` (25 ms by default, the file locker's
own) on the store's clock, and tries the lease again, until the holder's value
lands, its own ctx ends, or the lease comes free without a value — the holder
failed, was killed, or let a lease that can expire (`lock.Deadliner`) lapse —
when it takes it and reads once more before filling.

What the waiting process gets is decided by what the processes SHARE, and is
said wherever the sibling is:

- **A chain whose far tier every process reaches** answers the waiter with the
  holder's value: the origin sees one fill. Proved across a real process
  boundary by `TestTwoProcessesOverOneFileLockerFillOnce` (a child re-executing
  the test binary, a directory tier both share, a fills file both append to),
  and the SIGKILLed holder by `TestAKilledLeaseHolderLetsTheWaiterFillOnce`.
  Both skip, after asserting the refusal, where the lock domain's file locker
  refuses the platform (`proc.UnsupportedPlatform`: solaris, illumos).
- **A memory store alone** shares no value: the lease only takes TURNS, one
  fill at a time, each process filling its own copy once the holder is done
  (`TestAMemoryStoreAloneTakesTurns`). That still spares the origin a burst —
  N processes fill one after another, never together — but it is not one fill.
- **A lease that can expire is not renewed** during the fill: its lifetime must
  outlast the fill, or a second process fills beside the first.
- A nil locker makes `LoadLeased` `Load`; a locker that cannot answer is
  `CACHE_LEASE_FAILED` (`0.3.48.3`), its own error kept on the trail. `Load`
  and `LoadLeased` on one key share the in-process collapse: a `LoadLeased`
  joining a `Load` already filling takes no lease.
- The memory store waits on `MemoryConfig.Clock` when it can wait and on
  `clock.System` otherwise (a `clock.CoarseClock` only reads time); the chain
  on `ChainConfig.Clock`. The config's `Clock` type is NOT widened to
  `clock.Timed`: that would break every caller passing a reading-only clock.

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

### A promotion never writes back what a write replaced (#460)

A promotion copies into the nearer tiers an entry it read from a farther one
**before** it writes it. A `Set`, `Delete` or `InvalidateTag` that ran to
completion in between would see its effect overwritten by that older copy —
served for the near TTL, forever under `NoExpiry`. So every write
**announces itself** before it touches a tier (`announce`): it counts itself
in `writing`, then moves `generation` under `promoting`'s write lock, which
waits out every promotion already past its check; `finish` moves
`generation` again when the write ends. A promotion reads `generation` before
its walk and, under `promoting`'s read lock, writes only when no write is
running and none began or ended since — so a write already running when the
walk began, and finished before the check, is caught too. A skipped promotion costs one
later fall-through, never a stale hit; with no write around it, it promotes as
before. `chain_race_external_test.go` places each write inside the far read of
a `Fetch` and of a `Load`, on two and three tiers, with gated tiers — no sleep.

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

## Stripes, bytes, policy (#808, #839, #834)

- **Stripes.** `MemoryConfig.Stripes` (0 = 1; held at `MaxEntries` and at 1024;
  negative refused) splits the store into `memoryStripe`s, each a primitive, a
  tag index and the mutex over both — the lock discipline above holds per
  stripe, because a key lives in exactly one (`maphash.String` over a
  per-store seed); an unstriped store keeps its one stripe in `single` and
  hashes nothing. `MaxEntries` and `MaxBytes` are
  divided between the stripes, the remainder to the first ones, so the bounds
  are exact in total and the eviction order exact within a stripe only.
  `InvalidateTag` and `Expire` walk the stripes one lock at a time; `Load`'s
  singleflight group stays one per store. `TestTagIndexNeverNamesAKeyTheStoreNoLongerHolds`
  and `TestEvictionUnderTheStoreLockDoesNotDeadlock` run at 1 and 8 stripes.
- **Bytes.** `MemoryConfig.MaxBytes` needs a weigher, and `MemoryConfig` is not
  generic, so the weigher comes through `NewWeighedMemory[V](cfg, weigh func(V) int64)`;
  `NewMemory` refuses a positive `MaxBytes` (`CACHE_MISCONFIGURED`), and so does
  `NewWeighedMemory` a nil weigher or a non-positive bound — two half-stated
  bounds. The weigher runs once per write, in `validate`; the stripe hands the
  weight to the primitive through `writing`, the weight of the write in
  progress, which the primitive's `WeightOf` reads on the call that stores the
  entry (#1059) — the record carries no copy
  (`TestThePrimitiveWeighsWhatValidateMeasured`). An entry heavier than one
  stripe's share of `MaxBytes` is refused with `CACHE_ENTRY_REJECTED` — the
  primitive would store it alone and overrun the bound.
- **Policy.** `MemoryConfig.Policy` is forwarded to every stripe's primitive
  (`kcache.PolicyLRU`, the zero, or `PolicyS3FIFO`). Every external table of
  this package runs under both (`forEachPolicy`; `newStore` and `policyConfig`
  read the policy off the subtest's name).
- **Clock.** A `clock.CoarseClock` (#814) as `MemoryConfig.Clock` makes a TTL'd
  hit's clock read an atomic load; every expiry is then late by up to its
  resolution.

## Sweeps, batches, refresh-ahead, negative entries (#1059, #840, #838)

- **`Expirer`.** `Expire(ctx)` sweeps every stripe through the primitive's
  `Expire` (an expiry heap, O(k log n)); each removal comes back through
  `onEvict` and is unlinked from the tag index like an eviction. The store
  starts no goroutine: the caller schedules the sweep — the cadence ADR 0049
  left to the caller. The chain sweeps every tier implementing `Expirer`, far
  to near, and reports the total of removals, as `InvalidateTag` counts them.
  A full store also evicts a dead entry before a live one (the primitive's
  eviction loop tries the expiry heap's top first).
- **`BulkFetcher` / `BulkSetter`.** `FetchMany(ctx, keys, values, found)`
  appends one answer a key, in order, to the caller's slices — the value and
  true, or the zero value and false for a miss or a negative entry —, growing
  them only when their capacity is short (#840, below). `SetMany` validates
  every entry before storing any (all or nothing on one store), then locks each
  stripe once for its entries. The chain's `FetchMany` makes ONE pass per tier
  over the positions every nearer tier missed — a key the batch names twice
  asked once (`firstOf`) and answered at each of its positions —, and promotes
  a tier's hits into the nearer tiers as a batch (through a tier's
  `BulkSetter` when it has one) under the same `announce`/generation guard as
  a single promotion (#460); a broken tier stops the walk and appends nothing.
  `SetMany` writes far to near after validating every entry.
- **`Refresher`.** `LoadRefreshing(ctx, key, fill, before)`: a hit whose
  remaining lifetime is below `before` is returned at once and ONE background
  fill per key starts — claimed under the stripe's lock in `refreshing`, so N
  hits in the window start one — on `context.WithoutCancel(ctx)` bounded by the
  remaining lifetime (a real-time deadline: the store's clock only reads time).
  Its answer is stored as `Load` stores one; a plain failure leaves the old
  entry to expire. A miss is `Load`. `before <= 0` is refused
  (`CACHE_MISCONFIGURED`).
- **Negative entries.** A fill returning `corecache.Absent(cause, ttl)` (ttl > 0)
  makes the memory store keep a record with `absent` set, carrying the error the
  fill returned: `Load` and `LoadRefreshing` answer with it — `errors.Is` still
  finds the cause — without calling a fill, `Fetch`/`FetchEntry`/`FetchMany`
  report a miss, a `Set` replaces it, and it counts in capacity, tags, expiry
  and (one unit) bytes. The chain keeps no negative entry: a tier has no port to
  store one, so `Absent` passes through it.

## What a hit costs, and why it is one call deep (#1059)

The owner held `Fetch_Hit` and `Load_Hit` to 10 % of `ee1f48ba2`. The wave of
2026-11-02 had put them 15 to 29 % over it; `-gcflags=-m=2` and the
disassembly of `Fetch` at both commits named two causes, both removed here:

- **Two locks.** Every call into the primitive already held the stripe's
  mutex, and the locked `Cache` took its own on top, with two deferred calls
  and an eviction list. The stripe now holds the kernel's `UnlockedMap`; the
  stripe's mutex is the only one.
- **A record returned through memory.** The record had grown to eleven words
  (a `time.Time` deadline and an `int64` weight, both copies of what the
  primitive keeps), past the amd64 ABI's nine result registers, so every
  level — `Cache.Fetch`, the stripe, `fetchRecord` — copied it through the
  stack. It is seven words now, and `lookup` hands back only the value, the
  negative answer and the found flag: a hit comes back in registers.

Also on the path: an unstriped store reaches its stripe through `single`
without a hash (`stripeOf` inlines), and `UnlockedMap.Fetch` writes its lookup
out, so a hit is `Fetch` → `lookup` → `UnlockedMap.Fetch`. `Test_record_size`
holds the record to its words. The verdict is the interleaved A/B of
`bench-ab.yml` (rules `1059-fetch-hit`, `1059-load-hit`,
`1059-fetch-hit-allocs`); `scale_bench_test.go` adds `Fetch_Hit_1M` and
`Load_Hit_1M`, a million entries read in a random order, for its scale mode.

## What a batch costs, and why it answers by position (#840)

The owner held `FetchMany` of 16 keys to 40 % of `Fetch_Hit` a key. The batch
had paid a lock per stripe but everything else per key: the primitive's
`Fetch` (two calls, not inlined), a clock read for every key with a deadline,
and a result map — 1 240 B, four allocations, two more hashes a key and the
collection they bring. `BENCH.md` §#840 measures the designs; what stands:

- **Answers by position, in the caller's slices.** `FetchMany` appends to
  `values` and `found` (ADR 0049, amended 2026-11-15): a caller handing back the
  slices of its last batch, emptied, allocates nothing. The map was the
  biggest cost, and no other store implemented the sibling.
- **One batched read of the primitive.** `UnlockedMap.FetchMany` (ADR 0025,
  amended 2026-11-15) reads the clock once a batch, issues a chunk of sixteen
  lookups before it reads an entry, and calls nothing on a hit's path; it hands
  back references to its records, which `fetchInto` / `fetchAt` read under the
  stripe's lock that found them (`answerOf`). Up to `batchOnStack` (64) keys the
  references — and on a striped store the sort keys and the keys in stripe
  order — are stack arrays; past it they are allocated for the call.
- **A repeat is read once** — the port's promise kept without a map: the
  primitive marks an entry with the number of the batch that read it.
- **A striped store** sorts the positions by stripe (`stripeShift`, the stripe
  above a 54-bit position), keeps the caller's order within a stripe, and locks
  each stripe once; its stripe hash reads every key's bytes. An unstriped store
  reads each key's first byte before its lock (`warmKeys`): at scale the
  misses overlap instead of waiting one key at a time.

`TestFetchManyAllocatesNothing` (`fetchmany_alloc_external_test.go`,
`//go:build !race`, the alloc lane) holds a batch of 16 and of 64 keys to zero
allocations on one stripe and on eight. The verdict on the bound is the
interleaved A/B of `bench-ab.yml` (rules `840-fetchmany16-per-key` and, in the
scale mode, `840-fetchmany16-1m` over `FetchMany_16_1M`, which lives in
`siblings_scale_bench_test.go` so the base's binary keeps `scale_bench_test.go`).

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

## Verification

```
cd internal/service && GOWORK=off go test -race ./data/cache/
bazel test --config=race //internal/service/data/cache:cache_test
```
