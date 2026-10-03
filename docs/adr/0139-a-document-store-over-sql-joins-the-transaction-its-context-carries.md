# ADR 0139 — A document store over SQL joins the transaction its context carries

- **Status**: Accepted
- **Date**: 2026-09-27
- **Deciders**: SDK maintainers
- **Amended by**: [ADR 0160](0160-every-service-has-a-core-and-a-code-keeps-its-value-when-it-moves.md) — §D2: the two engines implement ports after all, declared in `internal/core/data/docstore` — two families, `Collection` without a context and `CollectionContext` with one, and the shared `Announcer` — so neither engine gains or loses a context
- **Related**: [ADR 0055](0055-sdk-sql-domain.md) (the `sql` domain this builds on), [ADR 0110](0110-a-document-store-writes-one-entry-and-rests-as-one-file.md) (the document store whose contract this keeps), [ADR 0039](0039-extending-a-published-port-without-breaking-it.md) (a port grows by siblings), [ADR 0074](0074-what-a-public-alias-may-point-at.md) (an engine's values are the engine's), [ADR 0140](0140-sqlites-migration-lock-is-the-database-files-write-lock.md) (the SQLite runner that migrates its tables); kitsunium/platform ADRs 0004, 0006 and 0007 (kitsunium/platform#18), tracked here as #254

## Context

The framework built on this SDK keeps its products' entities in `docstore`
(ADR 0110): typed documents under a key, unique and multi-valued indexes,
three write modes, hooks, and refusals it maps to its own errors. `docstore`
says what it is not: it keeps every document in memory, has no transaction
across documents, and is one process's store. kit's ADR 0004 moves a
product's stores onto a database the product names — PostgreSQL, MySQL or
SQLite — and asks the SDK for the mechanism: a document store over `sql` that
keeps `docstore`'s contract and joins the transaction its context carries.

The `sql` domain (ADR 0055) had the transactions, not the joining. A
`Transact` inside another is a savepoint, which is right for a caller's own
unit of work, but a callee handed only a context had no way to run a
statement in the transaction that context carries: the chain that records it
(`txscope.go`) is unexported. Nor could anything wait for a commit: a store's
hooks wake workflows and loops, and a hook that fires for a write the caller
then rolls back announces something that never happened.

## Decision

### D1 — the transactor grows two siblings: `Joiner` and `Deferrer`

`Transactor` is frozen at one method (ADR 0055 §D3), so both capabilities are
ADR 0039 siblings in `core/sql`, discovered by type assertion, and the SDK's
own transactor implements both:

- **`Join(ctx) (ex Executor, inTx bool)`** answers where a statement issued
  under `ctx` runs: the executor of the innermost scope of the transaction
  `ctx` carries for THIS transactor, else the pool. A scope that has returned
  still answers its own, retired, executor, which refuses with `TX_CLOSED`: a
  context that outlived its transaction must not fall back to the pool, where
  its statements would run outside the transaction they were written for.
  Another transactor's transaction is another database and is not joined.
- **`Defer(ctx, fn) (held bool)`** holds `fn` until that transaction commits.
  The held functions run once, in order, on the committing goroutine, after
  the commit and before the outermost `Transact` returns. A rollback drops
  them; so does the rollback of the savepoint they were held in, while a
  released savepoint hands them to the scope around it. Each is tagged with
  the id of the scope that held it — the savepoint's counter, 0 for the root —
  and scopes are a stack whose ids only grow, so a savepoint numbered n drops
  exactly what was held at n or above. A panic in a held function reaches the
  caller of the outermost `Transact` after the commit stood, and the functions
  after it do not run. `Defer` answers false, holding nothing, when `ctx`
  carries no open transaction of this transactor: there is no commit to wait
  for, and `fn` is the caller's again.

`Deferrer` is also what a framework needs for its own held effects on a
database — a publish or a mail waiting for the commit — so it is a `sql`
capability, not a store's.

### D2 — the SQL store is a second engine of `docstore`, and there is still no port

`OpenSQL[T](SQLConfig[T], ...IndexSpec[T]) (*SQLStore[T], error)` lives in
`internal/service/docstore`, beside `Open`, the way `queue`, `lock` and
`secret` keep several engines in one package. It takes the same `Unique` and
`Index` declarations, files by the same rule (an empty key is no key, a key
listed twice is filed once), encodes and decodes through the same functions,
and answers the same sentinels under the same codes — `DOCUMENT_NOT_FOUND`,
`DOCUMENT_EXISTS`, `UNIQUE_KEY_TAKEN`, `DOCUMENT_KEY_CHANGED`,
`INDEX_UNKNOWN`, `INDEX_NOT_UNIQUE`, `DOCUMENT_UNDECODABLE`,
`INDEX_BROKEN`, `STORE_MISCONFIGURED` — so a caller mapping `0.3.80.*` maps
both engines unchanged. Two codes are new, in the same range:
`STATEMENT_FAILED` (`0.3.80.16`) and `KEY_TOO_LONG` (`0.3.80.17`).

ADR 0110 §D1 said there is one engine and no port a second implementation
would satisfy. The second engine exists now, and still no port does: every
SQL call takes a context and can fail as a database fails, while the file
engine has no context by decision (ADR 0110 §D6) and never waits on a network.
A shared interface would either give the file store a context it cannot honour
or take the SQL store's away. The values stay the engines' (ADR 0074), and a
framework that wants one port over both declares it on its side, as kit's
unexported `storeEngine` does.

### D3 — two tables per store, binary keys, the document as written

`SQLConfig.Table` names the documents' table; the store derives its index
table by appending `___ix`. A table name is a lower-case identifier —
PostgreSQL folds unquoted names to lower case and MySQL's case sensitivity
follows the server's filesystem — of at most `MaxSQLTableLen` (58) bytes, so
the derived name fits PostgreSQL's 63; it holds no `___`, so no derived name
is ever another store's table; and it does not start with `sqlite_`, which
SQLite reserves. It is interpolated, quoted, into every statement — an
identifier cannot be bound — and that validation is the whole defence.

| table | columns | keys |
|---|---|---|
| `<table>` | `doc_key`, `rev`, `doc` | primary key `doc_key` |
| `<table>___ix` | `index_name`, `index_key`, `doc_key`, `uniq` | primary key `(index_name, index_key, doc_key)`; an index on `doc_key`; `UNIQUE (index_name, index_key, uniq)` |

- **Keys are bytes.** Every key column is binary — `bytea`, `VARBINARY`,
  `BLOB` — and every key is bound as `[]byte`, so two keys are one only when
  their bytes are, and the order is Go's. A text column would compare through
  a collation, and go-sql-driver/mysql's default is case- and
  accent-insensitive: `a` and `A` would be one key.
- **The document is the bytes the store encoded** — `bytea`, `LONGBLOB`,
  `BLOB` — never the engine's JSON type, which sorts members, keeps one of two
  duplicates and respells numbers. It reads back byte for byte.
- **`rev` counts a document's writes.** It starts at 1 and every write moves
  it. That makes MySQL's affected-row count unambiguous — an upsert over an
  existing row always changes it, so 1 is a creation and 2 a replacement,
  whatever `CLIENT_FOUND_ROWS` says — and it is the numbering ADR 0007's
  versions will need.
- **Uniqueness is the table's.** `uniq` is 1 on a unique index's rows and
  NULL on the others; NULLs never collide in a `UNIQUE` constraint on any of
  the three engines, so the one constraint guards every unique index and no
  multi-valued one, and it holds across transactions and processes. The index
  on `doc_key` exists for a document's rows to be found and replaced; on
  PostgreSQL and SQLite, which declare no plain index inside `CREATE TABLE`, it
  is a `UNIQUE (doc_key, index_name, index_key)` that can never be broken.
- **A key is at most `MaxSQLKeyLen` (1024) bytes**, store key and index key
  alike — two of them and an index name fit InnoDB's 3072-byte index entry —
  and an index name at most 64. A longer key is refused with `KEY_TOO_LONG`
  before any statement: MySQL outside strict mode would truncate it into
  another key.

### D4 — its tables are one migration the caller numbers

`SQLMigration(dialect, table, version) (sql.Migration, error)` returns the
migration that creates both tables, named `docstore <table>`, whose `Down`
drops them. The SDK numbers nothing: a migration is a value its consumer
constructs (ADR 0055 §D12), and only the consumer knows where a store's
tables sit in its history.

Every statement is `CREATE TABLE IF NOT EXISTS` or `DROP TABLE IF EXISTS`.
On MySQL, DDL commits implicitly, and ADR 0055 §D8 asks for one DDL statement
per migration so a failure is atomic; this migration has two, and is safe
another way — a run MySQL stopped between them completes when it runs again,
because the statement that already ran does nothing the second time.
`OpenSQL` itself sends no statement.

### D5 — every call runs where its context says

- A read runs on `Join(ctx)`'s executor: in the caller's transaction, which
  sees what it wrote, or on the pool.
- Inside the caller's transaction, EVERY write runs in `Transact`, which nests:
  a savepoint of that transaction, however many statements the write sends. A
  write that fails — refused, collided, or cancelled by a per-call deadline —
  undoes itself alone, and on PostgreSQL the savepoint's rollback also clears
  the aborted state a failed statement leaves, so the caller's transaction
  stays usable. Outside one, a write of several statements — any write to a
  store with indexes, and every `Update` — runs in a transaction of the
  store's own, and a write of one statement runs on the pool, where one
  statement is atomic by itself.
- A write's hook runs through `Defer(ctx, …)`: after the caller's commit, or
  at once when the write ran in a transaction of its own, which has committed.
- `Update` locks the document from its read to its write: `SELECT … FOR
  UPDATE` on PostgreSQL and MySQL; on SQLite, whose lock is the database's and
  is taken by a transaction's first WRITE, the read IS a write — `UPDATE …
  SET rev = rev + 1 … RETURNING doc` — so the lock is held before the
  document is read, whatever `_txlock` the connection was opened with.
- A transaction of the store's own asks for READ COMMITTED on MySQL. InnoDB's
  default, REPEATABLE READ, takes a gap lock wherever a locking statement
  finds no row, and two writers of new documents with neighbouring keys then
  deadlock on each other's gaps. PostgreSQL and SQLite keep their defaults.

What a call costs, pinned by `TestSQLRoundTripsPerCall`:

| call | statements |
|---|---|
| `Get`, `Lookup`, `Find`, `List`, `Count` | 1 |
| `Put` to a store without indexes | 1 |
| `Put` of a new document, with indexes | BEGIN, upsert, unique check, index rows, COMMIT |
| `Put` over a stored document | the same, and a deletion of its previous rows |
| `Update` | BEGIN, locking read, write, unique check, deletion, index rows, COMMIT |
| any write inside the caller's transaction, with or without indexes | its statements between a SAVEPOINT and a RELEASE |

The unique check is skipped for a document with no unique key; a document's
unique keys are checked 400 at a time and its index rows travel 200 to a
statement, under the 999 bound parameters of an SQLite older than 3.32. Timed on the real engines
(`third-party/db/sql/BENCH.md`), a call costs its round trips: on PostgreSQL
across Docker's network a `Get` is one (0.31 ms), an indexed `Put` about six
(1.81 ms), an `Update` about seven and a half; on SQLite, where there is no
network, an indexed `Put` is its one commit's flush (0.15 ms).

### D6 — a refusal is decided before a statement fails, and a raced one is asked about after

The store never parses a driver's error: it imports no driver, and the words
differ by driver and version. So each refusal is decided by the database
answering a question:

- `Insert` over a taken key is `INSERT … ON CONFLICT DO NOTHING` affecting no
  row on PostgreSQL and SQLite — no error, so no aborted PostgreSQL
  transaction; `Replace` and `Delete` of a missing key affect no row.
- A unique key held by another document is found by a read before the index
  rows are written, and refused naming the first such index in declaration
  order.
- Between that read and the write, another transaction can commit the same
  key; on MySQL, `Insert` has no `ON CONFLICT`. Then the engine's constraint
  refuses the statement. The store rolls its write back — to the savepoint, or
  the whole transaction of its own — and asks again what the key or the unique
  key is held by now, through `Join(ctx)`: on MySQL with `LOCK IN SHARE MODE`,
  which reads past a REPEATABLE READ snapshot. A collision it finds is the
  refusal; none is `STATEMENT_FAILED`. Thirty-two writers of one unique key
  end with one winner and thirty-one `UNIQUE_KEY_TAKEN` on every engine.

### D7 — a database's failure is `STATEMENT_FAILED`, and the driver's words are withheld

`STATEMENT_FAILED` carries HTTP 503 and `EX_TEMPFAIL`, names the table and the
step, and says what was being written did not take effect. The driver's error
is joined beside it — `errors.Is` finds a context's deadline, `errors.As` the
driver's own type — but its text is WITHHELD from every rendering: a driver
quotes the row a constraint refused (`Key (doc_key)=(…) already exists`,
`Duplicate entry '…'`), and a store key is routinely an e-mail address, which
no error of `docstore` quotes (ADR 0110 §D6). What is rendered is the error's
Go type, with its SQLSTATE or result code when a method offers one —
`*pgconn.PrepareError SQLSTATE 42P01`, `*sqlite.Error code 1` — which is the
class of failure and names no row.

The transactor's own verdicts (`BEGIN_FAILED`, `COMMIT_FAILED`, …) pass
through unchanged, with ADR 0055's join.

### D8 — index keys may be stored hashed, and index rows are rebuilt on demand

`SQLConfig.IndexKey`, when set, transforms every index key before it reaches
the database — at every write and at every `Lookup` and `Find` — so a table
holds what it returns and never the key: a framework passes a keyed HMAC, an
index is an equality lookup, which a keyed hash keeps, and an e-mail address
never reaches a table. An empty key is filed nowhere, before and after the
transform.

The file store rebuilds its indexes at every `Open`; the SQL store keeps its
rows. So a change to the declarations — an index added, a key function or
`IndexKey` changed, an index made unique — files nothing for the stored
documents until `Reindex(ctx)` runs. It drops every row, a removed index's
included, reads the documents a page of 256 at a time in key order, files
them unconstrained, then refuses with `INDEX_BROKEN`, naming the index, when a
unique index files one key under two documents, before constraining the rows.
A key function that panics is `INDEX_BROKEN` too, as in the file store. It is
one transaction, and must not run beside writers of the same store.

### D9 — what is not guaranteed

- **MySQL, inside the caller's REPEATABLE READ transaction**, a write that
  finds no row still takes a gap lock, and two such transactions can deadlock;
  InnoDB then rolls one back entirely, and its caller gets the driver's
  deadlock beside `STATEMENT_FAILED`. A caller writing documents on MySQL
  should open its transactions READ COMMITTED, as the store does for its own.
- **PostgreSQL, inside a REPEATABLE READ or SERIALIZABLE transaction**, the
  question D6 asks after a raced collision is answered from the snapshot and
  may find nothing: the caller gets `STATEMENT_FAILED` rather than the
  refusal, and serialization failures surface the same way.
- **SQLite** serialises writers. A connection opened without a busy timeout
  answers a held lock with the driver's busy error at once; a transaction the
  caller began DEFERRED and read in before the store's first write can be
  refused busy, because its snapshot is stale.
- `Update`'s function runs inside the write's transaction: a read it makes on
  the pool needs a second connection, and a write to the same document waits
  for the lock the update holds.
- `List` and `Filter` read the whole table, as the file store reads every
  document.
- **Engine versions.** The store needs SQLite 3.35 or later (`RETURNING`,
  and UPSERT since 3.24), and InnoDB's DYNAMIC row format — the default since
  MySQL 5.7.9 and MariaDB 10.2 — for its 3072-byte index entries. An older
  engine refuses the statements, and the caller gets `STATEMENT_FAILED`.

### D10 — what the same write will carry later

ADRs 0006 and 0007 of the framework need the same durable write to carry side
values and versions. Neither is implemented here, and neither needs a change
to what is:

- **Side values.** A subject reference is an equality index the framework
  declares like any other, hashed by `IndexKey`. The due instants its
  retention sweep reads by range need an ORDERED index: a later sibling of
  `IndexSpec` whose key is an instant, kept in a third table `<table>___du`,
  written by the same write scope that files the index rows, created by a
  migration of its own.
- **Versions.** `rev` already numbers every write. A table `<table>___vs`
  keyed `(doc_key, rev)` would receive the previous document in the same
  write scope, and be pruned there; `Put` would then read the row it replaces
  first.

Both tags are three underscores away from the table's name, within
`MaxSQLTableLen`, and both tables are opt-in migrations beside the one D4
returns.

## Consequences / Semantics

- A framework moves a store to a database by handing `OpenSQL` a transactor
  and a table, and running `SQLMigration` under its own version table; its
  calls gain a context, its refusals keep their codes.
- `pkg/v1/sql` publishes `Joiner` and `Deferrer`; `pkg/v1/docstore` publishes
  `OpenSQL`, `SQLStore`, `SQLConfig`, `SQLMigration`, `MaxSQLTableLen`,
  `MaxSQLKeyLen`, `MaxSQLIndexNameLen`, `StatementFailed`, `KeyTooLong` and
  their codes. Both siblings are frozen at one method from this commit.
- The default suite runs the SQL store on all three dialects' statements over
  a fake engine written for it, under the race detector. The same contract
  runs on SQLite, PostgreSQL 17 and MySQL 8.4 through real drivers in
  `third-party/db/sql`, behind the `integration` tag.

## Breaking changes

None. The two siblings and the SQL engine are additions; `docstore`'s file
engine is unchanged, and its three helpers moved to shared functions without
a change of behaviour.

## Why not

- **The engine's JSON type** (`jsonb`, MySQL's `JSON`). It reorders what it
  keeps, so a document would not read back as written (D3).
- **Parsing the driver's error** — SQLSTATE 23505, MySQL 1062 — to recognise a
  collision. It needs the driver's types, which no workspace module imports
  (ADR 0055 §D2), and a text match is a guess. Asking the database after the
  rollback (D6) needs neither.
- **`INSERT IGNORE`** for MySQL's insertion. It also turns a truncation into a
  warning.
- **`VALUES(col)` in MySQL's upsert.** Deprecated since MySQL 8.0.20; the row
  alias that replaces it is not MariaDB's. The document is bound twice.
- **A table per unique index**, or a unique table beside the multi-valued one.
  More tables, more statements per write; the NULL rule gives one constraint
  the same guarantee.
- **No savepoint around a joined write.** A write that failed after its
  document row and before its index rows would leave both in the caller's
  transaction, and a caller catching the refusal would commit half a write.
- **A shared core port over both engines** (D2).
- **A separate `docstore/sql` package.** Its refusals would be another
  package's codes, and every caller mapping `0.3.80.*` would map a second
  range for the same meaning.
- **Rebuilding the index rows at every open**, as the file store does. It is
  O(N) at every start, on a table no longer bounded by memory; `Reindex` makes
  the cost a decision.

## Deferred

- The side values and versions of D10.
- Paged `List` and `Filter`, and queries the engine runs (kit's ADR 0005).
- Typed tables — a column per field — which kit's ADR 0004 defers to a record
  of its own.
- A prepared-statement cache over the `Preparer` sibling (ADR 0055 defers it
  for the same reason).
- Retrying a transaction on a deadlock or a serialization failure.

## Verification

- `internal/core/sql`: `TestTheTransactorsSiblingsAreFrozenAtOneMethodEach`.
- `internal/service/sql/join_external_test.go`: `Join` on the pool, inside a
  transaction, on the innermost scope, on a leaked context and on another
  transactor's; `Defer` after the commit in order, never for a rollback, a
  refused commit or a panic, dropped by a failed savepoint, kept by a released
  one and dropped with the scope around it, false without an open transaction,
  and a panicking held function after the commit.
- `internal/service/docstore/sql_*_external_test.go`, on all three dialects
  over the fake engine (`sqlfake_external_test.go`): the write modes, keys
  empty and too long, reads in byte order, `Update`, the indexes, `IndexKey`,
  hooks after the commit, the caller's transaction joined, rolled back,
  refused writes caught, a failed one-statement write caught on PostgreSQL's
  aborted state, a nested failure, raced collisions answered as refusals,
  450 unique keys checked in two batches, `STATEMENT_FAILED` withholding a driver's text that quotes a key,
  the statements per call, the configuration refusals, `Reindex`, thirty-two
  writers, and the statements' text per dialect.
- `third-party/db/sql`, under `-tags integration`: the same contract on
  SQLite, PostgreSQL 17 and MySQL 8.4, a document read back byte for byte
  (member order, `1.230e-5`, a duplicate member), `a`/`A`, `a`/`a␠`, `é`/`e`,
  and thirty-two writers; `docstore_bench_test.go` and `BENCH.md` time each
  call there.

## References

- `internal/core/sql/sql_interface.go` — `Joiner`, `Deferrer`
- `internal/service/sql/join.go`, `txscope.go`, `txstate.go`
- `internal/service/docstore/sql_*.go`
- `third-party/db/sql/`
- kitsunium/platform `docs/adr/0004-the-store-is-the-port-databases-are-adapters.md`,
  `0006-data-is-classified-field-by-field.md`, `0007-data-remembers-its-versions.md`
- PostgreSQL: `INSERT … ON CONFLICT`, `SELECT … FOR UPDATE`, `bytea`; MySQL 8.4:
  `INSERT … ON DUPLICATE KEY UPDATE` (affected rows), `LOCK IN SHARE MODE`,
  InnoDB gap locks and READ COMMITTED; SQLite: UPSERT and `RETURNING` (3.35),
  write-transaction start on a write statement
