<!-- updated: 2026-09-28T19:19:15Z -->
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

Public facade: `pkg/v1/docstore`.

Code range `0.3.80.*`, shared: both engines answer the same refusals under the
same codes, and the SQL engine adds `STATEMENT_FAILED` and `KEY_TOO_LONG`. No
core counterpart: the two engines differ on the one thing a port would have to
fix — a context, and a database that can fail — so the values are the
engines' (ADR 0074, ADR 0139 §D2).

## Contents

| File | Surface |
|---|---|
| `docstore.go` | package doc, `Store[T]`, `EntryValue`, `StatsValue`; the reads — `Get`, `List`, `Filter`, `Entries`, `Lookup`, `Find`, `Stats`; `decode` → `decodeAs` (shared), `jsonCause` (a decoding failure described without a byte of the document) |
| `open.go` | `Open[T](Config[T], ...IndexSpec[T])`; `newStore`; `open` — load, rebuild, THEN fold, so a refused open writes no data |
| `config.go` | `Config[T]` (`Key`, `FS`, `Path`, `FoldAt`), `IndexSpec[T]`, `Unique`, `Index`, `DefaultFoldAt`; the refusals (`StoreMisconfigured`), `validateIndexes` (shared) |
| `write.go` | `Put` / `Insert` / `Replace` (the three write modes), `Update`, `Delete`; `prepare` → `encodeAs` (shared, outside every lock) → `commit` / `modify` / `remove` (under the writers' lock) → `persistAndApply`; `encodeCause` |
| `index.go` | the index maps (`entries`, `owned`), `fileableKeys` (shared: the empty key is no key, a repeated one is filed once), `uniqueTaken`, `file` / `unfile`, `rebuild` on open: every document decoded, checked against its own `Key` (`LOAD_FAILED` otherwise, naming the file via `origin`) and filed; a broken unique index or a panicking key function refuses the open |
| `persist.go` | the files: `entryName` (SHA-256 of the key), `persist` (one overlay entry per write), `publish` (PersistFailed vs WriteUnconfirmed), `maybeFold`, `Fold`, `Close`, `fold` / `removeFolded` / `recordFold`, `encodeSnapshot` |
| `load.go` | `load`: the directories, `readSnapshot`, `readOverlay` / `replay`, the crash leftovers removed; it reports whether `open` must fold, and folds nothing itself |
| `hooks.go` | `OnWrite` / `OnDelete`, called with the key after the write is durable (file engine) or committed (SQL engine), outside every lock |
| `sql_config.go` | `SQLConfig[T]` (`Key`, `Transactor`, `IndexKey`, `Table`, `Dialect`), `MaxSQLTableLen`, `MaxSQLKeyLen`, `MaxSQLIndexNameLen`, the table-name rule, `indexTable`, the refusals |
| `sql_dialect.go` | **the only place the SQL engine renders SQL**: every statement per dialect, rendered once at `OpenSQL` (`sqlStatements`), the four rendered per call (a write's index rows and unique-key check, `Reindex`'s shared-key check and unique marking), the DDL |
| `sql_store.go` | `OpenSQL`, `SQLStore[T]`; the reads — `Get`, `List`, `Filter`, `Entries`, `Count`, `Lookup`, `Find`; `OnWrite` / `OnDelete`; `failed` (STATEMENT_FAILED + the withheld cause) |
| `sql_write.go` | `Put` / `Insert` / `Replace`, `Update`, `Delete`; `run` (a savepoint of the caller's transaction, a transaction of the store's own, or one statement), `announce` (hooks through `Deferrer`), the unique check, `mayConflict` and `classify` (a raced collision asked about after the rollback) |
| `sql_reindex.go` | `Reindex`: every index row rebuilt from the documents, a page at a time, `INDEX_BROKEN` for a unique key two documents share |
| `sql_migration.go` | `SQLMigration`: the two tables as one idempotent `core/sql` migration the caller numbers |
| `sql_withheld.go` | `withheld`: the driver's error for `errors.Is`/`errors.As`, its text out of every rendering |
| `codes.go` / `errors.go` | `0.3.80.1`–`0.3.80.17` |
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
  affected-row count exact and is the numbering kitsunium/platform's ADR 0007
  versions will need (ADR 0139 §D10). A unique
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

## Do NOT

- **Persist the file engine's indexes.** Rebuild them. See above.
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
  validated table name.
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
transactions, savepoints, the two constraints, PostgreSQL's aborted state, and
failures and concurrent commits injected at a named statement. The same
contract runs on SQLite, PostgreSQL 17 and MySQL 8.4 through real drivers in
`third-party/db/sql`, under `-tags integration` — see its CLAUDE.md.
