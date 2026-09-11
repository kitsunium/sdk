# internal/core/sql/

## Purpose

Declares the **relational-database ports** above `database/sql`: `Executor`
(the read/write surface a statement runs against), `Transactor` (who owns a
transaction), `Checker` (a bounded liveness probe) and `Migrator` (an ordered,
versioned, mutually-exclusive schema runner) — plus the domain values
`TxOptionsValue`, `MigrationValue`, `Dialect`, the `TxFunc` / `Step` func ports
and the typed sentinels. Admitted by **ADR 0055**. Every concrete
implementation lives in `internal/service/sql`.

Code range: `0.2.24.*` (ADR 0055).

## Contents

| File | Surface |
|---|---|
| `sql.go` | package doc + `TxFunc func(ctx, Executor) error` |
| `sql_interface.go` | the four ports — `Executor`, `Preparer` (ADR 0039 sibling), `Transactor`, `Checker`, `Migrator` |
| `sql_dialect.go` | `Dialect` + `DialectUnknown/Postgres/MySQL/SQLite` + `String` / `Valid` / `SupportsAdvisoryLock` + `ParseDialect` |
| `sql_txoptions.go` | `TxOptionsValue` — `Isolation` / `ReadOnly` + `IsZero` / `StdOptions` |
| `sql_migration.go` | `Step func(ctx, Executor) error`, `MigrationValue` + `Validate`, `Irreversible` |
| `codes.go` | `Code*` constants — range 0.2.24.* |
| `errors.go` | `UnknownDialect` / `DialectRefused` / `NestedIsolation` / `InvalidMigration` / `MigrationIrreversible` (`errs.Define`) |

## Conventions

- **`database/sql` is imported, and no driver ever is.** The stdlib's own
  package is the driver interface of the Go ecosystem, so depending on it is
  not a vendor dependency; a *driver* is. `pgx`, `go-sql-driver/mysql` and the
  sqlite bindings are connectors to a third-party system and belong under
  `third-party/` by the rule that put the AWS writers there (ADR 0012).
  ADR 0055 §D2.
- **The ports do not re-declare the stdlib's types.** `Executor` speaks
  `*sql.Rows`, `*sql.Row` and `sql.Result`. Re-declaring them would be the
  first step of the ORM this domain refuses to become: once `Rows` is ours,
  scanning is ours, and once scanning is ours, mapping is a small step.
- **`Executor` is FROZEN at three methods, and none of them ends a
  transaction.** There is no `Commit`, no `Rollback`, no `Begin`. A function
  that receives an `Executor` therefore *cannot* end the transaction it was
  handed — the mistake does not compile. `Preparer` is the first ADR 0039
  sibling; a fourth capability gets a fifth interface, never a fourth method.
- **`TxFunc` and `Step` are FUNC ports**, the shape `internal/core/CLAUDE.md`
  already admits for `resilience.Operation`, `scheduler.Job` and
  `lifecycle.Start`. ADR 0039 is satisfied structurally: a func type cannot
  grow a method at all.
- **No registry.** There is one `Transactor` and one `Migrator`. A registry
  would have one entry and would add a way to select a database engine from a
  configuration string at runtime — which is exactly the mistake `Dialect`
  exists to prevent.
- **`Dialect` is a CLOSED set with two different refusals.** A name the SDK has
  never heard of returns `UnknownDialect`. A name it recognises and declines
  returns `DialectRefused` *with the reason in a field* — T-SQL has no `RELEASE
  SAVEPOINT`, Oracle has none either, Db2 requires a mandatory cursor-retention
  clause. "I have never heard of this" and "I know this engine and its
  savepoint grammar is a different algorithm" are different facts, and a caller
  debugging a refusal deserves to know which one they hit.
- **The zero `Dialect` is unusable.** `DialectUnknown` is what an unset
  configuration field looks like, and reading it as "probably Postgres" is how
  a MySQL deployment discovers the difference in production (ADR 0031).
- **The zero `TxOptionsValue` IS a working configuration** — the driver's own
  default isolation, read-write. That is ADR 0031's *clamp* side: it is the
  only non-arbitrary default available, because every engine defines its own
  default level and picking one here would silently change the semantics of an
  existing application on migration.
- **A nil `Down` is refused, `Irreversible` is the declaration.** "I forgot the
  reversal" and "there is no reversal" are the same nil and only one of them is
  a defect — the rule `core/lifecycle` applies to a nil `Stop`.
- **A version above `math.MaxInt64` is refused at `Validate`.** It is stored in
  a `BIGINT` and bound as an `int64`; a value with no representation there
  would silently reorder the history.
- Every refusal in this package carries `EX_CONFIG` (78): they are permanent
  wiring faults, fixed by a code change and never by a retry.

## Do NOT

- **Add a method to `Executor`, `Transactor`, `Checker` or `Migrator`.**
  `pkg/v1/sql` aliases all four, so the shape is published (ADR 0039). A new
  capability gets a sibling interface discovered by type assertion, as
  `Preparer` is.
- **Grow an ORM here.** No entity mapping, no query builder, no lazy loading,
  no identity map, no change tracking, no repository generation, no schema
  reflection. ADR 0055 §D1 is a decision, not an omission.
- **Import a driver, or `net`, or anything that opens a connection.** This
  package declares shapes; `internal/service/sql` runs statements.
- **Invent a migration file format, directory layout or naming convention.**
  A `MigrationValue` is a value the consumer constructs. ADR 0055 §D8.
- **Put a `Rows`/`Row`/`Result` of our own here.** See above — that is the
  first step of the ORM.

## Verification

```
bazel test --config=race //internal/core/sql:sql_test
# OR
cd internal/core && GOWORK=off go test -race ./sql
```
