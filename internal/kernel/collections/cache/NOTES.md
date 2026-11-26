# internal/kernel/collections/cache/ — reference

The implementation detail of `PolicyTinyLFU` that ADR 0025's amendment of
2026-11-15 (#834) keeps out of the decision. `CLAUDE.md` holds what to obey.

## W-TinyLFU's sketch

- The estimate is TinyLFU's doorkeeper — a Bloom filter that takes a key's
  first use in an aging period, four bits an increment of the period (three
  of them set per key) — in front of a count-min sketch of four-bit counters,
  a key's four in one 64-byte block (Caffeine's layout), halved every ten
  increments an entry of the bound, the doorkeeper emptied at the same time.
- The counters are shared between keys by design, so what `contest` reads
  (`estimateHeld`) is an estimate: unrelated keys can raise it, and aging
  removes a resident's doorkeeper credit.
- The key's 32-bit hash (`maphash.Comparable` under `sketchSeed`) is taken once
  at admission and kept in the entry. The seed is drawn once a process, so
  two caches of one process count alike and no input can be crafted to hit a
  key's counters.
- The sketch is sized by the entry bound (one word of sixteen counters an
  entry, 8 bytes, and four doorkeeper bits an increment of the period,
  5 bytes), or by the live count, growing, under a weight bound alone; a cache
  with no bound keeps none.

## The node stays one cache line

The 32-bit hash and the 16-bit period in which the key last went through the
doorkeeper take the padding after the queue byte: 63 bytes for a string key
and a word value. A hit counts its key off the stored hash without reading
the doorkeeper; S3-FIFO's counter and W-TinyLFU's protected relink are
inlined in `fetch` beside LRU's.

## What a hit costs

S3-FIFO's hit sets a counter; W-TinyLFU's adds one sketch count — about
16 ns on the development VM's Zen 1, where LRU's hit takes 49. The verdict is
the A/B lane's (`834-fetch-hit-s3fifo`, `834-fetch-hit-tinylfu`).

## The batched read

`UnlockedMap.FetchMany(keys, refs)` (#840, ADR 0025 amended 2026-11-15)
appends one reference a key — the entry's own value, nil for a miss —, read
as `Fetch` reads it with three differences, each a cost `Fetch` pays once a
key: the clock is read once a batch, at the first entry that carries a
deadline (`TestUnlockedMapFetchManyReadsTheClockOnce`); a chunk of sixteen
lookups (`lookupChunk`) is issued before any entry is read, so the map's
cache misses overlap; and a key the batch names twice is read once — its
first position records the hit through the policy's `touch` and sets
`readMark`, the top bit of the entry's freq, and its later positions get the
same reference without a relink or a count
(`TestUnlockedMapFetchManyReadsARepeatOnce`). The node has no byte left for a
mark of its own: W-TinyLFU's period and hash fill it to 64 bytes
(`Test_entry_size`), and a 32-bit batch number there took it to 72. The batch
keeps the entries it marked in `marked`, its room reused from one call to the
next, and `clearMarks` clears them before the call returns, so S3-FIFO's
counter never reads a mark. An entry past its deadline is reaped as `Fetch`
reaps it, its later positions of the chunk looked up again, and `OnEvict`
runs at the end of the call (`TestUnlockedMapFetchManyReapsWhatExpired`). A
reference is valid until the next call into the map, to be read and never
written through: the caller holds the lock the map is serialised under.
`refs` grows only when its capacity is short, so a caller with stack room
allocates nothing.
