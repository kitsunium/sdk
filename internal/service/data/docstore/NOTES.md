# internal/service/data/docstore — notes

Reference moved out of `CLAUDE.md` (ADR 0253), unchanged and under the same section titles.

## Contents

| File | Surface |
|---|---|
| `doc.go` | the package comment — kit writes it from the design (ADR 0167) |
| `docstore.go` | `Store[T]` (its `weight`, the versions' counter), `StatsValue` (`Versions`, that counter); the reads — `Get`, `GetInto` (into the caller's value, zeroed first: #813), `List`, `Filter`, `Entries`, `Keys` (the keys above `after`, taken and sorted once under the read lock when the range starts — the map's own strings, no document copied: ADR 0178) and `yieldKeys` (shared: the keys, then a failure once as the last yield), `Lookup`, `Find`, `Stats`; `decode` → `decodeAs` → `decodeInto` (shared; a failed decode leaves the value zero), `jsonCause` (a decoding failure described without a byte of the document: `fieldPath` resolves `encoding/json`'s path against the decoded type and writes every map key on it — a store key, a metadata key — as `[key]`, #1061) |
| `open.go` | `Open[T](Config[T], ...IndexSpec[T])`; `newStore`; `open` — load, rebuild, THEN fold, so a refused open writes no data |
| `config.go` | `Config[T]` (`Key`, `FS`, `Clock`, `Held`, `Path`, `FoldAt`, `Versions`, `Changes`, `ChangesWithDocument`), `DefaultFoldAt`; the refusals (`StoreMisconfigured`), `validateIndexes` — over `core/data/docstore.IndexSpec` — and `validateVersions` (shared) |
| `write.go` | `Put` / `Insert` / `Replace` (the three write modes), `Update`, `Delete`; `prepare` → `encodeAs` (shared, outside every lock; a store key or index key that is not valid UTF-8 is refused `DOCUMENT_UNENCODABLE` by `validKeys` / `invalidKey`, shared with the SQL engine's `prepare` / `indexRows` — JSON would rewrite it into another key, which the reopened store or `Reindex` files elsewhere, #459) → `commit` / `modify` / `remove` (under the writers' lock) → `persistAndApply`, which computes the versions a write leaves before it persists anything; `encodeCause` |
| `version.go` | the file engine's `PutStamped` / `InsertStamped` / `ReplaceStamped` / `UpdateStamped`, `Versions`, `Version`, `RewriteVersions`; `versionsRecord` (a record is never changed once stored), `nextVersions` → `pruned` (asks `Held` only when something would go), `checkRewrite` and `pickVersion` (shared), `compactJSON`; the file engine's `PutDocument` (`preparedDocument`, `persistDocument`, `recordOf`) and, shared by both engines, `checkDocument` → `versionProblem`, `documentJSON`, `metaOrNil` and the `VERSIONS_INVALID` problems (ADR 0178); `setVersions` — the one place a key's record changes, which moves `weight` by what the new record weighs less what the old one did (`versionsRecord.weighed`: its former versions, its trashed document, their bytes) — and `VersionWeight`, that counter read under the read lock |
| `trash.go` | the file engine's `Trash` / `Restore` / `Purge` / `Tombstones`: a trashed document is its versions record with `Trashed` (`trashedDocument`: the document and the instant), published as one deletion entry that keeps versions; `trashedRecord`, `notTrashed` (shared) |
| `index.go` | the index maps (`entries`, `owned`, and an ordered index's `sorted` list of (key, owner), kept by binary search and built once at `Open` by `resort`), `fileableKeys` (shared: the empty key is no key, a repeated one is filed once), `uniqueTaken`, `file` / `unfile`, `rebuild` on open: every document decoded, checked against its own `Key` (`LOAD_FAILED` otherwise, naming the file via `origin`) and filed; a broken unique index or a panicking key function refuses the open |
| `persist.go` | the files: `entryName` (SHA-256 of the key), `persist` (one overlay entry per write, the document and its versions), `publish` (PersistFailed vs WriteUnconfirmed), `maybeFold`, `Fold`, `Close`, `fold` / `foldVersions` (the versions file, then the snapshot) / `removeFolded` / `recordFold`, `encodeSnapshot` |
| `load.go` | `load`: the directories, `readSnapshot`, `replayOverSnapshot` → `readVersions` (refused by a store that keeps none), `readOverlay` / `replay` / `checkEntryVersions`, `versionsWithoutDocument`, `checkRecord`, `compacted`, the crash leftovers removed; `replayGroup` — a batch's group entry a crash left, replayed over the keys' entries through `applyRecord`, which `replay` shares; `makeDir` (the directories a `MkdirAll` created) and `syncCreated` (`SyncDir` on the parent of each, deepest first, through `vfs.DirSyncer` when the filesystem has it — ADR 0110, amended 2026-11-02); it reports whether `open` must fold, and folds nothing itself |
| `batch.go` | the file engine's `Batch` (ADR 0209, amended 2026-11-02): `batchWriter` (each write prepared at once), `commitBatch` — `settleGroup`, `planBatch` over a `batchView` (the store, and the batch's own writes over it; unique keys freed and taken; versions by `nextVersionsFrom`), `publishGroup` (ONE group entry, `batch.group.json`, every written key's final state), `applyBatch`, `completeGroup` (the keys' entries, then the group entry removed); a batch `completeGroup` could not finish is `lingering`, completed by `settleGroup` before anything else is published |
| `page.go` | the file engine's `Documents` (pages of `documentsPageLen` = 256 under the read lock, chosen by `smallestAfter` in one pass keeping at most twice the page — never a sort of the store), `FindAfter`, `Range` (an ordered index's `between`); shared: `pager` (what a paged listing has left), `yieldEntries`, `unordered` (`INDEX_UNKNOWN` / `INDEX_NOT_ORDERED`) — ADR 0209, amended 2026-11-03 (#836) |
| `sql_page.go` | the SQL engine's `Documents` (Reindex's `pageAfter`), `FindAfter` (`findAfter`), `Range` (`rangeFrom` / `rangeBelow`, resumed on a `rangeCursor` — the last index key and document key read); each page read whole and its rows closed before it is yielded, decoded as yielded |
| `sql_batch.go` | the SQL engine's `Batch`: `sqlBatchWriter`, then every write through `writeIn` in one transaction (`run(ctx, true, …)`), the hooks after the commit |
| `revision.go` | the file engine's `Revision` / `ReplaceIf` (`commitIf`: the comparison under the writers' lock, then `persistAndApply` as `Replace`); `revisionOf`, the SHA-256 of the document's JSON compacted and HTML-escaped as `json.Marshal` writes it — nothing persisted, so a fold, a reopen and the memory store keep it (ADR 0209) |
| `change.go` | the change feed (#831), shared: `changeFeed` (the stream and `withDocument`), `record` (keyed by the document's key, the `core/data/docstore.ChangeValue` as JSON), `append` (`CHANGE_FEED_FAILED` joined to the stream's error), `validateChanges`; the file engine's `changeVersion`, `feed` (one record under the writers' lock before `persist`) and `feedBatch` (one Append per key a batch writes, before the group entry); the SQL engine's `feedIn` (the version read by `readHead` in the write's transaction, the record appended with the write's context); both `ChangeStream` |
| `hooks.go` | `OnWrite` / `OnDelete`, called with the key after the write is durable (file engine) or committed (SQL engine), outside every lock |
| `sql_config.go` | `SQLConfig[T]` (`Key`, `Clock`, `Held`, `Transactor`, `IndexKey`, `Table`, `Dialect`, `Versions`, `Changes`, `ChangesWithDocument`), `MaxSQLTableLen`, `MaxSQLKeyLen`, `MaxSQLIndexNameLen`, the table-name rule (`isTableName`, written out rather than a regular expression compiled at initialisation, held to `^[a-z_][a-z0-9_]*$` by `table_internal_test.go` — #276), `indexTable`, `versionsTable`, the refusals |
| `sql_dialect.go` | **the only place the SQL engine renders SQL**: every statement per dialect, rendered once at `OpenSQL` (`sqlStatements`: the claim, the version rows' reads under `Dialect.ForUpdate`, the pruning, `keysAfter` — a page of keys alone, for `Keys` — and `trashStatements`' four: the key's lowest row, the trash row's removal, a trashed document's versions, the trash; and `versionWeight`, one aggregate over the versions table — former rows, trash rows, `LENGTH(doc)` of the binary documents —, for `VersionWeight`), `revisionStatements` (`revision`, `replaceIf`, `lockDocIf` — ADR 0209), the five rendered per call (a write's index rows and unique-key check, its version rows, `Reindex`'s shared-key check and unique marking), the DDL of the three tables and of the trash's index (`trashIndexStatements`, `trashIndexExistsStatement`); every marker, quoted name and row lock is `core/data/sql.Dialect`'s, and `shareLockClause` — MySQL's `LOCK IN SHARE MODE`, the one clause whose choice is this store's isolation reasoning rather than an engine's grammar — is the store's own |
| `sql_store.go` | `OpenSQL`, `SQLStore[T]`; the reads — `Get`, `GetInto`, `List`, `Filter`, `Entries`, `Keys` (pages of `keysPageLen` = 256 keys, each one `keysAfter` statement on the primary key resuming after the last key read, on the context's transaction or the pool; no document read, and no trashed one listed, since the trash is a version row — ADR 0178), `Count`, `Lookup`, `Find`; `OnWrite` / `OnDelete`; `failed` (STATEMENT_FAILED + the driver's error through `service/data/sql`'s `Withheld`, its text out of every rendering) |
| `sql_write.go` | `Put` / `Insert` / `Replace`, `Update`, `UpdateStamped`, `Delete`; `run` (a savepoint of the caller's transaction, a transaction of the store's own, or one statement), `several`, `claimRow` / `lockedReplace` (a store that keeps versions reads what it replaces first), `announce` (hooks through `Deferrer`), the unique check, `mayConflict` and `classify` (a raced collision asked about after the rollback) |
| `sql_version.go` | the SQL engine's `PutStamped` / `InsertStamped` / `ReplaceStamped`, `Versions` (one LEFT JOIN, and a second for a trashed document), `readVersionRows`, `Version`, `RewriteVersions`; `keepVersions` (the version rows a write leaves, in its transaction), `keepCreation` (a creation's: version 1, after a leftover's rows, or a write over a trashed document), `readHead`, `prune`, `insertVersionRows` (150 a statement), `versionOf`; the SQL engine's `PutDocument` (`preparedDocument`, `putDocument`, `keyTaken` — the key's lowest version row and its document's row, both read locked); `VersionWeight` — the `versionWeight` statement, one row, on the context's transaction or the pool |
| `sql_trash.go` | the SQL engine's `Trash` / `Restore` / `Purge` / `Tombstones`: a trashed document is the version row numbered 0 (`tombstoneNum`); `firstVersion` — the key's lowest row, locked — which a creation, `Restore` and `Purge` read |
| `sql_revision.go` | the SQL engine's `Revision` (the `revision` statement: the row's `rev` and document) and `ReplaceIf` — `write` in the mode `replaceOnRevision`: `replaceIf`, Replace's UPDATE on `rev = ?`, or `lockDocIf` where versions are kept; `revisionRefused` asks `exists` which refusal a miss is; a token it never gave asks for revision 0, which no row has (ADR 0209) |
| `sql_reindex.go` | `Reindex`: every index row rebuilt from the documents, a page at a time, `INDEX_BROKEN` for a unique key two documents share |
| `sql_migration.go` | `SQLMigration`: the two tables as one idempotent `core/data/sql` migration the caller numbers; `SQLVersionsMigration`: the versions table, beside it; `SQLVersionsTrashIndexMigration`: the trash's index `<table>___tr`, after it, for every table — MySQL's two directions through `whenTrashIndex`, which asks `information_schema` first (ADR 0143, amended 2026-11-02) |
| `docstore_compliance.go` | the compile-time assertions that `Store[T]` implements `core/data/docstore.Collection`, `Versioned`, `Trashable`, `KeyLister`, `VersionWriter` and `VersionWeigher`, `SQLStore[T]` `CollectionContext`, `VersionedContext`, `TrashableContext`, `KeyListerContext`, `VersionWriterContext`, `VersionWeigherContext` and `RevisionedContext`, and both `Announcer` and `ChangeFeeder`; `Store[T]` also `Revisioned` |
| `BENCH.md` | what a write costs as the file store grows, against the whole-file rewrite it replaces |

## Why-this-shape

- **`GetInto` is `Get` into a value the caller keeps, and the copy stays the contract** (#813). It sets `*dst` to T's zero value before decoding, because `encoding/json` decoding into a value MERGES: a field the stored JSON omits (`omitempty`) would keep the earlier document's value, a map would keep its entries, and a slice or pointer `dst` held would be written through. Zeroed first, `dst` ends equal to what `Get` returns and shares nothing with the store or with what it held; what is saved is the fresh T `Get` heap-allocates (`json.Unmarshal` takes `&v` as an `any`), 7 allocations a read of the bench's account against 5. A failed decode leaves `dst` zero, never half a document. The budget `allocs: 5` on `(*Store[...]).GetInto` (`design/sdk.yaml`, fixture `perfGetInto` in `perf_fixtures_test.go`, race off) holds it; `TestGetIntoIsGetInTheCallersValue` and `TestSQLGetIntoIsGetInTheCallersValue` hold the semantics.
- **One snapshot at rest, one overlay entry per write.** The whole-file store
  this replaces rewrote every document on every write — measured here at
  70.3 ms per write at 100 000 documents. A write now publishes ONE small file
  (the key and its document, or its deletion) and costs the same at a hundred
  documents or a hundred thousand; the overlay is folded into the snapshot when
  it holds as many entries as the store holds documents (at least
  `DefaultFoldAt`), so the fold's O(N) is paid once per N writes. `Close` folds,
  so a closed store is ONE file a person can read — the bare `{key: document}`
  object a framework's existing files already are.
- **Replay is order-free and idempotent, by construction.** An entry holds its
  key's WHOLE latest state, and the entry's name is the key's digest, so there
  is at most one entry per key. An entry the last fold already contains holds
  exactly what the snapshot holds. Hence: a crash after the snapshot and before
  the removals, or between two removals, or a removal the filesystem never made
  durable, all load the same documents — `TestAFoldInterruptedAnywhereLoadsTheSameDocuments`.
  The fold holds the writers' lock throughout, which is what makes "an entry it
  removes holds what the snapshot holds" true.
- **Two locks, and readers never wait for a disk.** `writing` serialises
  writers and is held across the filesystem work; `mu` guards the maps and is
  taken only to apply a write that is already durable. A reader sees the state
  before a write or after it — `TestReadersDoNotWaitForTheDisk` holds a
  publication at a gate and reads meanwhile.
- **Two verdicts for a failed publication.** `PERSIST_FAILED`: nothing changed,
  memory and disk agree. `WRITE_UNCONFIRMED`: vfs's `DirectorySyncFailed` —
  the rename happened, so the write TOOK EFFECT and the store applies it (the
  file shows it; a store that disagreed with its own files would be worse); only
  its survival across a power loss is in doubt. A failed automatic fold does not
  fail the write that triggered it — that write's entry was already durable — and
  is kept in `Stats().FoldError`; the entries stay until a fold works.
- **Indexes are rebuilt, never persisted — by the file engine.** They are
  derived data, and a derived file that could disagree with its source would
  need a repair path. A unique index the documents break refuses the OPEN
  (`INDEX_BROKEN`): a unique index that does not hold would be a lie every
  `Lookup` tells. The fold at open comes after the rebuild, so a refused open
  leaves the snapshot an operator has to repair byte for byte —
  `TestARefusedOpenWritesNothing`.
- **A store opens only over documents it can serve.** Every document is
  decoded at open, with or without indexes, and must sit under the key its own
  `Key` gives: a document filed under another key would be served under one
  identity while `Update` refused it as a rename. A snapshot that is JSON
  `null` is refused too — it decodes into no map without an error.
- **The key never reaches an error.** A store key is routinely an e-mail
  address; an index key routinely the hash of a token. Every refusal names the
  store and the index, never a key, and a decoding failure names the field and
  the Go type — never the offending value, whose digits encoding/json quotes.
  The SQL engine WITHHOLDS a driver's words, which quote the row a constraint
  refused: `STATEMENT_FAILED` joins the driver's error for `errors.As` and
  renders only its Go type and its SQLSTATE or code.
- **Each engine states its consistency** (#836), in the ports' and the
  facade's docs: the file engine is read-your-writes within the process,
  readers never waiting; the SQL engine reads at the isolation of the
  transaction a call runs on — a write visible to its own transaction at
  once, to another once committed, a REPEATABLE READ caller reading its
  snapshot. `TestAWriteIsReadBackByItsOwnTransaction` and
  `TestAnUncommittedWriteIsInvisibleToAnotherTransaction`
  (`sql_tx_external_test.go`) hold the SQL half.
- **No context — in the file engine.** Nothing there can be abandoned: the
  waits are the writers' mutex and a device flush, and a flush a caller walked
  away from still happens. Every SQL call takes one, because it waits on a
  database.

### The SQL engine (ADR 0139)

- **Two tables, binary keys, the document as written.** `<table>` holds
  `doc_key`, `rev` and `doc`; `<table>___ix` holds one row per index key. Every
  key column is binary and every key is bound as `[]byte`, so `a`, `A` and `a␠`
  are three keys in Go's order; the document is the bytes encoded, never the
  engine's JSON type. `rev` moves on every write, which makes MySQL's
  affected-row count exact. Versions are numbered apart from it, in their own
  table: a write that makes no version moves `rev` too (ADR 0143). A unique
  index's rows carry `uniq = 1` and the others NULL, so one
  `UNIQUE (index_name, index_key, uniq)` guards every unique index across
  transactions and processes.
- **Every call runs where its context says** — `core/data/sql.Joiner`. Inside the
  caller's transaction every write is a savepoint of it, so a refused or failed
  write undoes itself alone and a PostgreSQL transaction is not left aborted.
  Outside one, a write of several statements runs in a transaction of the
  store's own, READ COMMITTED on MySQL because REPEATABLE READ's gap locks
  deadlock two writers of neighbouring new keys, and a write of one statement
  runs on the pool. Its hooks go through `core/data/sql.Deferrer`, after the commit
  and never for a rollback. Unique keys are checked 400 at a time and index
  rows written 200 at a time, under the oldest SQLite's bound-parameter
  ceiling.
- **A refusal is the database's answer, never a parsed error.** `Insert` over a
  taken key affects no row on PostgreSQL and SQLite (`ON CONFLICT DO NOTHING`),
  a unique key held elsewhere is read before the rows are written, and a
  statement the engine's constraint refused — MySQL's plain `INSERT` over a
  taken key, or a collision raced in — rolls the write back, after which the
  store asks what now holds the key (`LOCK IN SHARE MODE` on MySQL, past a
  REPEATABLE READ snapshot).
- **Update locks before it reads**: `FOR UPDATE` on PostgreSQL and MySQL; on
  SQLite the read IS a write — `UPDATE … RETURNING` — because SQLite's lock is
  taken by a transaction's first write.
- **Index rows are kept**, so a changed declaration needs `Reindex`, which
  refuses `INDEX_BROKEN` for a unique key two documents share. `IndexKey`
  stores index keys transformed — a keyed hash — at every write, `Lookup` and
  `Find`.

### Versions (ADR 0143)

- **The current version is the document.** A document's versions are its
  current one — the document itself, with the number, instant and metadata of
  the write that made it — and up to `Versions` former ones, newest first. A
  creation is version 1, every write that changes the document the next; a
  write storing the JSON already stored, or stamped `InPlace`, makes none and
  the document keeps its current version's header. A document stored before
  its store kept versions is version 1 at an unknown instant.
- **Versions travel in the document's own write.** File engine: the overlay
  entry holds the key's document AND its versions, pruned, so one atomic
  publication carries both; the fold writes the versions file, then the
  snapshot, and removes the entries only after both — a key without an entry
  is the same in the old files as in the new, so a crash anywhere loads the
  same documents with the same versions. SQL engine: the version rows are
  statements of the write's own transaction or savepoint.
- **A creation starts a new history.** On the SQL engine it deletes whatever
  version rows its key holds before it writes version 1: rows a document of
  the same key left while the store kept no versions are not its history.
- **An instant is kept to the nanosecond.** The file engine writes it as
  RFC 3339 text (years 0 to 9999; a write stamped outside them fails
  `PERSIST_FAILED`); the SQL engine keeps seconds and nanoseconds in two
  columns, `made_at` and `made_ns`, which hold any instant a `time.Time`
  does.
- **A Put reads what it replaces.** On a store that keeps versions, the SQL
  engine's Put starts with a claim — an upsert that leaves the stored document
  as it is and moves its revision — which creates the key or locks the row in
  one statement, so no writer lands between the read and the write.
- **The version rows are read locked inside a write** (`FOR UPDATE` on
  PostgreSQL and MySQL), because a plain read inside a caller's REPEATABLE READ
  transaction on MySQL answers from its snapshot.
- **`Held` is asked only when something would be pruned**, under the writers'
  lock or inside the write's transaction, with its context.
- **A store without versions writes byte for byte what it wrote before.** The
  file engine refuses (`LOAD_FAILED`) files that keep versions rather than
  drop them at its next fold; the SQL engine cannot see a versions table it was
  not told about, so turning versions off there is the migration's Down.

### The trash (ADR 0143, amended for ADR 0178)

- **Only a store that keeps versions has one**; the others answer
  `VERSIONS_NOT_KEPT`. kit keeps a trash for stores with `Revisions(n)` only.
- **A trashed document is a former version without a current one**: gone from
  every read, unfiled — its unique keys free, so `Restore` may meet
  `UNIQUE_KEY_TAKEN` — and its versions, read by `Versions`, untouched.
- **Its number is kept.** A creation under a trashed key is a write over the
  trashed document (`nextVersions`, `keepVersions`): it continues the
  numbering and empties the trash. `Restore` makes no version.
- **`Delete` never reaches the trash**, and finds no trashed document:
  emptying the trash of one is `Purge`. The SDK has no idea what privacy is;
  kit routes its erasures to `Delete` and `Purge`.
- **No age, no sweep.** `Tombstones` answers each key and the instant it was
  trashed; the caller purges.
- **File engine**: `trashed: {at, doc}` in the key's versions record, carried by
  a deletion entry that keeps versions — the one deletion that may — and by the
  versions file; the loader refuses a trash beside the same key's document.
- **SQL engine**: the row numbered 0 of `<table>___vs`, holding the document and
  the instant in `made_at` / `made_ns` — no migration to keep one, and one to
  find them fast: `SQLVersionsTrashIndexMigration` indexes the rows numbered
  0 (`<table>___tr`), which `Tombstones` otherwise reads by scanning every
  version (ADR 0143, amended 2026-11-02). A creation reads the
  key's lowest row (`firstVersion`) where it used to delete them all: number 0
  is the trash, another number a leftover of ADR 0143 D7, deleted as before.

### A document written as it lies (ADR 0143, amended for ADR 0178)

- **`PutDocument(key, versions, trashedAt)`** writes a document with its
  versions, newest first, each kept as given — number, instant, metadata, JSON
  compacted and HTML-escaped as `json.Marshal` writes one — and, with
  `trashedAt` set, puts it in the trash at that instant. It is what kit's
  `data copy` writes a store with from the data directory into a database: no
  other write can keep a number or an instant, since a write numbers its
  version after the current one and stamps it with the store's clock.
- **Nothing may be under the key** — a document, a trashed one, or version rows
  (`DOCUMENT_EXISTS`). **`checkDocument`, shared**, refuses identically on both
  engines what no store keeps as given (`VERSIONS_INVALID`, `problem` naming
  which): no version, a number 0, numbers that do not strictly decrease (gaps
  are allowed: pruning, a hold and a rewrite leave them), an unknown instant on
  a version other than 1, a version that is not JSON, a current document whose
  own key is another. `DOCUMENT_UNDECODABLE` for one the type does not decode,
  `UNIQUE_KEY_TAKEN` for a stored one whose unique key is held — a trashed one
  is filed nowhere, as `Trash` leaves it.
- **It asks `Held` nothing and prunes nothing**: the next write prunes, as every
  write does. A stored document calls `OnWrite`; a trashed one, no hook.
- **File engine**: one entry, as `Trash` publishes one for a trashed document
  (a deletion entry that keeps versions). **SQL engine**: one transaction — the
  row, the index rows and the version rows, the trash's numbered 0 —, reusing
  the statements `Restore` and `Trash` send: no new SQL.
- `putdocument_external_test.go` runs the contract on every engine — memory,
  file, and SQL on each dialect — plus a reopen of the file store before and
  after a fold.

### What the versions weigh (ADR 0143, amended for kit's Studio)

- **`VersionWeight()`** answers `VersionWeightValue{Former, Trashed, Bytes}`:
  the former versions every document keeps — a trashed one's included —, the
  documents in the trash, and the length of the JSON they hold. A current
  version is the document and counts in none. A store keeping no versions
  answers `VERSIONS_NOT_KEPT`.
- **File engine: a counter, not a scan.** `setVersions` is the one place a
  key's record changes — every write, a trash, a rewrite, a load — and moves
  `weight` by what the new record weighs less what the old one did; a record
  never changes once stored, so both are exact. `VersionWeight` and `Stats`
  read it under the read lock: 20-30 ns whatever the store holds, where
  reading every document's versions costs 3 ms at 1 000 documents of ten
  versions and 60-100 ms at 10 000. A write pays one loop over the versions it
  already built: no allocation more, within noise (`BenchmarkPutVersioned`).
- **SQL engine: one aggregate.** `COUNT` of the former rows (`num > 0` holding
  a document), of the trash rows (`num = 0`), and `SUM(LENGTH(doc))` — `doc`
  is binary on every engine, so `LENGTH` counts bytes, and a current version's
  NULL counts in nothing. The database scans the versions table and sends one
  row: on SQLite 5 ms at 11 000 rows and 53 ms at 110 000, against 130 ms and
  1.3 s for a recount through `Versions`. A D7 leftover — rows a document
  deleted while the store kept no versions left behind — weighs what it holds.
- `weight_external_test.go` runs the contract on every engine — each step's
  weight recomputed from `Versions` and `Tombstones` —, the file engine's
  counter in `Stats`, through a reopen after a crash and after a fold, under
  concurrent writers, and the SQL statement on the context's transaction.

### The change feed (kitsunium/sdk#831)

- **One record per write that stands**, keyed by the document's key — so the
  stream's key hash keeps a document's changes in one partition, in order —
  its payload a `core/data/docstore.ChangeValue` as JSON: `op` (`put`,
  `delete`, `trash`, `restore`), `key`, `version` (the document's current
  version once the write stood; 0 for a delete and on a store that keeps no
  versions; 1 for a document stored before versions were kept), `at` (the
  store's clock) and, with `ChangesWithDocument`, `doc`. `Purge` is a
  `delete`; `RewriteVersions` changes no document and records nothing; a
  refused write records nothing.
- **SQL engine: in the write's transaction.** `feedIn` runs at the end of each
  write's work — `writeIn`, `modify`, `remove`, `trash`, `restore`, `purge`,
  `putDocument` — inside its savepoint, and appends with the write's context:
  the stream's SQL engine on the same `Transactor` joins that transaction, so
  a write rolled back leaves no record and a committed one exactly one; a
  stream on another transactor would commit apart, which the store cannot see
  and its doc says. With `Changes`, `several()` is true: even a one-statement
  write runs in a transaction. A batch appends per write.
- **File engine: before the publication.** `feed` runs under the writers'
  lock before `persist` (and `feedBatch` before the group entry), so a crash
  or a refused publication between them leaves a record of a write that did
  not land — at least once; a reader skips a record whose version the store
  does not hold, and replays by (key, version). A stream's `Append` takes one
  partition's records, so a batch appends one record per key, one Append
  each, its final state. The engine takes no context, so the record is
  appended under `context.Background()`.
- **A refused record refuses the write**: `CHANGE_FEED_FAILED`, the stream's
  error joined for `errors.Is`, nothing published or committed.
- **Composition**: the package imports the `core/data/stream` port only; its
  tests reach `service/data/stream` (the memory engine, `Subscribe`), and
  `e2e/integration/sql` runs the SQL engine's feed on the stream's SQL engine.
- `change_external_test.go` holds it: every write of the file engine recorded
  in order with its version; a feed replayed over an empty store giving a
  snapshot equal byte for byte; a refused publication leaving its record and
  a refused record leaving no document; and on each SQL dialect, a write in a
  rolled-back transaction leaving no record, committed writes one each, read
  by a consumer group. `TestConformance/EveryWriteAppendsOneChangeRecord` is
  the same sequence on every engine.
