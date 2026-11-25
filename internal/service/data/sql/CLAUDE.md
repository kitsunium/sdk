<!-- updated: 2026-11-02T00:00:00Z -->
# internal/service/data/sql/

The reference half of this file moved, unchanged and under the same section titles, to
`internal/service/data/sql/NOTES.md` (ADR 0253): read the section a change touches.
It holds:
§Contents; §Conventions.

## Purpose

Implements the **ADR 0055** relational-database ports declared in
`internal/core/data/sql`: the transaction manager (root transactions **and** nested
savepoints) with its `Joiner`, `Deferrer` and `Aborter` siblings (ADR 0139), the
connection-pool policy, the bounded health check, and the migration runner
with its version table and its lock — the session's advisory lock, or SQLite's
database-file write lock (ADR 0140). Since ADR 0055's 2026-11-02 amendment
also: a retrying root transactor over a `Transient` classifier, migration
checksums, a per-statement budget, read-only transactions routed to a read
replica, the `Each` and `InsertRows` helpers, and a statement observer
installed under the pool by a `driver.Connector` wrapper.

It ships **no driver** and imports none. `database/sql` is the stdlib's driver
interface; a *driver* is a vendor connector and lives under `third-party/`
(ADR 0012 / ADR 0055 §D2).

Code range: `0.3.54.*` (ADR 0055) — the 20 run outcomes this package emits,
declared since ADR 0160 with the port's `0.2.24.*` in `internal/core/data/sql`,
under the values this package allocated. This package declares no code.

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
pretending: it **says so** (here, in the package comment in `doc.go`, in `pkg/v1/data/sql`'s
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
cd internal/service && GOWORK=off go test -run '^$' -bench=. -benchmem -count=5 ./data/sql/
```

## Linter exclusions this package carries

Six, each narrow-globbed with its reasoning in `.ktn-linter.yaml`. They exist
because the rule and the domain disagree about a decision the ADR already took,
never because a finding was inconvenient:

| Rule | Scope | Why |
|---|---|---|
| `KTN-API-MINIF` | `internal/service/data/sql/**` | ADR 0055 §D1 — the ports speak `*sql.DB` / `*sql.Conn` / `*sql.Rows`, the stdlib's own types. Narrowing them is the first step of the ORM this domain refuses to be. |
| `KTN-INTERFACE-ANYUSE` | `internal/service/data/sql/**` | `arg any` IS `database/sql`'s bind-parameter type. |
| `KTN-VAR-BIGSTRUCT` | `internal/service/data/sql/**`, `pkg/v1/data/sql/**` | `Config` is 72 B and every field is 8-aligned, so 64 is unreachable without deleting one. Value semantics are the contract; the copy happens once per port. |
| `KTN-FUNC-BLANKPARAM` | `dialect_sql.go` | `savepointSQL` discards its dialect by design (ADR 0055 §D4). |
| `KTN-VAR-STRMAP` | `core/data/sql/sql_dialect.go` | the string keys are the *input* being parsed, not an enum. |
| `KTN-INTERFACE-ERNAME` | `Transactor` / `Preparer` (FQN vocabulary) | "Transacter" and "PrepareContexter" are not words. |

## Do NOT

- **Import a driver.** Not for a test, not behind a build tag. The suite's own
  `driver.Driver` is the supported way to exercise this package.
- **Add a `Commit` or `Rollback` to `scopedExecutor`.** The absence is the
  design.
- **Render dialect-specific SQL anywhere but `dialect_sql.go`.** The
  version-table statements `migrate_table.go` builds read the same on all
  three engines and take their bind markers from `core/data/sql`'s
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
- **Replace the advisory lock with a lock table or `pkg/v1/app/lock`** without
  reading ADR 0055 §D7 — a lock that survives its holder's death is the
  failure mode this design exists to avoid. The same holds for SQLite's file
  lock (ADR 0140).
- **Read a busy SQLite lock as anything but SQLite's own words.** A driver
  error that does not match is a failure, which stops the run; widening the
  match could read a real failure as a lock held elsewhere and wait out the
  budget on a broken database.

## Verification

```
bazel test --config=race //internal/service/data/sql:sql_test
# OR
cd internal/service && GOWORK=off go test -race ./data/sql
```
