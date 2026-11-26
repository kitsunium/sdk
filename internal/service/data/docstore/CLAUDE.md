<!-- updated: 2026-11-02T00:00:00Z -->
# internal/service/data/docstore/

The reference half of this file moved, unchanged and under the same section titles, to
`internal/service/data/docstore/NOTES.md` (ADR 0253): read the section a change touches.
It holds:
§Contents; §Why-this-shape.

## Purpose

A typed, keyed store of JSON documents with unique and multi-valued secondary
indexes, in two engines that keep one contract — and
`conformance_external_test.go` is that sentence executable: one table of rows,
each run on memory, a filesystem and the SQL store on the three dialects
(`everyStore()`, #1087):

- **`Open`** — in memory or persisted through `core/data/vfs`, every write durable
  before it returns, and a write's cost independent of how many documents the
  store holds. **ADR 0110.**
- **`OpenSQL`** — in two tables of a PostgreSQL, MySQL or SQLite database the
  caller owns, every call taking a context and running on the transaction it
  carries, nothing held in memory. **ADR 0139.**

Both keep, when their configuration's `Versions` says so, the last versions of
each document in the same durable write as the document — the file engine's
overlay entry, the SQL engine's transaction — pruned in that write, kept from
pruning by `Held`, rewritten by `RewriteVersions`. **ADR 0143.** A store that
keeps versions also has a trash — `Trash`, `Restore`, `Purge`, `Tombstones` —
where a trashed document leaves every read and keeps its versions and its
number. **ADR 0143, amended for ADR 0178 C4.** And both write a document with
its versions and its trash as they are given — `PutDocument`, for a copy from
one engine to another. **ADR 0143, amended for ADR 0178's `data copy`.** And
both say what their versions weigh — `VersionWeight`: the file engine from a
counter every write moves, which `Stats` reports too, the SQL engine from one
aggregate statement over the versions table. **ADR 0143, amended for kit's
Studio.** And both replace a document on the revision a caller read —
`Revision`, `ReplaceIf` —: the file engine's token the SHA-256 of the
document's JSON, the SQL engine's the row's `rev`. **ADR 0209.** And both
write a change record of every write to a `core/data/stream` Stream when their
configuration's `Changes` names one — the change feed, the store's CDC: the
SQL engine inside the write's transaction, the file engine before it publishes.
**kitsunium/sdk#831.**

Public facade: `pkg/v1/data/docstore`.

Code range `0.3.80.*`, shared: both engines answer the same refusals under the
same codes, and the SQL engine adds `STATEMENT_FAILED` and `KEY_TOO_LONG`;
versions add `VERSIONS_NOT_KEPT`, `VERSION_NOT_FOUND` and
`VERSIONS_REWRITE_REFUSED` to both, the trash `DOCUMENT_NOT_TRASHED`,
`PutDocument` `VERSIONS_INVALID`, `ReplaceIf` `REVISION_MISMATCH`, the change
feed `CHANGE_FEED_FAILED` (`0.3.80.25`). The codes
and sentinels, the values both engines read and return (`EntryValue`,
`VersionValue`, `StampValue`, `TombstoneValue`, `VersionWeightValue`, `RevisionValue`), the index declaration both take
(`IndexSpec`, `Unique`, `Index`, `Ordered`) and the ports are declared in
`internal/core/data/docstore` (ADR 0160): `Collection`, `Versioned` and
`Trashable`, `KeyLister`, `VersionWriter`, `VersionWeigher`, `Revisioned`,
`Batcher`, `Pager` and `Ranged` for the file engine, `CollectionContext`,
`VersionedContext`, `TrashableContext`, `KeyListerContext`,
`VersionWriterContext`, `VersionWeigherContext`, `RevisionedContext`,
`BatcherContext`, `PagerContext` and `RangedContext` for the SQL engine — two families, because the engines differ on the one thing a
single port would have to fix, a context (ADR 0139 §D2) — and `Announcer` and
`ChangeFeeder`, which both implement as they are. This package holds the engines, their
configurations and the file engine's `StatsValue` (ADR 0074).

## Do NOT

- **Persist the file engine's indexes.** Rebuild them. See above.
- **Write a document without its versions, or its versions without it** — not
  in two entries, not in two transactions. The whole promise is one write.
- **Change a `versionsRecord` once stored.** A write stores a new one; readers
  hold the old one outside the lock.
- **Remove an overlay entry outside a fold**, or fold without the writers'
  lock: the idempotent replay rests on "every entry removed is inside the
  snapshot just written". The one exception is a batch's group entry, which
  is no key's entry: `completeGroup` removes it once every key's entry holds
  what it held.
- **Publish a key's entry while a batch's group entry is on disk** — the
  group entry would undo it at the next open. `persist` settles a lingering
  batch first; a new write path goes through `persist`.
- **Apply a write before it is durable**, except `WRITE_UNCONFIRMED`, whose
  rename already happened.
- **Quote a key, an index key or a document** in an error or a field — nor a
  driver's error text, which quotes them.
- **Run a caller's function under `mu`.** Key functions run before any lock;
  `Update`'s function runs under `writing` only, so it can read the store.
- **Render SQL outside `sql_dialect.go`**, or interpolate anything but the
  validated table name. Spell no marker, quote or row lock by hand there
  either: they are `core/data/sql.Dialect`'s (`Placeholder`, `QuoteIdent`,
  `ForUpdate`), which this file used to copy.
- **Parse a driver's error** to recognise a collision: ask the database after
  the rollback. The SQL engine imports no driver (ADR 0055 §D2).
- **Run a write outside a savepoint of the caller's transaction** when the
  context carries one: a caller catching its failure would commit half of it,
  or find a PostgreSQL transaction aborted.

## Verification

```
bazel test --config=race //internal/service/data/docstore:docstore_test
# OR
cd internal/service && GOWORK=off go test -race ./data/docstore
```

The SQL engine's default-lane suite (`sql_*_external_test.go`) runs every case
on the three dialects' statements over `sqlfake_external_test.go`, a fake
engine that understands exactly those statements, with serialised
transactions, savepoints, the constraints, PostgreSQL's aborted state, the
versions table, and failures and concurrent commits injected at a named
statement. The versions have their own files: `version_external_test.go` and
`version_persist_external_test.go` (memory and file engines, the interrupted
fold, the versions file) and `sql_version_external_test.go` (every dialect).
The trash is ONE contract run on every engine — memory, file, and SQL on each
dialect — through an adapter, `trash_external_test.go`, with each engine's
own cases beside it: `trash_persist_external_test.go` (crash, fold, refused
files) and `sql_trash_external_test.go` (the row numbered 0, rollbacks).
What the versions weigh is one contract too, `weight_external_test.go`, every
step held to a recount through `Versions` and `Tombstones`.
`conformance_external_test.go` is the one table both engines answer —
`TestConformance/<row>/<engine>`, every row on memory, a filesystem and the SQL
store on each dialect: the versions' rows (numbered, stamped and pruned; a
write that changes nothing; never indexed; a deletion; a hold; a rewrite; a
store without versions; the refused configurations; contention) and the
trash's (#1087). What only one engine does stays in its own files:
`version_external_test.go` and `trash_persist_external_test.go` (the file
engine's entries and files), `sql_version_external_test.go` and
`sql_trash_external_test.go` (the rows, the statements, the hold asked inside
the transaction, the trash index's migration). `revision_external_test.go`
runs `Revision` / `ReplaceIf` on every engine (ADR 0209).
`fuzz_external_test.go` fuzzes the file engine's loader — `FuzzOpenSnapshot`,
`FuzzOpenVersions`, `FuzzReplayEntry` — over the memory filesystem, seeded
with a real store's files; under plain `go test` the seeds and
`testdata/fuzz/` run, and `go test -run '^$' -fuzz FuzzOpenSnapshot
-fuzztime 30s ./data/docstore` (each target alike) fuzzes it (ADR 0110,
amended 2026-11-02). `syncdir_external_test.go` holds the directory flushes
`Open` makes.
The same contract runs on SQLite, PostgreSQL 17 and MySQL 8.4 through real
drivers in `e2e/integration/sql`, under `-tags integration` — see its
CLAUDE.md.
