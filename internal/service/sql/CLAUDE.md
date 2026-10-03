<!-- updated: 2026-10-03T00:24:48Z -->
# internal/service/sql/

## Purpose

Implements the **ADR 0055** relational-database ports declared in
`internal/core/sql`: the transaction manager (root transactions **and** nested
savepoints) with its `Joiner` and `Deferrer` siblings (ADR 0139), the
connection-pool policy, the bounded health check, and the migration runner
with its version table and its lock — the session's advisory lock, or SQLite's
database-file write lock (ADR 0140).

It ships **no driver** and imports none. `database/sql` is the stdlib's driver
interface; a *driver* is a vendor connector and lives under `third-party/`
(ADR 0012 / ADR 0055 §D2).

Code range: `0.3.54.*` (ADR 0055).

## Contents

| File | Surface |
|---|---|
| `sql.go` | package doc + `failed()` — the verdict/driver-error join |
| `config.go` | `Config`, `PoolConfig`, `Default{MaxLifetime,MaxIdle,CheckTimeout}` |
| `resolved.go` | `resolved` — the validated, clamped form of a `Config` |
| `tx.go` | `NewTransactor`, `transactor`, root/nested settle paths, `cleanup()`, `txEnded()`; the held functions run after the root's commit and are dropped with a failed savepoint |
| `join.go` | `Join` (core/sql.Joiner) and `Defer` (core/sql.Deferrer) — ADR 0139 |
| `txstate.go` | `txState` — savepoint counter + poison flag + the functions held until the commit, each tagged with its scope's id, under one `sync.RWMutex` |
| `txscope.go` | `txScope` — the per-context transaction chain and its walk; each link carries the executor its scope lent and its id (0 for the root, the savepoint counter otherwise) |
| `scope_key_type.go` | the single unexported context key |
| `executor.go` | `scopedExecutor` — the retiring guard handed to a `TxFunc` |
| `statements.go` | `Statements(...string) coresql.Step` |
| `dialect_sql.go` | **the only place this package renders dialect-specific SQL** — savepoints, advisory lock, and `FileLockSQL`, the write that writes nothing and takes SQLite's file lock (ADR 0140), exported because the queue's SQL broker takes the same lock with it before a lease's first read; the bind markers are `core/sql`'s `Dialect.Placeholder` |
| `health.go` | `NewChecker`, `checker` — bounded ping on the injected clock |
| `migrate.go` | `NewMigrator`, `migrator`, `Plan` / `Up` / `Down`, `serialised` — the work under whichever lock the dialect has |
| `migrate_config.go` | `MigrateConfig`, `Default{VersionTable,LockTimeout,LockRetryInterval}` |
| `migrate_plan.go` | `migratePlan` — the validated, sorted, clamped migration set |
| `migrate_lock.go` | `advisoryLock` — acquisition, retry loop, release |
| `migrate_filelock.go` | SQLite's run: ONE transaction holding the file's write lock, `lockBusy`, the retry on the injected clock — ADR 0140 |
| `migrate_table.go` | version-table DDL, reads, `record` / `forget`, `scanVersions` |
| `codes.go` | `Code*` constants — range 0.3.54.* |
| `savepoint_stmts.go` | `savepointStmts` — one savepoint's three rendered statements |
| `errors.go` | the 17 `errs.Define` sentinels |
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
  for why this is not `pkg/v1/lock`.
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
  COMMIT, before `Transact` returns (ADR 0139).
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
  `pkg/v1/sql`'s `TestNoPublicMessageReachesTheConsumerWithInfrastructure`,
  which scans these seventeen beside `core/sql`'s five (`core/sql`'s own
  `TestNoPublicMessageDescribesInfrastructure` scans only those five).

## A partially applied migration — what is guaranteed on which engine

Each migration runs in **its own transaction** — on SQLite, its own savepoint
of the run's one transaction — and its version-table row is written by that
**same** transaction or savepoint on that **same** `Executor`.

| Engine | Transactional DDL | A failed migration leaves |
|---|---|---|
| PostgreSQL | **yes** | nothing — schema change and version row roll back together; the database sits at *n−1* and re-running is safe |
| SQLite | **yes** | same — and the run is ONE transaction under the file's write lock, each migration a savepoint: a failed migration rolls back to its own, and the run commits the ones before it (ADR 0140) |
| MySQL / MariaDB | **NO** — DDL implicitly commits | the schema change **already committed**, the version row rolled back: a state no version number describes |

The MySQL row is the reason this table exists. No client-side code can make
MySQL's DDL transactional, so the runner does three things instead of
pretending: it **says so** (here, in `sql.go`'s package doc, in `pkg/v1/sql`'s
doc, and in `MigrationFailed`'s `Private`), it **stops at the first failure**
rather than continuing to *n+1*, and it **names the version and direction** so
the migration to inspect by hand is identified.

The operational rule that follows is the **consumer's**: one DDL statement per
migration on MySQL, which makes the failure atomic by construction. This
package cannot enforce it — it does not parse SQL (ADR 0055 §D1) — and does not
pretend to. ADR 0055 §D8.

Two ordering hazards are refused *before* anything is applied:
`MigrationOutOfOrder` (a pending version below one already applied — two
branches merged) and `MigrationUnknownVersion` (on `Down`, a recorded version
this binary carries no migration for — the database is ahead of the code, and
reversing what sits below it would undo those migrations underneath a change
still in place).

## Testing without a driver

There is no driver in this module, so the suite **writes one**:
`harness_external_test.go` registers a hand-written `driver.Driver` with
`sql.Register`. Everything above it — the pool, `*sql.Tx`, the context
plumbing, `ErrTxDone`, `awaitDone` — is the real, unmodified standard library,
so a test exercises the production code path minus the network.

That buys three things a live database cannot: a **statement log**, so a test
asserts the exact SQL and its order (which *is* the contract of a savepoint
implementation); **deterministic failures on a named statement**, so "what
happens when `ROLLBACK TO SAVEPOINT` itself fails" is a test rather than a
paragraph; and **no container**, so the suite runs in the sandbox with `-race`.

What it deliberately does not verify is that PostgreSQL *accepts* the SQL. That
is a conformance question, it belongs to `e2e/`, and it is stated here rather
than implied.

## Benchmarks

`sql_bench_test.go` + `BENCH.md` (rule 9). Every benchmark runs against the
scripted driver, so no number includes a network — the useful reading is the
**delta** against the `Std*` baselines, which run the identical driver calls
through `database/sql` alone. Refresh with:

```
cd internal/service && GOWORK=off go test -run '^$' -bench=. -benchmem -count=5 ./sql/
```

## Linter exclusions this package carries

Six, each narrow-globbed with its reasoning in `.ktn-linter.yaml`. They exist
because the rule and the domain disagree about a decision the ADR already took,
never because a finding was inconvenient:

| Rule | Scope | Why |
|---|---|---|
| `KTN-API-MINIF` | `internal/service/sql/**` | ADR 0055 §D1 — the ports speak `*sql.DB` / `*sql.Conn` / `*sql.Rows`, the stdlib's own types. Narrowing them is the first step of the ORM this domain refuses to be. |
| `KTN-INTERFACE-ANYUSE` | `internal/service/sql/**` | `arg any` IS `database/sql`'s bind-parameter type. |
| `KTN-VAR-BIGSTRUCT` | `internal/service/sql/**`, `pkg/v1/sql/**` | `Config` is 72 B and every field is 8-aligned, so 64 is unreachable without deleting one. Value semantics are the contract; the copy happens once per port. |
| `KTN-FUNC-BLANKPARAM` | `dialect_sql.go` | `savepointSQL` discards its dialect by design (ADR 0055 §D4). |
| `KTN-VAR-STRMAP` | `core/sql/sql_dialect.go` | the string keys are the *input* being parsed, not an enum. |
| `KTN-INTERFACE-ERNAME` | `Transactor` / `Preparer` (FQN vocabulary) | "Transacter" and "PrepareContexter" are not words. |

## Do NOT

- **Import a driver.** Not for a test, not behind a build tag. The suite's own
  `driver.Driver` is the supported way to exercise this package.
- **Add a `Commit` or `Rollback` to `scopedExecutor`.** The absence is the
  design.
- **Render dialect-specific SQL anywhere but `dialect_sql.go`.** The
  version-table statements `migrate_table.go` builds read the same on all
  three engines and take their bind markers from `core/sql`'s
  `Dialect.Placeholder`, as every SQL renderer of the SDK does. Adding a
  fourth engine must not be possible by forgetting a file.
- **Spell a bind marker by hand.** It was this package's own `placeholder`
  until it became `Dialect.Placeholder`; docstore and queue carried
  byte-identical copies of it, and a copy is how a fourth engine gets
  forgotten in one of three places.
- **Use `time.Now`, `time.After` or `time.Sleep`.** Take the injected clock.
- **Read a driver error into a `Public`.** Join it; never rephrase it.
- **Interpolate anything a caller supplied into SQL** other than the
  version-table name, which is validated at construction.
- **Make the savepoint counter per-scope, or reset it.** Reuse shadows.
- **Replace the advisory lock with a lock table or `pkg/v1/lock`** without
  reading ADR 0055 §D7 — a lock that survives its holder's death is the
  failure mode this design exists to avoid. The same holds for SQLite's file
  lock (ADR 0140).
- **Read a busy SQLite lock as anything but SQLite's own words.** A driver
  error that does not match is a failure, which stops the run; widening the
  match could read a real failure as a lock held elsewhere and wait out the
  budget on a broken database.

## Verification

```
bazel test --config=race //internal/service/sql:sql_test
# OR
cd internal/service && GOWORK=off go test -race ./sql
```
