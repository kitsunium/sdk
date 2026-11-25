# internal/kernel/collections/frozen/

## Purpose

**Read-only maps built once from a known key set and read in place** (ADR
0252): `Map[K, V]` (integer keys and values), `StringMap[V]` (string keys,
integer values — a symbol table) and `Dict` (string keys and values — a
configuration). A minimal perfect hash function in the manner of PTHash and
PtrHash gives every key its own record, the table IS its image (one byte
slice, read with `encoding/binary`), and `AppendBinary` / `MarshalBinary` and
the three loaders move it to and from bytes without a copy. Stdlib-only AND
generic: its signatures name keys, values, bytes and a verification level —
no symbol, no series, no config — so it passes SDK rule 1.

**Not published** in `pkg/v1` until a domain adopts it (Chronos' symbol tables
and index, configuration caches are the callers it was written for). Codes
`0.1.9.*` (`design/sdk.yaml`'s `codes.ranges`).

## Contents

| File | What it holds |
|---|---|
| `doc.go` | the package comment — kit writes it from the design (ADR 0167) |
| `gen_codes.go` | the seven codes and sentinels — kit writes them (ADR 0164) |
| `map.go` | `Integer`, `Map`, `StringMap`, `Dict`: their constructors, loaders and methods, each a thin typed shell over `table` |
| `table.go` | the image's layout (header, sections), `table` (the sections as slices of the image), the lookups `findInt` / `findString`, the record and entry readers |
| `hash.go` | `hashInt` (a bijective finalizer), `hashString` (16 bytes per folded multiply), `below` (the range reduction), `slotOf` (a key's slot under a pilot) |
| `solve.go` | the search: buckets by counting sort, largest first, a free pilot or the cheapest eviction, the remap of the slots past n |
| `build.go` | `build` (size checks, solve, the image written in slot order), `widthOf` (an integer type's width and sign, without `unsafe`) |
| `load.go` | `Check`, `load` (header, sizes, shape) and `verify` (`CheckAll`'s walk of the body) |
| `view.go` | `stringOf`: the package's one use of `unsafe`, a string over the image's blob |

## The surface

| Symbol | What it is |
|---|---|
| `NewMap(keys, values)` / `NewStringMap` / `NewDict` | a build from two aligned slices; `LENGTH_MISMATCH`, `DUPLICATE_KEY` (the second index), `TOO_LARGE`, `UNSOLVED` |
| `LoadMap[K, V](image, check)` / `LoadStringMap[V]` / `LoadDict` | a table over the caller's bytes, which it keeps; `IMAGE_CORRUPT` (with `why`), `IMAGE_VERSION`, `IMAGE_SHAPE` |
| `Get(key)` | `(value, ok)`; no allocation (a design budget, `allocs: 0`, held by `gen_perf_test.go` race off) |
| `Len()`, `All()` | the count; every pair in slot order |
| `AppendBinary(b)`, `MarshalBinary()` | the image (`encoding.BinaryAppender`, `encoding.BinaryMarshaler`) |
| `CheckHeader`, `CheckAll` | the header in constant time, or everything in linear time |

## Why-this-shape

- **A minimal perfect hash, not a probed table.** n keys take n records and no
  empty one; a lookup reads a pilot (a third of a byte a key, cache-resident),
  a tag and one record. A SwissTable-style table — what `cnfgo` builds — keeps
  an eighth of its slots empty and probes groups; the comparison is in
  `BENCH.md` and ADR 0252.
- **The tag array is what makes a miss cheap.** One byte a key, the hash's low
  byte, read before the record: an absent key is refused 255 times in 256
  without touching the records or the blob. Without it a miss on a million
  string keys cost about 60 ns; with it about 15.
- **A string table's record is an offset into the blob**, whose entry holds
  the key's length, a Dict's value length, then the key and the value: a hit
  reads a 4-byte offset and one entry, the key and the value on the same
  lines.
- **An integer table's record is the key then the value**, each at its type's
  width, read with `encoding/binary` — one read for a hit. Keys are compared
  masked to their width, so a signed key's sign extension never matters.
- **The image is the table.** Building writes the image, loading slices it, so
  there is one layout, one reader and no decode step; a table of any size
  holds six pointers (`TestTableHoldsNoPointerPerKey`). A Dict value or a
  string key handed out is a view of the image: zero allocation, and the
  reason `stringOf` exists.
- **One `unsafe`, isolated.** `stringOf` makes a string over the blob's bytes.
  It is sound because nothing writes an image a table views: a built table owns
  its image, and a loaded one is the caller's under the loaders' contract
  (alive and unchanged). No typed array is ever made over bytes: integer
  records are read with `encoding/binary`, which is why the value types are
  integers and not arbitrary pointer-free structs.
- **Determinism.** The seeds are a fixed sequence, the buckets are placed by
  size then index, the records and the blob are written in slot order: one key
  set always writes the same image, whatever order it came in
  (`TestImageRoundTripAndDeterminism`).
- **`hashInt` is a bijection.** One folded multiply kept the structure of
  consecutive keys and a build of 200 000 of them took two minutes; the
  finalizer takes 40 ms and never maps two integer keys to one hash.
  `TestStructuredKeys` builds consecutive, strided and high-bit keys so a
  weaker hash shows up as a hung test, not a slow production build.
- **No floats.** NaN equals nothing: a frozen table would have to choose
  between refusing it and holding a key no `Get` finds, and a caller who needs
  float keys can freeze their bits as `uint64`.

## Do NOT

- Mutate the bytes given to a loader while the table lives, or the bytes a
  table's `Get` returned: they are the image.
- Load untrusted bytes under `CheckHeader`. It verifies the header only; a
  damaged body can make `Get` answer wrongly or panic. `CheckAll` is the mode
  that never panics (`FuzzLoad`, `FuzzLoadSealed`).
- Change the hash, the slot function, the seed sequence or the layout without
  a new `version`: an image written by one build is read by the next.
- Add a value type the image cannot hold without `unsafe` (a struct, a float
  read through a pointer): encode it as an integer, or grow the layout.

## Tests

| Test | Holds |
|---|---|
| `TestMapMatchesNativeMap`, `TestStringMapAndDictMatchNativeMap` | every key found with its value, absent keys refused, `All` complete, at 0, 1, 2, 3, 7, 100, 1000 and 50 000 keys |
| `TestSignedAndNarrowTypes` | `int8` keys, `int16` values, round trip, a load as unsigned refused |
| `TestEdgeCases` | duplicates, length mismatch, a 1 MiB key and an empty one, the zero value |
| `TestImageRoundTripAndDeterminism` | one set in two orders writes one image; a flipped bit refused under `CheckAll` |
| `TestLoadRefusesEachHeaderFault`, `TestCheckAllRecomputesTheSlots` | each header fault its code; swapped records with valid CRCs caught by the walk |
| `TestStructuredKeys` | consecutive, strided, high-bit and numbered-string keys build and are found |
| `FuzzLoad`, `FuzzLoadSealed` | the loaders never panic on any bytes, sealed or not, and an accepted table finds what it yields |
| `TestConcurrentGets` | eight readers on one Dict, under `-race` |
| `TestTableHoldsNoPointerPerKey` | the table's fields are byte slices and one string |
| `gen_perf_test.go` (race off, alloc lane) | `Get` allocates nothing on any of the three types, hit or miss |

## Verification

```
cd internal && GOWORK=off go test -race ./kernel/collections/frozen/
cd internal && GOWORK=off go test -run '^$' -fuzz FuzzLoadSealed -fuzztime 60s ./kernel/collections/frozen/
bazel test //internal/kernel/collections/frozen:frozen_test
```
