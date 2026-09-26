# ADR 0110 — a document store writes one entry per write and rests as one file

- **Status**: Accepted
- **Date**: 2026-09-26
- **Deciders**: SDK maintainers
- **Related**: [ADR 0056](0056-sdk-vfs-domain.md) (the atomic publication every write is), [ADR 0074](0074-what-a-public-alias-may-point-at.md) (no port, so the values are the engine's), [ADR 0031](0031-policy-zero-values-are-never-inert.md) (the clamps), [ADR 0039](0039-extending-a-published-port-without-breaking-it.md)

## Context

The framework built on this SDK (kitsunium/platform, kit) stores its products'
entities in `kit/store.go` and `kit/index.go`: typed documents under a key,
JSON-encoded so a read is always a copy, three write modes (upsert, insert
only, replace only), unique and multi-valued secondary indexes rebuilt on load
(a load that breaks a unique index is refused), and write and delete hooks
called outside the lock. None of that is framework. A Go program that never
imports kit and keeps a few thousand documents for itself wants all of it,
unchanged.

It also has the one known defect kit's own notes list: `Store.flush` rewrites
the WHOLE store on every write. Measured here with kit's own encoding (every
document into one indented JSON object, published atomically), one write costs
**54.9 µs** at 100 documents, **7.1 ms** at 10 000 and **70.3 ms** at
100 000. That is linear in a store's size, for a write that changed one
document.

## Decision

### D1 — `internal/service/docstore`, with no core counterpart

`Open[T](Config[T], ...IndexSpec[T]) (*Store[T], error)`. There is one engine
and no second implementation a port would describe, so the values are the
engine's (ADR 0074), exactly as for `redact`. The code range is `0.3.80.*`.
The facade `pkg/v1/docstore` aliases every type and exports the codes, because
a caller maps them: a framework turning `DocumentNotFound` into a 404 reads
`CodeDocumentNotFound`.

The indexes are `Open`'s variadic arguments (`Unique`, `Index`), not a
`Config` field. A store is declared the way a framework declares one, a key
and then its indexes, and the configuration stays a small value.

### D2 — the files: one snapshot at rest, one overlay entry per write

The resting state is ONE file, the snapshot at `Config.Path`: a JSON object
mapping each key to its document, indented, keys sorted. That is exactly the
format kit's files already have, so kit's existing data opens as it is.

A write does not touch the snapshot. It publishes one small file through
`vfs.AtomicWriter.WriteAtomic`, in the directory `Path + ".d"`, before the
write returns: the overlay entry for that key, holding `{key, doc}` or
`{key, deleted}`. The entry's name is the SHA-256 of the key in lowercase
hexadecimal, which is fixed-length, safe on every filesystem (a
case-insensitive one included) and says nothing about the key.

The overlay is folded back into the snapshot by the write that brings it to as
many entries as the store holds documents, and never below `DefaultFoldAt`
(1024). The fold rewrites the snapshot once and removes the entries it now
contains. `Open` folds when it replayed anything, and `Close` folds too, so a
closed store is ONE file a person can read. `Open` folds only after every
check has passed, so an open it refuses (a broken unique index, a document
the type no longer fits) leaves the snapshot an operator must repair exactly
as it was. `FoldAt` sets another threshold, and a negative one never folds on
a write.

What a write costs, measured on darwin/arm64 over the in-memory filesystem (so
the device is out of the number and only the store's own work is left, folds
amortised; `BENCH.md` has the table and its envelope):

| documents | this store | the whole-file rewrite | ratio |
|---|---|---|---|
| 100 | 3.0 µs | 54.9 µs | 18× |
| 10 000 | 4.5 µs | 7.1 ms | 1 560× |
| 100 000 | 5.6 µs | 70.3 ms | 12 600× |

On a real disk, the cost is the device's two flushes: **11.0 ms** at 100
documents and **11.0 ms** at 10 000. That is flat, and it is the same
`fsync` pair a durable queue publish pays (ADR 0054). Opening decodes every
document to rebuild the indexes: 0.19 ms at 100 documents, 21 ms at 10 000.

### D3 — replay is order-free and idempotent, by construction

An entry holds its key's WHOLE latest state, and at most one entry exists per
key. Replaying the overlay over the snapshot is therefore independent of order.
An entry the last fold already contains holds exactly what the snapshot holds,
so replaying it changes nothing. That makes every crash point of a fold safe:

- after the snapshot is written and before the removals;
- between two removals;
- after a removal the filesystem never made durable.

Each loads the same documents. It holds because the fold keeps the writers'
lock throughout: no entry changes between "encode the snapshot" and "remove
what it contains". A temporary left by a crashed publication (`.vfs-*.tmp`) is
removed at `Open`, and any other stranger in the overlay directory is left
alone.

### D4 — two locks: readers never wait for a disk

A writer takes the writers' lock and does its filesystem work under it alone.
The documents and the indexes change only once the write is durable, under a
second lock that readers share. A reader sees the state before a write or after
it, never a document and an index that disagree, and never waits for a device.
The caller's functions — the key, the index keys — run before any lock.
`Update`'s function runs under the writers' lock only, so it may read the store
it updates, and must not write to it.

### D5 — two verdicts for a failed publication

- `PERSIST_FAILED`: the filesystem refused. Nothing changed, in memory or on
  disk, so the two still agree.
- `WRITE_UNCONFIRMED`: vfs's `DirectorySyncFailed`. The rename happened and
  only the directory flush failed, so the write TOOK EFFECT. The store applies
  it and calls the hooks, because a store that disagreed with its own files
  would be worse. What is in doubt is only whether it survives a power loss.

A failed AUTOMATIC fold does not fail the write that triggered it, since that
write's entry was already durable. It is kept in `Stats().FoldError` until a
fold succeeds, and the entries stay.

### D6 — what kit's store did, kept, and what no refusal says

- **Kept from kit's store:**
  - the three write modes, where Replace never resurrects a deleted document;
  - `Update`'s rename refusal (`DOCUMENT_KEY_CHANGED`) and its untouched
    error;
  - unique and multi-valued indexes, where an empty key is no key and a
    repeated key is filed once;
  - `Lookup`, `Find` (store-key order, an empty slice for no match) and
    `Filter`;
  - the rebuild on open, which refuses a broken unique index or a panicking
    key function (`INDEX_BROKEN`);
  - hooks called with the key once the write is durable, outside every lock,
    registered and removed at any time.
- **Never in a refusal:** a store key is routinely an e-mail address, and an
  index key the hash of a token. No refusal quotes either one. A decoding
  failure names the field and the Go type, never the value, whose digits
  encoding/json would quote.
- **No context:** nothing here can be abandoned. The waits are the writers'
  mutex and a device flush, and a flush a caller walked away from still
  happens.

## Consequences

- kit's `Store[T]` wraps a `docstore.Store[T]`:
  - it opens one at start, with `FS` set to kit's data `vfs.FullFS` and `Path`
    set to `<service>/<store>.json` (or no `FS` for an in-memory store), and
    closes it at stop;
  - it keeps its spans, its "not running" error and its graph description;
  - it maps the codes to its own errors;
  - its workflow calls `Insert` and `Replace`, and `OnWrite` / `OnDelete`
    return the removers its `stop` needs;
  - the Studio's raw view is `Entries(limit)`.
- kit's pinned file behaviour holds without a test change. After a clean stop,
  `shop/items.json` contains the document. A snapshot edited by hand so that
  two accounts share an e-mail refuses the next start with `INDEX_BROKEN`,
  which kit reports as its own `CodeStoreIndex`.
- A write's cost no longer depends on the store's size.

## Breaking changes

None. `docstore` is a new package: service range `0.3.80.1`–`0.3.80.15`,
facade `pkg/v1/docstore`.

## Alternatives considered

- **Rewrite the snapshot on every write** (kit's store). Rejected: that is the
  O(N) this exists to remove.
- **One file per document, with no snapshot.** It has the same write cost and
  is simpler, but a closed store would be N files instead of the one file an
  operator reads and a framework's existing data already is. Every existing
  store would also have to be migrated.
- **An append-only journal.** `vfs` publishes whole files and has no append.
  An ordered journal also needs an ordering its replay depends on, and a
  per-key entry needs none.
- **Persist the indexes.** They are derived data. A derived file that can
  disagree with its source needs a repair path, and rebuilding costs one decode
  per document at open (D2).
- **A context on every call.** Rejected (D6).

## Deferred

- **A second process opening the same files.** It is not detected, and each
  process would trust its own memory. An exclusive lease through `core/lock`
  is the answer, deferred until a caller runs two.
- **Durability of the directories Open creates.** The snapshot's publication
  flushes its own directory, which makes the overlay directory durable. The
  first creation of a PARENT directory is not flushed, because `vfs` has no
  verb for it.
- **Queries beyond the indexes, and ordered iteration without a sort.** `List`
  sorts the keys each time.

## Verification

- `internal/service/docstore`:
  - `store_external_test.go`: the modes, `Update`, the hooks, `Entries`, a
    closed store, a document the type no longer fits (the value in no text).
  - `index_external_test.go`, kit's cases ported: the unique index without the
    key in the refusal, `Find`/`Filter`, thirty-two concurrent writers of one
    key with exactly one winning, the rebuild on open and its refusal, a
    panicking key function, every configuration refused.
  - `persist_external_test.go`: durable before a write returns, one snapshot
    after `Close`, a fold interrupted at three points loading the same
    documents, a framework's bare snapshot, leftovers and strangers, six files
    `Open` refuses (their content in no text), a refused open that writes
    nothing, fold thresholds,
    `PERSIST_FAILED` and `WRITE_UNCONFIRMED`, a failed automatic fold, the
    store over the operating system's filesystem (0600 files, 0700
    directories).
  - `concurrency_external_test.go`: readers answered while a publication is
    held at a gate; every call at once under the race detector.
  - `docstore_bench_test.go` and `BENCH.md`.
- `pkg/v1/docstore`: the same through public names.

## References

- `internal/service/docstore/`, `internal/service/docstore/BENCH.md`
- kitsunium/platform `docs/adr/0001-the-sdk-holds-the-mechanisms-kit-is-the-framework.md`
  (the map, wave 3)
