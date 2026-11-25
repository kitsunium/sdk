# ADR 0252 — a frozen map is a minimal perfect hash, read in place from its image

- **Status**: Proposed
- **Date**: 2026-11-25
- **Deciders**: SDK maintainers
- **Amends**: nothing. One new kernel package, `internal/kernel/collections/frozen` (`0.1.9.*`), a member of the `collections` family (ADR 0159 §4).
- **Related**: [ADR 0025](0025-sdk-cache-kernel.md) (a kernel container admitted with no consumer), [ADR 0163](0163-the-sdk-is-designed-by-its-diagram-and-kit-writes-only-data-and-test-pins.md) and [ADR 0165](0165-a-performance-contract-is-the-designs-kit-measures-what-the-compiler-decides-and-a-test-holds-the-rest.md) (the design and the `allocs: 0` budgets of `Get`), [ADR 0156](0156-the-public-module-links-the-standard-library-and-nothing-else.md) (no dependency)
- **Area**: kernel
- **Need**: decision

## Context

Several programs built on the SDK hold a table that is computed once and then
only read: the symbol tables and the series index of a time-series store
(Chronos), a configuration snapshot, a routing table. A Go `map` serves them
and costs what a mutable table costs: an eighth of its slots empty, the
string keys' headers — one pointer each — scanned by every collection, and
nothing to write to a file but a re-encoding.

`github.com/Voskan/cnfgo` (MIT, v0.1.0) answers the same need with a
`FrozenMap`: an open-addressing table of the SwissTable kind (eight control
bytes a group, compared a word at a time, load at most 7/8), whose slots
point into one blob of encoded keys and values, decoded at every `Get`, hashed
with wyhash under a stored seed so the table can be written to a file. It is a
dependency the SDK does not take (ADR 0156), and its documentation reports
12.6 ns for a string `Get` on an Apple M5 Max.

The question was whether the SDK can hold such a table in the kernel, smaller
and faster, from the standard library alone.

## Decision

1. **A minimal perfect hash function, not a probed table.** A build searches,
   for its key set, a function that sends n keys to n distinct slots — the
   approach of PTHash (Pibiri and Trani, SIGIR 2021) and PtrHash (Groot
   Koerkamp, 2025): keys fall into buckets of three on average, the buckets
   are placed largest first, each takes the first one-byte pilot whose slots
   are all free, and a bucket with no free pilot evicts the cheapest set of
   placed buckets (the sum of their sizes squared, none evicted moments
   before) and sends them back to be placed. The table has n·100/97 slots;
   the slots taken past n are remapped onto the holes below it, so n keys take
   exactly n records. A seed that fails (two integer keys can never share a
   hash; two strings can) is followed by the next of a fixed sequence, eight
   at most (`UNSOLVED`).
2. **A lookup is a pilot, a tag and one record.** The pilots are a third of a
   byte a key and stay in cache. Before any record is read, the slot's tag —
   the key hash's low byte, one byte a key — refuses 255 absent keys in 256.
   An integer table's record is the key then the value, each at its type's
   width; a string table's record is a 4-byte offset of an entry in the blob,
   which holds the key's length, a `Dict`'s value length, the key and the
   value together. Nothing is decoded and nothing is allocated (`allocs: 0`
   budgets on the three `Get`s, ADR 0165).
3. **The table is its image.** A build writes the image and a load slices it:
   one layout, one reader. The image is little-endian and versioned: a
   64-byte header (magic `KFZN`, version 1, the shape — kind, key and value
   widths and signs —, the counts, the seed, the sizes, a CRC-32C of the body
   and one of the header) then the pilots, the tags, the remap, the records
   and the blob. A table of any size holds six pointers and the collector
   scans none of its bytes.
4. **Two verification levels.** `CheckHeader` verifies the header in constant
   time, for bytes the caller wrote and kept. `CheckAll` verifies, in linear
   time, the body's CRC-32C, every remapped slot, every string's bounds, every
   tag, and that every record's key hashes back to its own slot — which also
   proves no two records hold one key. A table loaded under `CheckAll` never
   panics; two fuzz targets hold that, one of them sealing the checksums over
   its mutations so they reach the walk.
5. **No mmap, no typed arrays over bytes.** The kernel is stdlib-only and
   every platform's: the caller brings the bytes and keeps them alive and
   unchanged. Integers are read with `encoding/binary`, so the value types are
   integers — no `unsafe.Slice` over a struct type. The one `unsafe` is
   `stringOf`, a string over the blob, which lets `Get` compare a key and
   return a `Dict` value without a copy; it is sound because nothing writes an
   image a table views.
6. **Deterministic and portable.** The hashes are the package's own,
   stdlib-only and fixed with the version: integers through a bijective
   finalizer (Stafford's thirteenth, the one SplitMix64 ends with), strings
   sixteen bytes per folded 64×64→128 multiply. The seeds are a fixed
   sequence and the buckets are placed by size then index, so one key set
   writes one image whatever order it is given in. Floats are not keys: NaN
   would have to be refused or held unreachable.
7. **Three types, not one generic over everything.** `Map[K, V Integer]`,
   `StringMap[V Integer]` and `Dict`. A single `Map[K, V]` over integers and
   strings would need a type switch in `Get` or a pointer cast — the first
   costs more than the lookup, the second is the type punning this package
   refuses. A table of integer keys and string values is not provided until a
   caller needs one.
8. **Clean room.** cnfgo's source was never read. It was used as a black box,
   through its public API (`cnfrt.NewFrozenMap`, `Get`, `MarshalCNF`), in a
   benchmark module outside the SDK that the SDK does not depend on.

## Measured

Apple M1 Pro (10 cores, 16 GB), Go 1.27.4, the same keys for the three
implementations (random `uint64`s; strings of 8 to 13 bytes), lookups in a
shuffled order with string probes in allocations of their own. The machine
was shared with other work during every run; `BENCH.md` gives the benchstat
tables and their spread, which is wide at the two larger sizes.

Median ns per `Get` (frozen / cnfgo / native), `-count=10`:

| keys | uint64 hit | uint64 miss | string hit | string miss |
|---|---|---|---|---|
| 1 000 | 9.0 / 11.7 / 11.4 | 7.0 / 8.8 / 7.4 | 15.7 / 22.7 / 14.5 | 7.8 / 13.0 / 11.3 |
| 100 000 | 21.3 / 33.9 / 16.3 | 7.5 / 20.3 / 26.4 | 24.9 / 41.4 / 68.4 (rerun) | 9.1 / 27.8 / 45.9 |
| 1 000 000 | 46.1 / 156 / 63.8 | 12.8 / 48.4 / 44.1 | 108 / 220 / 302 | 14.8 / 32.7 / 57.0 |

Held in memory at a million keys: 17.5 / 52.2 / 37.8 bytes a key for
`uint64 → uint64`, 29.6 / 56.3 / 55.9 for `string → uint64` (frozen's
including the key bytes it copies, the native map's not), none of frozen's
or cnfgo's scanned by the collector, all 55.9 of the native string map's.
The image is 17.5 and 29.6 bytes a key against cnfgo's 51.7 and 55.8.

A build is slower than cnfgo's below a million keys — 83 µs against 58 at
1 000 integer keys, 11.1 ms against 5.5 at 100 000 — and level at a million
(243 ms against 230, both spreads past 100 %); it allocates 2.3 to 2.6 times
less. A native map is the fastest to build, by 2 to 9 times, and at 1 000 keys
and on integer hits at 100 000 it looks up as fast as frozen within the
noise. The miss is where the design wins most: the tag byte refuses an
absent key before any record is read.

## Consequences

- A table the program builds once costs about a third of a native map's
  memory for integer keys and half for string keys, and its bytes are never
  scanned; a native `map[string]V` keeps one scanned pointer a key.
- A table can be written to a file and read back with no decode, by any
  platform, and a damaged or hostile image is refused rather than trusted
  under `CheckAll`.
- A build is slower than filling a native map — the search is the price of
  the lookups — and a frozen table cannot change: a new key set is a new
  build.
- The hash, the slot function, the seed sequence and the layout are part of
  the image format: changing any of them is a new `version`.

## Alternatives rejected

- **Take cnfgo as a dependency.** ADR 0156: the SDK builds on the standard
  library, and a third-party module enters only quarantined in a vendor
  module, for a vendor's protocol — not for a data structure.
- **A SwissTable-style frozen table.** Simpler to build, but it keeps empty
  slots and probes groups; the minimal perfect hash reads one record and
  holds none empty.
- **A sorted array and a binary search.** No hash, the smallest image, but
  log2(n) dependent reads: twenty at a million keys.
- **Typed arrays cast over the image (`unsafe.Slice`).** Would admit any
  pointer-free value type and a zero-copy `[]V`, at the price of type punning
  and of alignment the caller's bytes do not promise.
