<!-- updated: 2026-11-15T12:00:00Z -->
# internal/kernel/collections/cache/

## Purpose

Generic, concurrency-safe **LRU (or S3-FIFO, or W-TinyLFU) + TTL cache** `Cache[K comparable, V any]` — a
kernel primitive (stdlib-only AND generic, no domain vocabulary). Admitted by
**ADR 0025**: any domain (HTTP middleware, DNS resolver, codec scratch, config
hot-reload) reaches for an LRU/TTL cache, so it belongs in the kernel alongside
`ring`/`recycler`/`snapshot`. Reuses the kernel `clock` so TTL expiry is
testable. Emits **no error codes** (`Fetch` returns `(V, bool)`).

## Contents

| File | Surface |
|---|---|
| `cache.go` | `Cache[K,V]` + `NewCache` + `Fetch`/`Set`/`SetTTL`/`Delete`/`Expire`/`Keys`/`Len`/`Purge`/`Stats` — a `sync.Mutex` around an `UnlockedMap`, OnEvict drained after the unlock |
| `unlocked.go` | `UnlockedMap[K,V]` + `NewUnlockedMap` — the cache without its lock (#1038, for #1059): the lookup, the write path, the deadlines, the sweep and the eviction loop every `Cache` call runs; `Deadline`, the read of an entry's deadline (#1059); `tinyLFUShares`, W-TinyLFU's segment bounds it holds
|
| `deadline.go` | `deadlineHeap[K,V]` — the expiry heap: 4-ary, `int64` deadlines inline, each entry's slot its cell plus one |
| `entry.go` | `entry[K,V]` — intrusive list node, with its deadline, its heap slot, its weight, its S3-FIFO counter, its W-TinyLFU period and sketch hash: 64 bytes for a string key and a word-sized value |
| `list.go` | `entryList[K,V]` — the intrusive doubly-linked list each queue is, with its count and summed weight |
| `policy.go` | `Policy` — `PolicyLRU` (the zero) / `PolicyS3FIFO` / `PolicyTinyLFU` — and the three calls every policy answers: `admit`, `touch`, `evictOne` |
| `s3fifo.go` | S3-FIFO: admission into small or main (`admitS3FIFO`), the small/main walk (`evictS3FIFO`, `evictSmall`, `evictMain`), the shares (`smallShare`, a fiftieth; `ghostShare`, a quarter) |
| `ghost.go` | `ghostSet[K]` — S3-FIFO's ghost: a bounded FIFO of key hashes (`hash/maphash.Comparable`) |
| `tinylfu.go` | W-TinyLFU: the window, probation and protected segments and their shares (`initTinyLFU`, `protectedShare`), admission (`admitTinyLFU`), a hit (`touchTinyLFU`, `reorderTinyLFU`), the contest (`evictTinyLFU`, `contest`, `admitAtLeast`), the process-wide `sketchSeed` |
| `sketch.go` | `frequencySketch` — TinyLFU's doorkeeper (a Bloom filter, four bits an increment of the period, three a key) in front of a count-min sketch of four-bit counters, a key's four in one 64-byte `sketchBlock`, halved every ten increments an entry of the bound |
| `striped.go` | `Striped[K,V]` + `NewStriped` — N independent Caches behind one hash, each with its own lock (#808) |
| `eviction.go` | `evictionValue[K,V]` — an evicted key/value pair, copied out of the locked section for `OnEvict` |
| `config.go` | `Config[K,V]` — MaxEntries / DefaultTTL / Clock / OnEvict / MaxWeight / WeightOf / Policy |
| `stats.go` | `StatsValue` — Hits / Misses / Evictions / Weight / Policy |

## Conventions

- **`Fetch`, not `Get`**: a hit mutates state (LRU promotion or the S3-FIFO
  counter, plus the hit counter), so the read verb is `Fetch` — `Get` is
  reserved for pure accessors (KTN-GETTER-PURE).
- **`sync.Mutex`** (#1038, amending ADR 0025 §1): every call takes it,
  `Len`/`Stats`/`Keys` included. The uncontended lock and unlock are one
  atomic each and inlined; a `RWMutex`'s write lock cost four on every
  `Fetch` and `Set`, to let two `Len`s read at once.
- **`UnlockedMap` is the cache, `Cache` its lock** (#1038). `UnlockedMap[K,V]`
  carries every mechanism and no lock, for a caller that already serialises
  (the cache domain's stripes, #1059) — two locks on one path paid four
  atomics for nothing. Not safe for concurrent use; `-race` sees a misuse.
  Its OnEvict runs at the end of the call that evicted, once the cache is
  consistent, so it may re-enter. `TestUnlockedMapBehavesAsCache` drives both
  through the same seeded operations and compares every observable.
  Named `UnlockedMap`, not the reviews' `Unlocked`: an exported struct names
  its role by a suffix (KTN-STRUCT-ROLE), and `cache.UnlockedCache` stutters.
- **What the domain needs, and no more** (#1059). The cache domain keeps its
  records in an `UnlockedMap` and no longer copies what the map holds:
  `Deadline(key)` reads an entry's deadline back onto the clock it was taken
  from (`epoch.Add`), the zero `Time` for none — a read, never a hit, no
  counter moved, a passed deadline reported rather than reaped
  (`TestUnlockedMapDeadlineIsARead`) — and the weight stays the one
  `WeightOf` answered when the entry was stored, the call the domain's
  stripe hands it over on. `UnlockedMap.Fetch` writes its lookup out instead
  of delegating to `fetch`, which a `Cache` calls under its lock: a hit is
  one call deep and sets up no eviction list, and only a miss leaves for
  `missed`. `Cache`'s own path is unchanged.
- **The node is 64 bytes** (#1038). For a string key and a word-sized value
  the entry is 64 bytes — W-TinyLFU's period (2) and sketch hash (4) took the
  padding after the queue (#834) —: the 64-byte size class, one cache line. A deadline
  is an `int64` of nanoseconds since the cache's epoch — the clock's reading
  at the first deadline — taken by `time.Time.Sub`, so on the monotonic
  readings when both carry one, as `Before` compared them; a sum past the
  `int64` range (a TTL of centuries) is held at its maximum. An entry has a
  deadline when its heap slot is set, so a deadline of 0 is a deadline.
  `Test_entry_size` holds the budget.
- **Expiry: lazy, plus a sweep the CALLER drives** (#1038). An expired entry is
  reaped by the next `Fetch` of its key, by a capacity eviction, or by
  `Expire()`. Every entry carrying a deadline sits in an expiry heap
  (`deadline.go`: 4-ary, the deadline inline beside the entry, so a sift
  compares two `int64`s and calls nothing — `heap.IndexedHeap`, which the
  cache used until #1038, compares through a function value): `SetTTL`
  re-keys its cell, `Delete` and every eviction remove it, so `Expire` pops
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
- **Three policies** (#834). `PolicyLRU` (the zero, today's behaviour) moves
  a hit to the front and evicts the tail. An unknown `Policy` value runs as
  LRU and `Stats().Policy` says which policy runs — the kernel has no error
  surface (rule 1). The entry a `Set` just wrote is never its own victim,
  under any policy, and it never shields another (#1380): every walk steps
  over it rather than stopping there (an overwrite leaves it where it sits,
  newer entries ahead of it). Every behavioural table of
  `cache_external_test.go` runs under the three (`forEachPolicy`).
  - `PolicyS3FIFO` (Yang et al., SOSP 2023) admits a new key into a small
    FIFO holding a **fiftieth** of the bound; at the small tail an entry hit
    since admission moves to main with its counter cleared, one never hit is
    evicted and its key's hash remembered by a ghost FIFO of a **quarter** of
    the bound; a key the ghost remembers is admitted straight into main; main
    is a clock — a tail with hits left goes back to the head with one fewer
    (the counter saturates at 3, the paper's two bits). A hit moves nothing.
    Main is asked again after the walk promoted its hit entries, and an
    entry alone in main with hits left spends them and goes (#1380). The
    paper's tenth and ghost as long as main admitted into main keys that came
    back once by chance: measured over 100 million accesses a cell
    (`BENCH.md`, 2026-11-15), the fiftieth and the quarter keep more of every
    Zipf, scan and loop cell — 83.06 % against 82.06 % on Zipf s = 1.0 at a
    tenth of the keys —, and cost 0.9 points on popularity that moves.
  - `PolicyTinyLFU` (W-TinyLFU, Einziger, Friedman and Manes, ACM TOS 2017)
    admits a new key into an LRU **window** of a hundredth of the bound (at
    least one entry); the window's overflow moves into main's **probation**
    while main has room, and once main is full its oldest is a candidate that
    contests main's victim — probation's oldest, else protected's — and stays
    only when its estimate is at least 2 (`admitAtLeast`) and above the
    victim's; a tie keeps the victim. A hit in probation moves the entry to
    **protected** (four fifths of main), whose oldest go back to probation
    past its share. Every `Set` of a new key and every hit counts the key in
    `frequencySketch`: its first use in an aging period sets three bits of
    the doorkeeper and no counter, so a key used once — a scan, the long
    tail — is estimated at 1, below the bar of a contest; later uses count
    in the sketch; every ten increments an entry of the bound the counters
    are halved and the doorkeeper emptied. A resident entry records the
    period its key went through the doorkeeper (`entry.period`), so a hit
    counts without reading the doorkeeper, and the estimate of a resident
    key is exact on that bit. The period is 16 bits, numbered from 1 and
    wrapping; 0 is the mark of no period (`periodStale`), and when the
    numbers come round — or the table grows, which opens a period with an empty
    doorkeeper — `retireMarks` marks every held entry stale in one pass, so a
    mark never reads as the current period by coincidence. The key's 32-bit hash (`maphash.Comparable`
    under `sketchSeed`, one seed per process, so two caches of one process
    count alike and no input can target a key's counters) is taken once at
    admission and kept in the entry. The sketch is sized by the entry bound
    (one word of sixteen counters an entry, 8 bytes, and four doorkeeper bits
    an increment of the period, 5 bytes), or by the live count, growing,
    under a weight bound alone; a cache with no bound keeps none. A `Set`
    followed by a `Fetch` always hits: the window holds the key just written.
    Measured: 82.99 % on Zipf s = 1.0 at a tenth of the keys, 82.31 % through
    the scans, 82.47 % on the loop that S3-FIFO and LRU lose; the hit costs
    one sketch count, ~16 ns on the shared VM (`BENCH.md`).
  - Tests that read the contest's outcome pick keys whose counters and
    doorkeeper bits share nothing (`disjointKeys`): the seed is drawn per
    process, and a count-min sketch shares counters by design.
- **Striping is a sibling** (#808). `Striped[K,V]` holds N Caches, picks one by
  `hash(key) % N` (nil hash = `maphash.Comparable` over a per-`Striped` seed),
  and divides `MaxEntries`/`MaxWeight` between them (remainder to the first
  stripes, at least one unit each when the bound is set). The policy is exact
  within a stripe, approximate across them; with one stripe it is one Cache.
  `Len`/`Stats`/`Expire`/`Purge`/`Keys` walk the stripes one lock at a time — never
  two locks at once.
- **`Keys` yields a snapshot** (#816). The keys present and unexpired are
  copied under the lock and yielded after it is released — the rule
  OnEvict keeps: no caller code runs under `mu` — so the loop's body may
  Set, Delete or Fetch. A walk is not a read: nothing is promoted, no counter
  moves. One slice per loop (`BenchmarkKeys_1024`: 1 alloc, ~34 µs).
- **OnEvict** fires on capacity/expiry removal only — never on `Delete`/overwrite/`Purge`.
  It runs after the mutex is released, so the callback may call back into the cache.
- **The hot paths make one call** (#1038). Under LRU, `Fetch` runs the
  lookup and the relink in `UnlockedMap.fetch`, the relink inlined and writing no
  count or weight; S3-FIFO's counter is inlined there too, and W-TinyLFU's
  protected relink beside one call to the sketch (#834). A new key's `Set`
  runs `UnlockedMap.set`, which pushes onto the LRU list inline and calls the
  deadline, the policy's admission and the eviction loop only when a TTL,
  another policy or a bound asks for them.
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

## Reference in NOTES.md

`internal/kernel/collections/cache/NOTES.md`, read by a change that needs the mechanics of `PolicyTinyLFU`: `W-TinyLFU's sketch`; `The node stays one cache line`; `What a hit costs`.
