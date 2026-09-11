# internal/service/sql/

## Purpose

Implements the **ADR 0055** relational-database ports declared in
`internal/core/sql`: the transaction manager (root transactions **and** nested
savepoints), the connection-pool policy, the bounded health check, and the
migration runner with its advisory lock and version table.

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
| `tx.go` | `NewTransactor`, `transactor`, root/nested settle paths, `cleanup()`, `txEnded()` |
| `txstate.go` | `txState` — savepoint counter + poison flag, under one `sync.RWMutex` |
| `txscope.go` | `txScope` — the per-context transaction chain and its walk |
| `scope_key_type.go` | the single unexported context key |
| `executor.go` | `scopedExecutor` — the retiring guard handed to a `TxFunc` |
| `statements.go` | `Statements(...string) coresql.Step` |
| `dialect_sql.go` | **the only place dialect-specific SQL is rendered** — savepoints, placeholders, advisory lock |
| `health.go` | `NewChecker`, `checker` — bounded ping on the injected clock |
| `migrate.go` | `NewMigrator`, `migrator`, `Plan` / `Up` / `Down` |
| `migrate_config.go` | `MigrateConfig`, `Default{VersionTable,LockTimeout,LockRetryInterval}` |
| `migrate_plan.go` | `migratePlan` — the validated, sorted, clamped migration set |
| `migrate_lock.go` | `advisoryLock` — acquisition, retry loop, release |
| `migrate_table.go` | version-table DDL, reads, `record` / `forget`, `scanVersions` |
| `codes.go` | `Code*` constants — range 0.3.54.* |
| `savepoint_stmts.go` | `savepointStmts` — one savepoint's three rendered statements |
| `errors.go` | the 17 `errs.Define` sentinels |
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
- **A dialect with no advisory lock is refused at construction, by name.**
  SQLite gets `MigrationLockUnsupported`. Running unlocked is not offered as a
  fallback: a runner that silently drops mutual exclusion is at its most
  dangerous in exactly the situation it exists for.
- **The version table name is validated against `identifierPattern`, never
  bound.** An identifier cannot be a parameter, so it is interpolated — that
  regexp is the whole defence.
- **Errors are JOINED, never wrapped.** `failed()` uses `errors.Join(verdict,
  driverErr)` so origin-wins (CLAUDE.md rule 6) cannot relabel the SDK's
  verdict with the driver's code. `errs.HasCode(err, CodeCommitFailed)` and the
  caller's `errors.Is(err, driverSentinel)` both answer.
- **No `Public` describes infrastructure.** A failed dial names the host, the
  port and often the user; a failed statement names the statement. Every
  `Public` is a fixed literal naming the *class* of failure, pinned by
  `TestNoPublicMessageDescribesInfrastructure`.

## A partially applied migration — what is guaranteed on which engine

Each migration runs in **its own transaction**, and its version-table row is
written by that **same** transaction on that **same** `Executor`.

| Engine | Transactional DDL | A failed migration leaves |
|---|---|---|
| PostgreSQL | **yes** | nothing — schema change and version row roll back together; the database sits at *n−1* and re-running is safe |
| SQLite | **yes** | same |
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
- **Render SQL anywhere but `dialect_sql.go`.** Adding a fourth engine must not
  be possible by forgetting a file.
- **Use `time.Now`, `time.After` or `time.Sleep`.** Take the injected clock.
- **Read a driver error into a `Public`.** Join it; never rephrase it.
- **Interpolate anything a caller supplied into SQL** other than the
  version-table name, which is validated at construction.
- **Make the savepoint counter per-scope, or reset it.** Reuse shadows.
- **Replace the advisory lock with a lock table or `pkg/v1/lock`** without
  reading ADR 0055 §D7 — a lock that survives its holder's death is the
  failure mode this design exists to avoid.

## Verification

```
bazel test --config=race //internal/service/sql:sql_test
# OR
cd internal/service && GOWORK=off go test -race ./sql
```
