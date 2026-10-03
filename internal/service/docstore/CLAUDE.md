<!-- updated: 2026-10-03T00:24:48Z -->
# internal/service/docstore/

## Purpose

A typed, keyed store of JSON documents with unique and multi-valued secondary
indexes, in two engines that keep one contract:

- **`Open`** — in memory or persisted through `core/vfs`, every write durable
  before it returns, and a write's cost independent of how many documents the
  store holds. **ADR 0110.**
- **`OpenSQL`** — in two tables of a PostgreSQL, MySQL or SQLite database the
  caller owns, every call taking a context and running on the transaction it
  carries, nothing held in memory. **ADR 0139.**

Both keep, when their configuration's `Versions` says so, the last versions of
each document in the same durable write as the document — the file engine's
overlay entry, the SQL engine's transaction — pruned in that write, kept from
pruning by `Held`, rewritten by `RewriteVersions`. **ADR 0143.**

Public facade: `pkg/v1/docstore`.

Code range `0.3.80.*`, shared: both engines answer the same refusals under the
same codes, and the SQL engine adds `STATEMENT_FAILED` and `KEY_TOO_LONG`;
versions add `VERSIONS_NOT_KEPT`, `VERSION_NOT_FOUND` and
`VERSIONS_REWRITE_REFUSED` to both. No core counterpart: the two engines
differ on the one thing a port would have to fix — a context, and a database
that can fail — so the values are the engines' (ADR 0074, ADR 0139 §D2).

## Contents

| File | Surface |
|---|---|
| `docstore.go` | package doc, `Store[T]`, `EntryValue`, `StatsValue`; the reads — `Get`, `List`, `Filter`, `Entries`, `Lookup`, `Find`, `Stats`; `decode` → `decodeAs` (shared), `jsonCause` (a decoding failure described without a byte of the document) |
| `open.go` | `Open[T](Config[T], ...IndexSpec[T])`; `newStore`; `open` — load, rebuild, THEN fold, so a refused open writes no data |
| `config.go` | `Config[T]` (`Key`, `FS`, `Clock`, `Held`, `Path`, `FoldAt`, `Versions`), `IndexSpec[T]`, `Unique`, `Index`, `DefaultFoldAt`; the refusals (`StoreMisconfigured`), `validateIndexes` and `validateVersions` (shared) |
| `write.go` | `Put` / `Insert` / `Replace` (the three write modes), `Update`, `Delete`; `prepare` → `encodeAs` (shared, outside every lock) → `commit` / `modify` / `remove` (under the writers' lock) → `persistAndApply`, which computes the versions a write leaves before it persists anything; `encodeCause` |
| `version.go` | `VersionValue`, `StampValue` (shared); the file engine's `PutStamped` / `InsertStamped` / `ReplaceStamped` / `UpdateStamped`, `Versions`, `Version`, `RewriteVersions`; `versionsRecord` (a record is never changed once stored), `nextVersions` → `pruned` (asks `Held` only when something would go), `checkRewrite` and `pickVersion` (shared), `compactJSON` |
| `index.go` | the index maps (`entries`, `owned`), `fileableKeys` (shared: the empty key is no key, a repeated one is filed once), `uniqueTaken`, `file` / `unfile`, `rebuild` on open: every document decoded, checked against its own `Key` (`LOAD_FAILED` otherwise, naming the file via `origin`) and filed; a broken unique index or a panicking key function refuses the open |
| `persist.go` | the files: `entryName` (SHA-256 of the key), `persist` (one overlay entry per write, the document and its versions), `publish` (PersistFailed vs WriteUnconfirmed), `maybeFold`, `Fold`, `Close`, `fold` / `foldVersions` (the versions file, then the snapshot) / `removeFolded` / `recordFold`, `encodeSnapshot` |
| `load.go` | `load`: the directories, `readSnapshot`, `replayOverSnapshot` → `readVersions` (refused by a store that keeps none), `readOverlay` / `replay` / `checkEntryVersions`, `versionsWithoutDocument`, `checkRecord`, `compacted`, the crash leftovers removed; it reports whether `open` must fold, and folds nothing itself |
| `hooks.go` | `OnWrite` / `OnDelete`, called with the key after the write is durable (file engine) or committed (SQL engine), outside every lock |
| `sql_config.go` | `SQLConfig[T]` (`Key`, `Clock`, `Held`, `Transactor`, `IndexKey`, `Table`, `Dialect`, `Versions`), `MaxSQLTableLen`, `MaxSQLKeyLen`, `MaxSQLIndexNameLen`, the table-name rule, `indexTable`, `versionsTable`, the refusals |
| `sql_dialect.go` | **the only place the SQL engine renders SQL**: every statement per dialect, rendered once at `OpenSQL` (`sqlStatements`: the claim, the version rows' reads under `Dialect.ForUpdate`, the pruning), the five rendered per call (a write's index rows and unique-key check, its version rows, `Reindex`'s shared-key check and unique marking), the DDL of the three tables; every marker, quoted name and row lock is `core/sql.Dialect`'s, and `shareLockClause` — MySQL's `LOCK IN SHARE MODE`, the one clause whose choice is this store's isolation reasoning rather than an engine's grammar — is the store's own |
| `sql_store.go` | `OpenSQL`, `SQLStore[T]`; the reads — `Get`, `List`, `Filter`, `Entries`, `Count`, `Lookup`, `Find`; `OnWrite` / `OnDelete`; `failed` (STATEMENT_FAILED + the driver's error through `service/sql`'s `Withheld`, its text out of every rendering) |
| `sql_write.go` | `Put` / `Insert` / `Replace`, `Update`, `UpdateStamped`, `Delete`; `run` (a savepoint of the caller's transaction, a transaction of the store's own, or one statement), `several`, `claimRow` / `lockedReplace` (a store that keeps versions reads what it replaces first), `announce` (hooks through `Deferrer`), the unique check, `mayConflict` and `classify` (a raced collision asked about after the rollback) |
| `sql_version.go` | the SQL engine's `PutStamped` / `InsertStamped` / `ReplaceStamped`, `Versions` (one LEFT JOIN), `Version`, `RewriteVersions`; `keepVersions` (the version rows a write leaves, in its transaction), `readHead`, `prune`, `insertVersionRows` (150 a statement), `versionOf` |
| `sql_reindex.go` | `Reindex`: every index row rebuilt from the documents, a page at a time, `INDEX_BROKEN` for a unique key two documents share |
| `sql_migration.go` | `SQLMigration`: the two tables as one idempotent `core/sql` migration the caller numbers; `SQLVersionsMigration`: the versions table, beside it |
| `codes.go` / `errors.go` | `0.3.80.1`–`0.3.80.20` |
| `BENCH.md` | what a write costs as the file store grows, against the whole-file rewrite it replaces |

## Why-this-shape

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
- **Every call runs where its context says** — `core/sql.Joiner`. Inside the
  caller's transaction every write is a savepoint of it, so a refused or failed
  write undoes itself alone and a PostgreSQL transaction is not left aborted.
  Outside one, a write of several statements runs in a transaction of the
  store's own, READ COMMITTED on MySQL because REPEATABLE READ's gap locks
  deadlock two writers of neighbouring new keys, and a write of one statement
  runs on the pool. Its hooks go through `core/sql.Deferrer`, after the commit
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

## Do NOT

- **Persist the file engine's indexes.** Rebuild them. See above.
- **Write a document without its versions, or its versions without it** — not
  in two entries, not in two transactions. The whole promise is one write.
- **Change a `versionsRecord` once stored.** A write stores a new one; readers
  hold the old one outside the lock.
- **Remove an overlay entry outside a fold**, or fold without the writers'
  lock: the idempotent replay rests on "every entry removed is inside the
  snapshot just written".
- **Apply a write before it is durable**, except `WRITE_UNCONFIRMED`, whose
  rename already happened.
- **Quote a key, an index key or a document** in an error or a field — nor a
  driver's error text, which quotes them.
- **Run a caller's function under `mu`.** Key functions run before any lock;
  `Update`'s function runs under `writing` only, so it can read the store.
- **Render SQL outside `sql_dialect.go`**, or interpolate anything but the
  validated table name. Spell no marker, quote or row lock by hand there
  either: they are `core/sql.Dialect`'s (`Placeholder`, `QuoteIdent`,
  `ForUpdate`), which this file used to copy.
- **Parse a driver's error** to recognise a collision: ask the database after
  the rollback. The SQL engine imports no driver (ADR 0055 §D2).
- **Run a write outside a savepoint of the caller's transaction** when the
  context carries one: a caller catching its failure would commit half of it,
  or find a PostgreSQL transaction aborted.

## Verification

```
bazel test --config=race //internal/service/docstore:docstore_test
# OR
cd internal/service && GOWORK=off go test -race ./docstore
```

The SQL engine's default-lane suite (`sql_*_external_test.go`) runs every case
on the three dialects' statements over `sqlfake_external_test.go`, a fake
engine that understands exactly those statements, with serialised
transactions, savepoints, the constraints, PostgreSQL's aborted state, the
versions table, and failures and concurrent commits injected at a named
statement. The versions have their own files: `version_external_test.go` and
`version_persist_external_test.go` (memory and file engines, the interrupted
fold, the versions file) and `sql_version_external_test.go` (every dialect).
The same contract runs on SQLite, PostgreSQL 17 and MySQL 8.4 through real
drivers in `third-party/db/sql`, under `-tags integration` — see its
CLAUDE.md.
