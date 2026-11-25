# internal/service/data/sql — notes

Reference moved out of `CLAUDE.md` (ADR 0253), unchanged and under the same section titles.

## Contents

| File | Surface |
|---|---|
| `doc.go` | the package comment — kit writes it from the design (ADR 0167) |
| `sql.go` | `failed()` — the verdict/driver-error join |
| `config.go` | `Config`, `PoolConfig`, `Default{MaxLifetime,MaxIdle,CheckTimeout}` |
| `resolved.go` | `resolved` — the validated, clamped form of a `Config` |
| `tx.go` | `NewTransactor`, `transactor`, root/nested settle paths, `cleanup()`, `txEnded()`; the held functions run after the root's commit and are dropped with a failed savepoint; the functions held for an abort run after a failed savepoint's or a root's rollback (`runAborted`, newest first) |
| `join.go` | `Join` (core/data/sql.Joiner), `Defer` (core/data/sql.Deferrer) and `OnAbort` (core/data/sql.Aborter) — ADR 0139, the last by its 2026-10-14 amendment |
| `txstate.go` | `txState` — savepoint counter + poison flag + the functions held until the commit and those held until an abort (`abortFn`, with its detached context), each tagged with its scope's id, under one `sync.RWMutex` |
| `txscope.go` | `txScope` — the per-context transaction chain and its walk; each link carries the executor its scope lent and its id (0 for the root, the savepoint counter otherwise) |
| `scope_key_type.go` | the single unexported context key |
| `executor.go` | `scopedExecutor` — the retiring guard handed to a `TxFunc` |
| `statements.go` | `Statements(...string) coresql.Step`, `ChecksumOf(...string) string` — SHA-256 over the length-prefixed statements (#1056) |
| `dialect_sql.go` | **the only place this package renders dialect-specific SQL** — savepoints, advisory lock, and `FileLockSQL`, the write that writes nothing and takes SQLite's file lock (ADR 0140), exported because the queue's SQL broker takes the same lock with it before a lease's first read; the bind markers are `core/data/sql`'s `Dialect.Placeholder` |
| `health.go` | `NewChecker`, `checker` — bounded ping on the injected clock |
| `migrate.go` | `NewMigrator`, `migrator`, `Plan` / `Up` / `Down`, `serialised` — the work under whichever lock the dialect has |
| `migrate_config.go` | `MigrateConfig`, `Default{VersionTable,LockTimeout,LockRetryInterval}` |
| `migrate_plan.go` | `migratePlan` — the validated, sorted, clamped migration set |
| `migrate_lock.go` | `advisoryLock` — acquisition, retry loop, release |
| `migrate_filelock.go` | SQLite's run: ONE transaction holding the file's write lock, `lockBusy`, the retry on the injected clock — ADR 0140 |
| `migrate_table.go` | version-table DDL (three columns, and a nullable `checksum TEXT` for a set that declares a checksum), the checksum probe and `ALTER` under the lock, reads, the `MIGRATION_CHANGED` comparison and the backfill, `record` / `forget`, `scanVersions` |
| `savepoint_stmts.go` | `savepointStmts` — one savepoint's three rendered statements |
| `retry.go` | `NewRetrying`, `RetryConfig`, `Default{RetryAttempts,RetryDelay}`, `Classify` — the retrying root transactor (#1058): each attempt a whole transaction of the inner one, a nested call (context mark, or the inner `Joiner`) never re-run alone, `TX_RETRIES_EXHAUSTED` beside the last error; the three ADR 0139 siblings forwarded exactly when the inner transactor offers all three |
| `budget.go` | `stmtBudget` — `Config.StatementTimeout` on the injected clock: a watcher goroutine per bounded statement, `STATEMENT_TIMED_OUT` with the operation named; `timedPool`, the pool `Join` and `Reader` hand out under a budget; the unbounded path is the one measured and allocates nothing more |
| `budget_compliance.go` | the compile-time assertions that `timedPool` and `scopedExecutor` are the `statementRunner` a budget takes |
| `rows.go` | `Each` (the Next/Scan/Err/Close loop as an `iter.Seq2` that closes its rows) and `InsertRows` / `InsertSpec` (a multi-row `INSERT` into `InsertSpec`'s table and columns, `PerStatement` rows at most, chunked under 999 parameters on SQLite, 65535 on the servers; table and columns interpolated as given), `firstFailure` and `yieldFailure` (one failure said, the first) — #837 |
| `observe.go` | `Observe` — the `driver.Connector` wrapper telling a `StatementObserver` every exec, query, prepare, BEGIN, COMMIT and ROLLBACK, the SQL cut to `MaxObservedQuery` and never an argument; the optional interfaces database/sql reads forwarded or declined as its defaults would; `SlowQueries` and `Traced` — #1122 |
| `withheld.go` | `Withheld` + `NewWithheld` — a driver's error kept for `errors.Is` / `errors.As` and out of every rendering, which `docstore` and `queue` join beside their own verdicts (pinned by `withheld_external_test.go`) |
| `sql_bench_test.go` + `BENCH.md` | the measurements (rule 9) |

## Conventions

- **Only the manager commits.** `settle` is the single `Commit` call site, and
  a `TxFunc` cannot reach it because the `Executor` it was handed has no such
  method. Ownership is structural, not documented.
- **Nesting is a savepoint.** `database/sql` has no nested transactions. A
  `Transact` whose context already carries a transaction from **this** manager
  opens `SAVEPOINT ktn_sp_<n>` instead. The name is SDK-generated (an
  identifier cannot be bound, so a caller-supplied one would be an injection
  with the shape of a feature) and the counter **never resets inside one
  transaction** — a reused name *shadows* rather than replaces, so `RELEASE`
  would release the wrong one.
- **The context chain is a CHAIN, keyed on the manager pointer.** An
  application with two databases is normal: a `Transact` on B inside a
  `Transact` on A must open a real transaction on B, not a savepoint on A's.
  `scopeFor` walks outward looking for its own owner.
- **Non-zero `TxOptionsValue` on a nested call is REFUSED** (`NestedIsolation`).
  A savepoint changes neither isolation nor read-only-ness; silently
  downgrading a caller's `Serializable` is worse than saying no.
- **A cleanup statement never travels on the context whose cancellation caused
  it.** `ROLLBACK TO SAVEPOINT` and `RELEASE SAVEPOINT` run under
  `cleanup(ctx)` = `context.WithoutCancel`, and the migration lock's courtesy
  unlock does too. This is ADR 0050's rule one layer down, and without it a
  per-scope deadline would poison the healthy transaction around it —
  `TestAPerScopeTimeoutDoesNotCondemnTheOuterTransaction` is the executable
  proof. ADR 0055 §D6.
- **Poison means UNKNOWN, not OVER.** A failed `ROLLBACK TO SAVEPOINT` poisons
  the transaction: the SDK no longer knows what the engine kept, so nothing may
  be committed. `sql.ErrTxDone` is the opposite — `database/sql` has already
  retired the transaction, everything rolled back, and that is a *known* state.
  `txEnded()` is the distinction, and confusing the two would turn every
  ordinary cancellation into a diagnosis about savepoints.
- **The first poison wins.** A later failure caused by the first must not
  overwrite the diagnosis that explains it.
- **A panic is never converted.** The deferred rollback (root) or savepoint
  undo (nested) runs, then the panic continues with its original stack. A panic
  is not a database outcome. There is no return value left to report a *cleanup*
  failure on, so it is recorded on the `txState` instead: a unit of work that
  leaked its executor past the panic gets a typed refusal rather than a
  statement sent to a discarded connection.
- **The migration lock's release verdict is JOINED into `Up` / `Down`**, not
  discarded. A stuck unlock costs the *next* runner a full `LockTimeout`, which
  is worth a caller's attention — the same rule `RollbackFailed` states: a
  teardown that also breaks is a second defect, reported beside the first and
  never instead of it.
- **The pool policy is applied to the caller's `*sql.DB` at construction**, and
  a refused `Config` leaves it exactly as it was. `MaxOpen` is REQUIRED because
  `database/sql` reads 0 as *unlimited*; the other fields clamp (ADR 0031's two
  sides, in one struct).
- **Nothing waits on the wall clock.** Every budget — the probe, the lock retry
  — is read from an injected `kernel/clock.Timed`, so the suite advances a
  `ManualClock` instead of sleeping.
- **The migration lock is the ENGINE's session-scoped advisory lock**, held on
  a *dedicated* connection. `pg_advisory_lock` / `GET_LOCK` die with the
  session, which the OS does for a process that no longer exists — so there is
  no lease, no heartbeat, no expiry to tune and no stealing. See ADR 0055 §D7
  for why this is not `pkg/v1/app/lock`.
- **SQLite's lock is the database file's write lock** (ADR 0140). A run is
  ONE transaction of the runner's own transactor: `CREATE TABLE IF NOT EXISTS`
  on the version table, then `DELETE FROM <table> WHERE 1 = 0` — a write that
  writes nothing, and takes the write lock because SQLite starts a write
  transaction when a write statement starts. Each migration's `Transact` then
  NESTS — a savepoint with its version row — and the run COMMITS what applied
  even when a later migration failed. The version table is read through
  `Join`, inside the run's transaction, because the pool's other connections
  are the writers the lock holds off. Busy is SQLite's own words ("database
  is locked", `SQLITE_BUSY`), retried on the injected clock — from the lock
  statements, and from a BEGIN that never began, which is where a
  `_txlock=immediate` connection, or one converting a fresh file to WAL,
  answers the same lock; anything else fails at once, so a misreading stops
  the run and never runs it unlocked. A busy attempt whose ROLLBACK fails is
  not retried either: what its connection holds is unknown.
  Running unlocked is never offered: `MigrationLockUnsupported` stays defined
  for a dialect with neither lock, and none of the three answers it.
- **`Join` never falls back to the pool for a context that names one of this
  manager's transactions**, even a finished one: its retired executor refuses
  with `TX_CLOSED`. **`Defer` tags a held function with the id of the scope
  that held it**; a failed savepoint drops ids at or above its own — scopes
  are a stack and ids only grow — and the root runs what is left after its
  COMMIT, before `Transact` returns (ADR 0139). **`OnAbort` tags its function
  the same way and is the complement** (ADR 0139, amended 2026-10-14): a
  failed savepoint — error or panic — runs what was held at or above its id
  once its ROLLBACK TO SAVEPOINT ran (or failed and poisoned the transaction,
  which then never commits), newest first; the root runs what is left after
  a ROLLBACK — failed unit of work, poison, a failed COMMIT, a panic, before
  the panic continues — and drops it with a COMMIT that stood. Each function
  gets the context `OnAbort` was given, through `context.WithoutCancel`, the
  rule `cleanup()` follows: a clean-up never rides the cancelled context.
- **The version table name is validated by `isIdentifier`, never bound.**
  An identifier cannot be a parameter, so it is interpolated — that check,
  `^[A-Za-z_][A-Za-z0-9_]*$` written out rather than a regular expression
  compiled at the package's initialisation, is the whole defence.
- **Errors are JOINED, never wrapped.** `failed()` uses `errors.Join(verdict,
  driverErr)` so origin-wins (CLAUDE.md rule 6) cannot relabel the SDK's
  verdict with the driver's code. `errs.HasCode(err, CodeCommitFailed)` and the
  caller's `errors.Is(err, driverSentinel)` both answer.
- **`Withheld` is for a package whose rows hold a caller's data.** A driver
  describes the row a statement touched — MySQL's "Duplicate entry '…'", the
  key PostgreSQL's unique violation details — and a document store's key is
  routinely an e-mail address, a queue's row its payload. So `docstore` and
  `queue` join the driver's error through `NewWithheld` beside their own
  verdict (ADR 0139 §D7, ADR 0151 §D6): `errors.Is` and `errors.As` still
  reach it, and `Error()` names only a context's end, or the driver error's Go
  type with its SQLSTATE or result code. The two packages carried
  byte-identical private copies of it until it moved here; their verdicts —
  `STATEMENT_FAILED`, `QUEUE_BACKEND_FAILED` — stay their own, since what
  they share is only how somebody else's error is rendered. This package's
  own `failed()` joins the driver's error as it is, as ADR 0055 decided.
- **No `Public` describes infrastructure.** A failed dial names the host, the
  port and often the user; a failed statement names the statement. Every
  `Public` is a fixed literal naming the *class* of failure, pinned by
  `pkg/v1/data/sql`'s `TestNoPublicMessageReachesTheConsumerWithInfrastructure`,
  which scans these twenty beside `core/data/sql`'s five (`core/data/sql`'s own
  `TestNoPublicMessageDescribesInfrastructure` scans only those five).

- **A retry re-opens; it never re-runs a savepoint.** `NewRetrying` runs
  the WHOLE root transaction again — a new BEGIN, the unit of work from its
  start — and only a root: a context marked by its own root, or one the inner
  transactor's `Joiner` says is inside a transaction, joins as the inner
  transactor decides, once. Each attempt's `Defer` functions are dropped with
  its rollback; its `OnAbort` ones run after it. `Classify` reads only what a
  driver exposes through a method — never a driver import, never a message —
  and a context's end is never transient (ADR 0055, 2026-11-02).
- **A checksum is compared only where both sides have one.** An empty
  `MigrationValue.Checksum` is never compared; a NULL recorded one is
  backfilled under the lock. A set that declares none sends exactly the
  statements it sent before #1056 — the version table's definition, its read,
  the record — and the checksum column is added only under the lock, never
  by `Plan`.
- **The statement budget never touches the unbounded path.** Every executor
  method checks `budget.limit > 0` and takes the slow path only then, so
  `TransactCommit` keeps its allocations (`BENCH.md`). A query's cut context
  is never released early: its rows read through it, so its watcher ends with
  the budget or the caller's context. The migration runner's copy of the
  configuration carries no budget.
- **The replica is chosen by the caller, never by parsing.** A root
  transaction with `ReadOnly` opens on `Config.ReadReplica`; nothing else
  does. `Join` outside a transaction stays on the primary; `Reader` is the
  routed read.
- **An observer sees text, never values.** `Observe` hands the observer the
  SQL as written, cut at a UTF-8 boundary to `MaxObservedQuery`, and never an
  argument or a row; `SlowQueries` and `Traced` render a failure only as
  `Withheld` does.
