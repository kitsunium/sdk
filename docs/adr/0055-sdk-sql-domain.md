# ADR 0055 — relational-database domain (`sql`): transaction ownership, savepoints, and a migration lock that dies with its holder

- **Status**: Accepted
- **Date**: 2026-09-10
- **Deciders**: SDK maintainers
- **Related**: [ADR 0012](0012-logger-writer-registry.md) (vendor connectors live in `third-party/`), [ADR 0031](0031-policy-zero-values-are-never-inert.md) (zero values clamp or refuse, never sit inert), [ADR 0039](0039-extending-a-published-port-without-breaking-it.md) (a published port grows a sibling, never a method), [ADR 0050](0050-sdk-lifecycle-domain.md) (cleanup runs on a detached context), [ADR 0016](0016-sdk-process-supervision-domain.md) / [ADR 0026](0026-sdk-resilience-domain.md) / [ADR 0041](0041-sdk-scheduler-domain.md) (no-registry precedents), [ADR 0018](0018-sdk-cross-platform-portability.md) (refuse rather than approximate)

## Context

Every application the SDK is built for talks to a relational database, and the
SDK had nothing to say about it. What downstream teams write instead, over and
over, is not the SQL — it is the part *around* the SQL, and it is the part that
is consistently wrong:

- A repository function receives a `*sql.Tx` and commits it, because it can.
  Its caller had three more statements to run.
- A "nested transaction" helper calls `db.BeginTx` a second time, takes a
  second connection from the pool, and deadlocks against the row its own
  caller has locked. Under a pool of one, it deadlocks against itself.
- `SetMaxOpenConns` is never called, `database/sql` reads the zero as
  *unlimited*, and forty replicas race to exhaust one server's
  `max_connections`.
- A migration runner has no lock, two pods start together, and the same
  `ALTER TABLE` runs twice. Or it *has* a lock, in a table, the pod holding it
  is OOM-killed, and every subsequent deploy hangs behind a row nobody can
  prove is stale.

None of these is a hard problem. All of them are problems whose correct
solution is boring, unglamorous, and never quite finished before the feature
ships. That is precisely the SDK's remit.

It is also the one domain where the SDK's own doctrine — lowest level
possible, total end-to-end control, as few dependencies as it can manage —
runs into its stated exception: **a connector to a third-party system may be a
port/adapter, provided it is totally isolated.** A database is the archetype.
The design below is mostly an exercise in drawing that isolation line and then
not crossing it.

## Decision

### D1 — this is a set of PORTS above `database/sql`, and it will never be an ORM

Four ports and nothing else: `Executor` (the surface a statement runs
against), `Transactor` (who owns a transaction), `Checker` (a bounded liveness
probe), `Migrator` (an ordered, versioned, mutually-exclusive schema runner).

No entity mapping, no query builder, no lazy loading, no identity map, no
change tracking, no repository generation, no schema reflection. This is a
refusal, not a backlog.

The reason is not taste. An ORM's value is inseparable from its opinions about
*your* domain model, and the SDK has none and must have none — it is a
toolbox, and its consumers do not share a data model. A framework that needs
mapping integrates a library that provides it; it does not grow one inside its
own foundation, where the cost of a wrong opinion is paid by every consumer at
once and cannot be swapped out.

The corollary is enforced in the type system: **the ports do not re-declare
the stdlib's types.** `Executor` speaks `*sql.Rows`, `*sql.Row` and
`sql.Result`. Re-declaring them would be the first step down exactly this
road — once `Rows` is ours, scanning is ours; once scanning is ours, mapping
is one refactor away and someone will make it. Organising the standard library
is the whole ambition.

### D2 — `database/sql` is permitted; a driver is not, ever

`database/sql` is the standard library and, more to the point, it *is* Go's
driver interface. Depending on it is not depending on a vendor.

A driver is. `pgx`, `go-sql-driver/mysql`, `mattn/go-sqlite3` and
`modernc.org/sqlite` are connectors to third-party systems, and they land
under `third-party/` by the rule that put the AWS writers there (ADR 0012) —
or they land nowhere, and the consumer imports them directly. Nothing in
`internal/core/sql`, `internal/service/sql` or `pkg/v1/sql` imports a driver,
and no `go.mod` in the workspace grew one for this domain.

The consumer opens its own `*sql.DB` and hands it over. That is one extra line
at wiring time, and in exchange a `pkg/v1/sql` consumer that talks to
PostgreSQL does not compile MySQL's driver into its binary.

**This has a price, and it is paid in the test suite rather than dodged.**
See D10.

### D3 — a unit of work cannot end the transaction it was lent, because the method does not exist

`Transactor.Transact(ctx, opts, fn)` hands `fn` an `Executor`: `ExecContext`,
`QueryContext`, `QueryRowContext`. There is no `Commit`, no `Rollback`, no
`Begin`.

This is the whole point of the port. "A callee committed the transaction under
its caller" is the single most common transaction bug in Go services, and it is
possible only because `*sql.Tx` carries the verbs. Take them away and the bug
is a sentence with no spelling: it does not compile, so it cannot reach review,
so it cannot reach production.

The manager commits when `fn` returns nil and rolls back otherwise, in exactly
one place (`settle`). `TestExecutorHasNoWayToEndATransaction` asserts the
absence by reflection over the port, so a future widening fails the build
rather than the next incident.

`Executor` is therefore **frozen** at three methods (ADR 0039): `pkg/v1/sql`
aliases it, Go interfaces are structural, and a fourth method would break every
downstream two-method double at compile time with no deprecation window.
`Preparer` is the first sibling, discovered by type assertion.

### D4 — the dialect set is CLOSED, and a refusal names *which kind* of refusal

`Dialect` covers PostgreSQL, MySQL/MariaDB and SQLite. `ParseDialect` never
guesses, and it gives two different answers:

- `UnknownDialect` — a name the SDK has never heard of.
- `DialectRefused` — a name the SDK **recognises and declines**, carrying the
  reason in a field.

The second is a stronger statement than the first and the distinction is
load-bearing for whoever is reading the error. SQL Server spells a savepoint
`SAVE TRANSACTION n`, has **no** release statement at all, and dooms a
transaction on many errors so that even `ROLLBACK TRANSACTION n` fails
(`XACT_STATE() = -1`). Oracle has `SAVEPOINT` and `ROLLBACK TO` but no
`RELEASE SAVEPOINT`, so a savepoint lives until the transaction ends. Db2
requires `ON ROLLBACK RETAIN CURSORS`, which is mandatory and changes cursor
semantics.

Nesting on those engines is a **different algorithm, not a different string**.
Shipping a `Dialect` value for them would mean shipping SQL that fails at the
first savepoint of the first nested transaction on a production database. The
three supported engines were chosen because they spell all three savepoint
statements identically — that uniformity is the selection criterion, not a
happy accident.

The zero value is `DialectUnknown` and it is unusable (ADR 0031). It is what an
unset configuration field looks like, and reading it as "probably Postgres" is
how a MySQL deployment discovers the difference in production.

### D5 — a nested transaction is a SAVEPOINT, and every trap that comes with one is handled

**The question**: refuse outright, use savepoints, or silently flatten?

**The answer**: savepoints. Each of the three is defensible and the choice must
not be accidental, so here is the reasoning.

*Silently flattening* — treating an inner `Transact` as a no-op that joins the
outer transaction — is the worst of the three and it is what most hand-rolled
helpers do. The inner function believes its failure was contained; it was not,
and the outer commit ships the work the inner scope thought it had undone.

*Refusing outright* is honest, and it was seriously considered. It fails
because the composition it forbids is legitimate: a service function that
manages its own transaction must remain callable from inside another one, and
the alternative is two spellings of every repository function, one
transactional and one not, chosen by a boolean.

So: **savepoints**, with each of the traps closed rather than avoided.

- **The name is SDK-generated**, `ktn_sp_<n>`. An SQL identifier cannot be a
  bound parameter, so a caller-supplied name would be concatenated into a
  statement — an injection with the shape of a feature.
- **The counter never resets inside one transaction.** Reuse is *legal* in
  every supported dialect and it is a trap: `SAVEPOINT a` twice creates a
  second savepoint that shadows the first, and `RELEASE SAVEPOINT a` then
  releases only the inner one, leaving the outer alive under a name the caller
  believes is gone.
- **Non-zero `TxOptions` on a nested call is REFUSED** (`NestedIsolation`). A
  savepoint changes neither the isolation level nor the read-only-ness of the
  transaction it sits inside. Honouring the request is impossible; *ignoring*
  it would hand the caller a weaker transaction than the one they asked for
  under a nil error, which is how a data race gets written and never found.
- **The context chain is a chain, keyed on the manager's pointer identity.** An
  application with two databases is normal, and a `Transact` on B inside a
  `Transact` on A must open a *real* transaction on B — not a savepoint on A's.
- **A failed sub-transaction does NOT condemn the outer one.** `ROLLBACK TO
  SAVEPOINT` is precisely the statement that clears PostgreSQL's aborted state
  (SQLSTATE 25P02), so a caller who *catches* the inner error has a usable
  transaction to continue in. A caller who propagates it gets the outer
  rollback — the common case and the safe default.
- **Unless the recovery statement itself fails.** Then the transaction is
  **poisoned**: every later operation is refused with `TxPoisoned`, the commit
  never happens, and the whole transaction is rolled back. At that point the
  SDK no longer knows what the engine kept, and committing work it believes it
  undid is the one outcome worse than failing. The first poison wins — a later
  failure caused by the first must not overwrite the diagnosis that explains
  it.

### D6 — a cleanup statement never travels on the context whose cancellation caused it

**The question**: what happens to a transaction whose context is cancelled, and
is the rollback attempted on a detached context?

**The answer**: the root path was already correct for a reason worth writing
down, the *nested* path was not, and it is now — `context.WithoutCancel`,
exactly as ADR 0050 does for `lifecycle`.

**The root path.** `(*sql.Tx).Rollback()` takes no context at all, and
`database/sql` runs an `awaitDone` goroutine that rolls the transaction back as
soon as the context passed to `BeginTx` is done. So a cancelled root
transaction is already rolled back by the standard library, and the runner's
own `Rollback()` returns `ErrTxDone` — which is *not* a failure to report. It
is the normal state after a cancellation, and dressing it up as
`ROLLBACK_FAILED` would put a defect in every log where a client hung up.

**The nested path was a real defect**, found by writing the test before
assuming the answer. Bounding one nested scope with its own deadline is an
ordinary thing to do: the outer transaction is allowed a minute, one optional
step inside it is allowed a hundred milliseconds. When that step's context
ended, the runner issued `ROLLBACK TO SAVEPOINT` **on the expired context**.
`(*sql.Tx).grabConn` checks `ctx.Done()` before it acquires a connection, so
the statement failed without reaching the driver, the runner read that as "the
engine's state is unknown", and it **poisoned a perfectly healthy
transaction**. A per-scope timeout destroyed the transaction around it.

The undo the transaction needs was exactly the undo the cancellation
prevented. So:

- `ROLLBACK TO SAVEPOINT`, `RELEASE SAVEPOINT` and the panic-path undo all run
  under `cleanup(ctx)` = `context.WithoutCancel(ctx)`. Values (a trace span, a
  request id) still travel; only the ending does not.
- The migration lock's courtesy unlock does too — a run abandoned *because* its
  context ended is precisely the run whose unlock would otherwise never be
  sent.
- **`ErrTxDone` is separated from poison.** Poison means the engine's state is
  *unknown*. `ErrTxDone` means `database/sql` has already retired the
  transaction — everything rolled back, nothing can be committed, and there is
  nothing left to be uncertain about. Reading the second as the first would
  turn every ordinary cancellation into a diagnosis about savepoints.

`TestAPerScopeTimeoutDoesNotCondemnTheOuterTransaction`,
`TestAPerScopeTimeoutStillReleasesASucceedingSavepoint`,
`TestACancelledRootTransactionRollsBackAndReportsTheCancellation`,
`TestAnAlreadyFinishedTransactionIsNotPoisoned` and
`TestAnAbandonedMigrationStillSendsItsUnlock` are the five executable halves of
this section. The first two failed before the fix; that is recorded here
because a decision nobody could have got wrong is not worth a section.

**What is NOT guaranteed, stated out loud.** `WithoutCancel` drops the
deadline as well as the cancellation, so a detached cleanup statement has no
budget of its own. If the connection is wedged in a way the driver's own read
deadline does not resolve, that statement can block. The SDK does **not** add a
second budget here, and the asymmetry with ADR 0050 is deliberate: lifecycle
budgets a component's `Stop` because it is *arbitrary user code*, whereas this
is one statement on a connection `database/sql` already governs and whose
lifetime `PoolConfig.MaxLifetime` already bounds. Adding a timer per savepoint
would put a goroutine and an allocation on a hot path to defend against a case
the layer below already handles.

### D7 — the migration lock is the ENGINE's advisory lock, and it is deliberately not `pkg/v1/lock`

**The question the maintainer asked out loud, answered out loud**: the SDK
*has* a lock domain. Why does `migrate_lock.go` not use it?

Because the property that decides a migration lock is **what happens when the
holder dies**, and the two answers are opposites.

A migration runner is killed mid-run more often than any other piece of a
deployment: an OOM kill, a node drain, a CI job a human cancelled. A lock that
*survives* its holder's death — a row in a lock table, or a lease in a separate
service — stays held until something notices the holder is gone. Until then
every subsequent deploy either hangs or is invited to **steal** a lock nobody
can prove is dead, which is the same as having no lock at all with extra steps.

A **session-scoped advisory lock has no such window.** `pg_advisory_lock` and
`GET_LOCK` are released by the *server* when the connection drops, which the
operating system does for a process that no longer exists. There is no lease,
no heartbeat, no expiry to tune, no stealing, and no clock skew.

The second reason is failure-domain containment: the lock lives in the same
system as the thing it protects. If the database is unreachable, no migration
can run anyway — so a lock that lives in the database adds **no new way to be
unavailable**. A separate lock service would.

This is not a contradiction of the `lock` domain, it is that domain's own
analysis applied consistently: a lock whose holder can die without releasing it
needs a fence, and a fence is only worth what the protected resource can
compare. A schema has nothing to compare a fence against. The engine's own
session lifetime is a stronger primitive than anything the SDK could issue, so
the SDK uses it and does not wrap it.

Mechanics that follow from it:

- **A dedicated connection**, not the pool. Both mechanisms are scoped to a
  *session*; releasing from a pooled connection would release nothing on a
  different session and report success.
- **Non-blocking acquisition, retried on the injected clock.** A blocking
  acquisition is bounded only by the caller's context, and a cancelled context
  racing a server-side grant can leave a lock held by a session nobody is
  watching. Try / fail / wait is race-free, uniform across both engines, and
  testable without sleeping.
- **The release closes the connection.** The explicit unlock is a courtesy that
  makes the next runner immediate; the **close is the guarantee**.
- **SQLite is refused at construction, by name** (`MigrationLockUnsupported`).
  It has no advisory-lock function, only the file lock that serialises writers.
  Running unlocked is **not** offered as a fallback: a runner that silently
  drops mutual exclusion is at its most dangerous in exactly the situation it
  exists for. The rest of the domain works on SQLite; only `NewMigrator` does
  not (ADR 0018 — refuse rather than approximate).

**What is NOT guaranteed.** A lock this SDK holds excludes other processes
running *this* runner against *this* version table. It excludes nothing else:
not a DBA at a `psql` prompt, not a second migration tool, not a
`kubectl exec`. And the `lockKey` for PostgreSQL is an FNV-1a fold of the table
name into a `bigint`, so two *different* SDK migration sets whose table names
collide would serialise against each other — a performance surprise, never a
correctness one, and the failure direction is the safe one.

### D8 — a partially applied migration set: what is guaranteed on which engine, and what is not

This is the section that must not be optimistic, because DDL transactionality
is **not portable** and the industry mostly pretends otherwise.

**The SDK's guarantee, on every supported engine**: each migration runs in
**its own transaction**, and the version-table row is written **by that same
transaction, on that same `Executor`**. The set is applied in ascending version
order and stops at the first failure.

**What that buys, per engine**:

| Engine | Transactional DDL | What a failed migration leaves behind |
|---|---|---|
| **PostgreSQL** | **Yes** — `CREATE TABLE`, `ALTER TABLE`, `DROP` are all transactional | Nothing. The schema change **and** its version row are rolled back together. The database is exactly at migration *n−1*, and re-running is safe. This is the full guarantee. |
| **SQLite** | **Yes** for ordinary DDL | Same as PostgreSQL. |
| **MySQL / MariaDB** | **NO** — DDL causes an **implicit commit** | The schema change **has already been committed** by the server before the runner's transaction ever reaches `COMMIT`. The rollback undoes what it can, which may be *nothing*. |

The MySQL row is the one that matters, so it is spelled out rather than
alluded to. On MySQL, a migration whose second statement fails leaves the first
statement's `ALTER TABLE` **applied and committed**, while the version row —
written in the same transaction — is rolled back. The database is now in a
state no version number describes: ahead of *n−1*, not at *n*, and the runner
will try migration *n* again on the next run and fail differently.

**The SDK does not fix this, because it cannot.** No client-side code can make
MySQL's DDL transactional. What it does instead:

1. **Says so, in four places** — here, in `internal/service/sql/CLAUDE.md`, in
   the `pkg/v1/sql` package doc, and in `MigrationFailed`'s own `Private`.
2. **Fails fast and stops.** A failed migration aborts the run; the runner does
   not continue to *n+1* on the theory that it might work.
3. **Leaves the diagnosis complete.** `MigrationFailed` carries the `version`
   and the `direction`, joined with the driver's own error, so the operator
   knows exactly which migration to inspect by hand.

The operational rule that follows — and it is the consumer's, not the SDK's —
is **one DDL statement per migration on MySQL**. That makes the failure atomic
by construction, because there is no second statement to be half-applied. The
SDK cannot enforce it (it does not parse SQL, see D1) and does not pretend to.

Two ordering hazards are refused rather than tolerated, both *before* anything
is applied:

- `MigrationOutOfOrder` — a pending migration whose version is **lower** than
  one already applied. That is two branches merged: the change would slot
  underneath something that already shipped, and the version table could not
  express what happened.
- `MigrationUnknownVersion` — on `Down`, a version recorded in the database
  that this binary carries no migration for. The database being *ahead* of the
  code is a real deployment state (a rollback to a previous image is exactly
  how it happens), and reversing the migrations below it would undo them
  underneath a change still in place. Guessing is not an option: reversing a
  change whose `Down` is not present would mean inventing one.

### D9 — no `Public` ever describes infrastructure

A driver error is the most dangerous cause an SDK can wrap. A failed dial names
the host, the port and often the user; a failed statement names the statement,
and sometimes a bound argument.

Every `Public` in this domain is a fixed string literal naming the **class** of
failure. The driver's own error is attached with `errors.Join` — reachable from
a log, never from `errs.PublicOf`.

`errors.Join` rather than `errs.Wrap` for the reason ADR 0050 gives:
origin-wins (root `CLAUDE.md` rule 6) would make the driver's error the origin
and relabel the SDK's verdict with whatever code the driver happened to carry.
Side by side, `errs.HasCode(err, CodeCommitFailed)` and the caller's own
`errors.Is(err, driver.ErrBadConn)` both answer.

Two tests pin it, at two layers, deliberately duplicated:
`TestNoPublicMessageDescribesInfrastructure` in the core suite and
`TestNoPublicMessageReachesTheConsumerWithInfrastructure` in `pkg/v1` — the
latter because that is the layer a consumer actually renders to a user.

`CommitFailed`'s Public is the one that earns its wording:
*"The transaction could not be committed and had no effect."* Whether the work
took effect is the single fact a caller must never have to infer.

### D10 — testing without a driver: the suite writes one

D2 forbids a driver in the module. That makes "how do you test?" a real
question and not a rhetorical one, and the answer is **not** to skip the tests.

`database/sql` is a **registry**: `sql.Register` takes a `driver.Driver`, and
everything above it — the pool, `*sql.Tx`, the context plumbing, `ErrTxDone`,
`awaitDone` — is the real, unmodified standard library. So a hand-written
scripted driver in the test package exercises the actual production code path,
minus the network and the server.

It buys three things a live database cannot:

- **A statement log**, so a test asserts the exact SQL sent *and its order*.
  That is the entire contract of a savepoint implementation, and it is
  invisible to a test that only checks the final row values.
- **Deterministic failure on a named statement**, so "what happens when
  `ROLLBACK TO SAVEPOINT` itself fails" is a test rather than a paragraph. The
  poison mechanism is reachable no other way.
- **No container**, so the suite runs in the sandbox, in a second, with `-race`
  on.

**What it does NOT verify, stated rather than implied**: that PostgreSQL
*accepts* the SQL. That is a conformance question, it belongs to `e2e/`, and
the fake driver's willingness to accept a statement is not evidence about any
engine. The three-engine uniformity claim in D4 rests on the documentation
cited there, not on the suite.

### D11 — the pool policy is a policy, not a set of hints

`PoolConfig` is applied to the caller's `*sql.DB` at construction — a side
effect on a value the caller owns, and a deliberate one: a pool policy that has
to be installed by a second call is a pool policy someone will forget, and the
consequence of forgetting lands on the *database* rather than on the process
that forgot.

Both sides of ADR 0031 appear in one struct, which is why it is worth naming:

- **`MaxOpen` is REFUSED when non-positive** (`PoolMisconfigured`).
  `database/sql` reads 0 as **unlimited**. "As many connections as the
  application happens to want" is a decision made by forgetting a line, and no
  value the SDK could invent is defensible either — the right one is the
  server's ceiling divided by the replica count, which the SDK cannot see.
- **`MaxLifetime` CLAMPS** to 30 minutes when non-positive. `database/sql`
  reads 0 as "reuse forever", and a connection that lives forever survives a
  failover, a DNS change and a credential rotation — it keeps talking to a
  demoted replica and nothing in the application notices. The exact number
  matters far less than its being *finite*, so a clamp needs no explanation.
- **`MaxIdle` clamps twice** — up to `DefaultMaxIdle` when unset, and **down**
  to `MaxOpen` when above it, because `database/sql` performs that reduction
  silently and a caller reading their own configuration back would otherwise be
  misled about what is in force.
- **`MaxIdleTime` passes through**, including zero, and that is deliberate
  rather than an oversight: `MaxLifetime` is now always finite, so an idle
  connection is already retired on a bounded schedule and a second timer only
  decides how aggressively to shrink a warm pool — a tuning choice with no
  dangerous zero.

A refused `Config` leaves the caller's `*sql.DB` **exactly as it was**;
validation precedes every mutation.

### D12 — no registry, and no migration file format

**No registry.** There is one `Transactor` and one `Migrator`. A registry would
have one entry and would add a way to select a database engine from a
configuration string at runtime — which is exactly the mistake `Dialect` exists
to prevent, since an engine the SDK cannot spell must be refused at
construction, not resolved at runtime. The precedents are `proc` (ADR 0016),
`resilience` (ADR 0026), `net` (ADR 0029), `scheduler` (ADR 0041), `token`
(ADR 0042), `session` (ADR 0045) and `lifecycle` (ADR 0050).

**No migration file format, directory layout or naming convention**, and no
example migration tree. A `Migration` is a value the consumer constructs. The
moment the SDK reads a directory it has invented a filename grammar
(`0001_name.up.sql`? `V1__name.sql`?), a statement splitter, and a rule for
what a `;` inside a string literal means — three conventions imposed on every
consumer and requested by none.

`Statements(...string) Step` is the one helper, and it is the smallest thing
that is genuinely useful: each statement is sent as its own `Exec` rather than
as one semicolon-joined string, because multi-statement execution is a
per-driver capability several disable by default, and because a failure then
names the statement that failed by its position. Where the text comes from — a
literal, an `embed.FS`, a generator — stays the caller's business.

**A nil `Down` is refused; `Irreversible` is the declaration.** "I forgot the
reversal" and "there is no reversal" are the same `nil` and only one of them is
a defect — the rule `core/lifecycle` applies to a nil `Stop`, for the same
reason. `Irreversible` is a value rather than a nil convention so the claim
appears in the diff and in code review.

### D13 — nothing waits on the wall clock

Every budget — the health probe, the migration-lock retry — is read from an
injected `kernel/clock.Timed`. The suite advances a `ManualClock` and never
sleeps, so the lock-timeout assertion is exact rather than a tolerance, and a
tolerance never becomes a flake.

`Config.Clock` is `clock.Timed` rather than `clock.Clock` because this package
both *stamps* (the version table's `applied_at_unix`) and *waits*. `Timed` is
the ADR 0039 union that lets it do both without widening a published port.

The version table stores `applied_at_unix BIGINT` — seconds since the epoch,
**not** a timestamp type. `TIMESTAMP` / `DATETIME` / `TEXT` diverge in
spelling, in precision and in time-zone handling across the three engines; an
integer does not, and it is deterministic under a manual clock.

## Consequences / Semantics

- **Error blocks**: `0.2.24.*` (core, 5 codes) and `0.3.54.*` (service, 17
  codes). Both allocated in `codeRangeOwners` in the same change (ADR 0035).
- Refusals carry `EX_CONFIG` (78) — permanent wiring faults fixed by a code
  change, never a retry. Availability outcomes carry `EX_TEMPFAIL` (75), and
  `BeginFailed` / `CommitFailed` additionally map to HTTP 503: the caller's
  request was never the problem, and a load balancer reads the difference.
- **`pkg/v1/sql` publishes aliases**, so `Executor`, `Transactor`, `Checker`,
  `Migrator`, `TxFunc` and `Step` are frozen shapes under ADR 0039 from this
  commit. No previously published shape changed, so the ADR 0040 v0 licence is
  **not** used — said out loud rather than left silent.
- **Measured**, in `internal/service/sql/BENCH.md`, against the scripted
  driver: transaction ownership costs **715 ns and 4 allocations** over raw
  `database/sql`; the `Executor` guard costs **25 ns and zero allocations per
  statement**; one nesting level costs **~1.9 µs and 15 allocations**, of which
  three driver `Exec` calls are ~0.77 µs — the actionable fact being **two
  extra round trips per nested scope**, not the nanoseconds. Scanning the
  version table costs **~450 ns per recorded row**, which is linear and stated
  so a schema with a thousand migrations is not a surprise.

## Breaking changes

None. This is a new domain; nothing previously published changed shape.

## Why not

- **Why not an ORM, or a query builder?** D1. Its value is inseparable from
  opinions about a domain model the SDK does not have and must not acquire.
- **Why not wrap `database/sql` behind our own `Rows`/`Result`?** D1. That is
  the first step of the ORM, taken by accident.
- **Why not vendor one driver "just for tests"?** D2 / D10. It would put a
  vendor dependency in `internal/service` to buy something a 200-line scripted
  driver already provides *better* — a live database cannot fail
  `ROLLBACK TO SAVEPOINT` on demand.
- **Why not support SQL Server / Oracle / Db2?** D4. Their savepoint semantics
  are different algorithms. A `Dialect` value for them would be SQL that fails
  at the first nested transaction in production.
- **Why not use `pkg/v1/lock` for the migration lock?** D7. A lock that
  survives its holder's death is the failure mode a migration lock exists to
  avoid, and a fence is worthless where the protected resource cannot compare
  it. A schema cannot.
- **Why not a blocking `pg_advisory_lock` instead of try/retry?** D7. A
  blocking acquisition is bounded only by the caller's context, and a cancelled
  context racing a server-side grant leaves a lock held by a session nobody is
  watching.
- **Why not make DDL transactional on MySQL by wrapping it differently?** D8.
  It is a server-side property. No client can change it, and the honest move is
  to document the boundary and refuse to blur it.
- **Why not bound the detached cleanup statement with its own timer?** D6. It
  is one statement on a connection `database/sql` governs and
  `PoolConfig.MaxLifetime` bounds, unlike lifecycle's arbitrary user `Stop`.
  The residual risk is stated instead of being defended with a goroutine per
  savepoint.
- **Why not read migrations from a directory?** D12. It invents three
  conventions nobody asked for.

## Deferred

Each of these is deferred **by name**, so its absence is a decision rather than
an oversight:

- **A `Preparer`-backed statement cache.** `Preparer` ships as an ADR 0039
  sibling, but nothing in the SDK caches prepared statements. Doing it well
  needs a per-connection cache `database/sql` does not expose, and doing it
  badly re-prepares on every pool churn.
- **Read/write splitting and replica routing.** It is a *topology* concern:
  the SDK would have to know which statements are safe on a replica, which
  means parsing SQL (D1) or trusting an annotation nobody maintains.
- **`LISTEN`/`NOTIFY`, and any long-lived connection feature.** They need a
  connection outside the pool with its own lifecycle, and the natural home is
  the `events` domain's wire, not this one.
- **A `resilience` bridge.** Retrying a *transaction* is not retrying a call:
  it needs the unit of work to be idempotent and the retry to re-open, not
  re-run. `resilience.Runner` composes over `Transact` from the outside today;
  a first-class integration needs its own decision.
- **Migration checksums.** Detecting that migration 7's *text* changed after it
  was applied is genuinely useful, and it requires the SDK to have an opinion
  about what a migration's identity is beyond its version — which conflicts
  with `Step` being an opaque func (D12). Revisit if a text-based front end
  ever lands.
- **Oracle / SQL Server / Db2 support.** D4. Would need a second nesting
  algorithm, not a second string table.

## References

- `internal/core/sql/` — the ports, the values, `codes.go` / `errors.go`
- `internal/service/sql/` — the manager, the pool policy, the probe, the runner
- `internal/service/sql/BENCH.md` — the measurements cited above
- `pkg/v1/sql/` — the public facade
- PostgreSQL: `SAVEPOINT`, `ROLLBACK TO SAVEPOINT`, `RELEASE SAVEPOINT`;
  `pg_try_advisory_lock` / `pg_advisory_unlock` (session-scoped)
- MySQL: `GET_LOCK` / `RELEASE_LOCK` (session-scoped); implicit commit on DDL
- Go: `database/sql`, `database/sql/driver`, `(*sql.Tx).grabConn`,
  `sql.ErrTxDone`, `Tx.awaitDone`
