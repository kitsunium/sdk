# internal/service/data/cache/ — reference

Moved unchanged from `internal/service/data/cache/CLAUDE.md` (ADR 0253); each section keeps its title.

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
- **`BulkFetcher` / `BulkSetter`.** The memory store groups the keys by stripe
  and locks each stripe once per batch. `SetMany` validates every entry before
  storing any (all or nothing on one store). The chain's `FetchMany` makes ONE
  pass per tier over the keys every nearer tier missed, and promotes a tier's
  hits into the nearer tiers as a batch (through a tier's `BulkSetter` when it
  has one) under the same `announce`/generation guard as a single promotion
  (#460); `SetMany` writes far to near after validating every entry.
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
