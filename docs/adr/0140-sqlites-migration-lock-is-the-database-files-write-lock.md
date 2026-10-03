# ADR 0140 — SQLite's migration lock is the database file's write lock

- **Status**: Accepted
- **Date**: 2026-09-27
- **Deciders**: SDK maintainers
- **Amended by**: [ADR 0159](0159-the-kernel-holds-what-the-domains-rewrote-and-is-published-by-nature.md) §Consequences — `fileLockSQL` is exported as `FileLockSQL`, so the queue's SQL broker takes SQLite's file lock with the same statement
- **Amends**: [ADR 0055](0055-sdk-sql-domain.md) §D7 — its refusal of SQLite (`MIGRATION_LOCK_UNSUPPORTED`)
- **Related**: [ADR 0139](0139-a-document-store-over-sql-joins-the-transaction-its-context-carries.md) (the `Join` this runner reads its history through, and the tables it migrates), [ADR 0050](0050-sdk-lifecycle-domain.md) (cleanup on a detached context); kitsunium/platform ADR 0004 (kitsunium/platform#18), tracked here as #254

## Context

ADR 0055 §D7 chose the engine's session-scoped advisory lock for the migration
runner, for one property: the lock dies with its holder. A runner killed
mid-run leaves nothing held, because the server drops a session's locks with
its connection. SQLite has no advisory lock, so `NewMigrator` refused it at
construction, by name, rather than run unlocked — a runner that silently drops
mutual exclusion is at its most dangerous exactly when two instances start
together.

kit's ADR 0004 puts products on SQLite and asks for its migrations, noting
that SQLite already has a lock with the same property one layer down: the
database file's write lock, which the operating system releases when the
process holding it dies.

## Decision

### D1 — a SQLite run is ONE transaction that holds the write lock throughout

`NewMigrator` accepts SQLite. `Up` and `Down` run their work inside one
transaction of the runner's own transactor, which takes the database's write
lock with its first statements and holds it until its COMMIT. Each migration
runs through the same `Transact` it runs through on the other engines; since
the run's context carries the run's transaction, that `Transact` nests: each
migration is a SAVEPOINT of the run, with its version row written in it.

- A migration that fails rolls back to its savepoint. The run COMMITS the
  migrations before it, reports `MIGRATION_FAILED` naming the version, and
  stops — each migration atomic on its own, as on PostgreSQL.
- A process that dies mid-run loses the whole run rather than its last
  migration, because the run is one transaction. The version table matches the
  schema either way.
- A COMMIT that fails loses every migration the run applied, and says so: the
  phase is `commit`.
- The run needs one connection, where an advisory lock holds one and applies
  on another.
- The version table is created and read inside the run's transaction, through
  the transactor's `Join` (ADR 0139): on SQLite the pool's other connections
  are the writers the lock holds off, this runner's included. Under an
  advisory lock, `Join` answers the pool, as before.

### D2 — the write lock is taken by a write that writes nothing

database/sql opens a transaction with SQLite's deferred `BEGIN`, which takes
no lock. The run's first statements are the version table's
`CREATE TABLE IF NOT EXISTS` and `DELETE FROM <version table> WHERE 1 = 0`:
SQLite starts the write transaction a write statement needs when the
statement STARTS, before its `WHERE` is weighed, so the lock is taken and no
row is touched. Both run inside the run's transaction, so a lock that turns
out to be held undoes the first. `TestTheSQLiteRunHoldsTheFilesWriteLock`
proves it on the real engine: while a run is inside a migration, another
connection's `BEGIN IMMEDIATE` is refused busy, a second runner waits its
budget out and applies nothing, and the lock is gone with the run.

### D3 — busy is SQLite's own words, retried on the injected clock

A held lock is not an answer SQLite gives without an error: it is
`SQLITE_BUSY`, "database is locked". The runner imports no driver (ADR 0055
§D2), so it reads the error's text for SQLite's own words for that code —
`database is locked`, or `SQLITE_BUSY` where a driver names the code — and
retries after the retry interval on the injected clock until `LockTimeout`,
then answers `MIGRATION_LOCK_TIMEOUT` as the advisory runner does. Any other
refusal is `MIGRATION_FAILED` at once.

SQLite can also give that answer one statement earlier, at `BEGIN` itself: a
connection opened with `_txlock=immediate` — as kit's SQLite engine opens
them — takes the write lock there, and a connection that converts a fresh file
to WAL as it opens takes it while it opens, so a second connection opening
beside it is refused busy even with a busy timeout. A run whose transaction
never began, refused with SQLite's busy words, is the same lock held elsewhere
and is retried the same way. The real engine found this case: two runners
started together on a fresh file whose DSN asks for WAL failed about one
attempt in four before the runner read it so
(`TestTwoRunnersApplyAMigrationOnce`, which also runs on `_txlock=immediate`).

A misreading can only go one way. A driver that rewrote SQLite's words would
make a held lock a failure, which stops the run; nothing reads a failure as a
lock taken, so nothing runs unlocked.

Two answers are not waited out. A busy attempt whose ROLLBACK then fails stops
the run with `ROLLBACK_FAILED` beside the busy answer, because what that
connection still holds is unknown. And a BEGIN refused for any reason other
than a busy lock is reported in the `lock` phase — no migration started —
never as a COMMIT that lost the run.

A connection with a busy timeout first waits that long inside the driver
before it answers busy, so an attempt can outlast the runner's budget by the
connection's timeout. The budget is the runner's; the wait is the
connection's.

### D4 — what a SQLite migration cannot do

A migration runs inside the run's transaction, so it cannot run what SQLite
refuses there: `VACUUM`, `PRAGMA journal_mode`, and `PRAGMA foreign_keys`,
which does nothing inside a transaction. The table rebuild SQLite documents
for an `ALTER` it does not support, which switches foreign keys off, is run
outside the runner.

## Consequences / Semantics

- Every dialect the SDK speaks has a `Migrator`. `MIGRATION_LOCK_UNSUPPORTED`
  is no longer answered for any of them; it stays defined, because it is
  published and a caller matching it would stop compiling, for a dialect that
  would have neither lock.
- `Dialect.SupportsAdvisoryLock` keeps its meaning: it now decides HOW a run
  is serialised, not whether it can be.
- `Plan` still takes no lock. On SQLite it creates the version table on the
  pool when it is absent, a write that waits for — or is refused by — a run
  holding the lock, as ADR 0055 documents PostgreSQL's spurious race.

## Breaking changes

None that compiles differently. `NewMigrator` over SQLite used to answer
`MIGRATION_LOCK_UNSUPPORTED` and now answers a runner; a caller who relied on
the refusal to keep migrations off SQLite gets them run.

## Why not

- **`BEGIN IMMEDIATE` on a raw connection.** It takes the lock at once, but
  outside database/sql's transaction, so the runner's own savepoints and
  `Transact` could not be used, and a connection whose run panicked would go
  back to the pool mid-transaction.
- **A transaction per migration, re-taking the lock each time.** Between two
  migrations another runner could take it and interleave its own, so the run
  would not be one run.
- **A lock file beside the database, through `pkg/v1/lock`.** It excludes only
  other runners that know of it; the database file's own lock is the one every
  writer of the database takes, and it is already the one SQLite relies on.
- **A lock row in a table.** It survives its holder's death, which is the
  failure mode ADR 0055 §D7 exists to avoid.

## Deferred

- A database opened as `:memory:` is one database per connection, so the
  runner migrates the connection it holds and no other. That is a property of
  the database, not of the lock, and is not detected.

## Verification

- `internal/service/sql/migrate_filelock_external_test.go`, over the scripted
  driver: the run's exact statements (one BEGIN, the lock, the plan, a
  savepoint and version row per migration, one COMMIT) on a pool of ONE
  connection; a busy answer rolled back and retried on the manual clock; a
  lock held for the whole budget; a refusal that is not busy stopping at once;
  a busy BEGIN read as the same lock; a busy attempt whose rollback fails,
  not retried; a BEGIN refused otherwise, in the lock phase; a failing
  migration committing the ones before it; a failing COMMIT naming its phase;
  `Down` under the same lock; `Plan` taking none.
- `third-party/db/sql/migrate_integration_test.go`, under `-tags integration`:
  the lock proved on SQLite (D2), a failing migration on the real engine, and
  two runners applying a migration once on SQLite — deferred and
  `_txlock=immediate` — PostgreSQL 17 and MySQL 8.4.

## References

- `internal/service/sql/migrate_filelock.go`, `migrate.go` (`serialised`),
  `migrate_table.go`, `dialect_sql.go` (`fileLockSQL`)
- SQLite: "BEGIN TRANSACTION" (deferred, immediate, exclusive), file locking
  and WAL, `SQLITE_BUSY` and the busy handler, the generalised `ALTER TABLE`
  procedure
