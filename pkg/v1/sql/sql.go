//go:generate gomarkdoc --output README.md --repository.url https://github.com/kitsunium/sdk --repository.default-branch main --repository.path /pkg/v1/sql .

// Package sql is the public facade for the SDK's relational-database domain:
// transaction ownership, a pool policy, a health check and a migration runner,
// over the standard library's database/sql.
//
// It is NOT an ORM, and it never will be. There is no entity mapping, no query
// builder, no lazy loading and no repository generation — you write SQL, the
// SDK owns the transaction around it (ADR 0055).
//
// It also ships no driver. Import the one you want, open the pool yourself,
// and hand it over:
//
//	import _ "github.com/jackc/pgx/v5/stdlib"
//
//	pool, err := stdsql.Open("pgx", dsn)
//	dialect, err := sql.ParseDialect("postgres")
//
//	tm, err := sql.NewTransactor(sql.Config{
//		DB:      pool,
//		Dialect: dialect,
//		Pool:    sql.PoolConfig{MaxOpen: 20},
//	})
//
// # Who is allowed to commit
//
// Nobody but the manager. [Transact] hands your unit of work an [Executor] —
// Exec, Query, QueryRow, and deliberately nothing else. A helper that receives
// one CANNOT commit or roll back the transaction it was lent, because those
// methods do not exist on the type it was given:
//
//	err := sql.Transact(ctx, tm, func(ctx context.Context, ex sql.Executor) error {
//		if _, err := ex.ExecContext(ctx, "UPDATE accounts SET cents = cents - $1 WHERE id = $2", n, from); err != nil {
//			return err
//		}
//		return credit(ctx, ex, to, n) // credit cannot commit; it has no way to
//	})
//
// Returning nil commits. Returning an error rolls back and hands the error
// back to you unchanged, so errors.Is keeps working.
//
// # Nesting is a savepoint
//
// database/sql has no nested transactions. Call [Transact] with a context that
// already carries one and you get a SAVEPOINT of that transaction rather than
// a second connection deadlocking against the first:
//
//	_ = sql.Transact(ctx, tm, func(ctx context.Context, ex sql.Executor) error {
//		if err := mustHappen(ctx, ex); err != nil {
//			return err
//		}
//		// best-effort: if this fails, only ITS work is undone
//		if err := sql.Transact(ctx, tm, optional); err != nil {
//			log.Warn("optional step skipped", "err", err)
//		}
//		return nil
//	})
//
// Three consequences worth knowing before you rely on it:
//
//   - Only postgres, mysql and sqlite are supported, because those three spell
//     savepoints identically. SQL Server, Oracle and Db2 are refused BY NAME
//     at [ParseDialect] — their nesting is a different algorithm, not a
//     different string, and generating SQL that fails at the first nested
//     transaction in production is not an option.
//   - A failed nested scope does NOT condemn the outer transaction. ROLLBACK
//     TO SAVEPOINT is precisely the statement that clears PostgreSQL's aborted
//     state, so catching the inner error leaves you a usable transaction. If
//     that statement itself fails, the transaction is poisoned: every later
//     operation is refused and the commit never happens.
//   - A nested call must pass the ZERO [TxOptions]. A savepoint cannot change
//     isolation or read-only mode, and being handed a weaker transaction than
//     you asked for under a nil error is worse than being told no.
//
// # Migrations
//
// [NewMigrator] applies an ordered, versioned set under the database's own
// advisory lock, so two instances starting together cannot apply the same
// migration twice. The lock dies with the connection that holds it, which is
// why it is the engine's and not a row in a table: a runner killed mid-run
// leaves nothing held.
//
// Migrations are VALUES you build. The SDK ships no directory, no file format
// and no naming convention, because the moment it reads a directory it has
// invented one you did not choose:
//
//	m := sql.Migration{
//		Version: 20260910143000,
//		Name:    "create accounts",
//		Up:      sql.Statements("CREATE TABLE accounts (id BIGINT PRIMARY KEY, cents BIGINT NOT NULL)"),
//		Down:    sql.Statements("DROP TABLE accounts"),
//	}
//
// Each migration runs in its own transaction together with its version-table
// row, so on PostgreSQL and SQLite a failed migration rolls back BOTH halves
// and the database sits exactly at the previous version.
//
// On MySQL and MariaDB that guarantee does not hold, and it is not something
// this SDK can fix: DDL causes an IMPLICIT COMMIT, so a migration whose second
// statement fails leaves the first one applied while its version row is rolled
// back. Write ONE DDL statement per migration on MySQL and the failure is
// atomic by construction. See ADR 0055 D8.
//
// [Migrator.Plan] is the dry run: it reports what [Migrator.Up] would apply,
// applies nothing and takes no lock. [Irreversible] is how a migration says
// out loud that it cannot be rolled back — a nil Down is refused, because "I
// forgot the reversal" and "there is no reversal" are the same nil.
//
// # Errors
//
// Every failure is a typed SDK error joined with the driver's own, side by
// side, so neither question loses its answer:
//
//	errors.Is(err, sql.CommitFailed)              // the SDK's verdict
//	errs.HasCode(err, sql.CommitFailed.Code())    // the same, by code
//	errors.Is(err, driver.ErrBadConn)             // the driver's own
//
// The sentinels above are the matchable values; their Code() is the ADR 0005
// dotted quad. No Public message in this domain ever carries a connection
// string, a query or a bound argument.
package sql

import (
	"context"

	coresql "github.com/kitsunium/sdk/internal/core/sql"
	svcsql "github.com/kitsunium/sdk/internal/service/sql"
)

// The three engines this SDK can spell. Each is re-exported individually
// rather than as one group: their values are FIXED by internal/core/sql and
// must equal it exactly, so an iota sequence here would be a second source of
// truth that silently diverges the day a dialect is inserted.

// DialectPostgres is PostgreSQL 9.1+.
const DialectPostgres Dialect = coresql.DialectPostgres

// DialectMySQL is MySQL 5.7+ / MariaDB 10.x with InnoDB.
const DialectMySQL Dialect = coresql.DialectMySQL

// DialectSQLite is SQLite 3.6.8+. It has no advisory lock, so [NewMigrator]
// refuses it — the rest of the domain works.
const DialectSQLite Dialect = coresql.DialectSQLite

// Executor is the public alias for the read/write surface a statement runs
// against. *sql.DB, *sql.Tx and *sql.Conn all satisfy it with no adapter, and
// it deliberately carries no Commit, Rollback or Begin.
type Executor = coresql.Executor

// Preparer is the public alias for the ADR 0039 sibling of [Executor]: an
// executor that can also prepare a statement. Discover it by type assertion.
type Preparer = coresql.Preparer

// TxFunc is the public alias for a unit of work run inside a transaction.
type TxFunc = coresql.TxFunc

// Transactor is the public alias for the transaction manager.
type Transactor = coresql.Transactor

// TxOptions is the public alias for one transaction's isolation and
// read-only-ness. The zero value defers to the driver.
type TxOptions = coresql.TxOptionsValue

// Checker is the public alias for the bounded liveness probe.
type Checker = coresql.Checker

// Migrator is the public alias for the schema-migration runner.
type Migrator = coresql.Migrator

// Migration is the public alias for one versioned schema change and its
// reversal.
type Migration = coresql.MigrationValue

// Step is the public alias for one direction of a migration.
type Step = coresql.Step

// Dialect is the public alias for the closed set of SQL engines this SDK can
// spell.
type Dialect = coresql.Dialect

// Config is the public alias for the parameters every port is built from.
type Config = svcsql.Config

// PoolConfig is the public alias for the connection-pool policy.
type PoolConfig = svcsql.PoolConfig

// MigrateConfig is the public alias for the migration runner's parameters.
type MigrateConfig = svcsql.MigrateConfig

var (
	// UnknownDialect is returned by ParseDialect for a name this SDK has
	// never heard of.
	UnknownDialect = coresql.UnknownDialect
	// DialectRefused is returned by ParseDialect for an engine this SDK
	// recognises and declines. The reason travels in a field.
	DialectRefused = coresql.DialectRefused
	// NestedIsolation is returned when a nested Transact asks for isolation
	// or read-only mode a savepoint cannot provide.
	NestedIsolation = coresql.NestedIsolation
	// InvalidMigration is returned for a migration with version 0, an empty
	// name, a nil Up or a nil Down.
	InvalidMigration = coresql.InvalidMigration
	// MigrationIrreversible is what the Irreversible step returns.
	MigrationIrreversible = coresql.MigrationIrreversible
	// ConfigInvalid is returned by every constructor for a Config that could
	// never produce a working port.
	ConfigInvalid = svcsql.ConfigInvalid
	// PoolMisconfigured refuses a pool policy whose zero value database/sql
	// would read as a decision nobody made — a non-positive MaxOpen.
	PoolMisconfigured = svcsql.PoolMisconfigured
	// BeginFailed reports a transaction the driver would not open.
	BeginFailed = svcsql.BeginFailed
	// CommitFailed reports a transaction that had NO effect.
	CommitFailed = svcsql.CommitFailed
	// RollbackFailed travels alongside the error that triggered the rollback.
	RollbackFailed = svcsql.RollbackFailed
	// SavepointFailed reports a rejected savepoint statement; the "statement"
	// field names which of the three.
	SavepointFailed = svcsql.SavepointFailed
	// TxPoisoned refuses every operation on a transaction whose savepoint
	// rollback failed. It is never committed.
	TxPoisoned = svcsql.TxPoisoned
	// TxClosed reports an Executor used after the scope that lent it
	// returned.
	TxClosed = svcsql.TxClosed
	// HealthCheckFailed reports a database that answered the probe with an
	// error.
	HealthCheckFailed = svcsql.HealthCheckFailed
	// HealthCheckTimeout reports a database that did not answer in time.
	HealthCheckTimeout = svcsql.HealthCheckTimeout
	// MigrationFailed reports a migration that did not apply; it was rolled
	// back.
	MigrationFailed = svcsql.MigrationFailed
	// MigrationOutOfOrder refuses a pending migration older than one already
	// applied.
	MigrationOutOfOrder = svcsql.MigrationOutOfOrder
	// MigrationLockUnsupported refuses a Migrator on a dialect with no
	// advisory lock.
	MigrationLockUnsupported = svcsql.MigrationLockUnsupported
	// MigrationLockTimeout reports another process holding the migration lock
	// for the whole budget. Nothing was applied.
	MigrationLockTimeout = svcsql.MigrationLockTimeout
	// MigrationUnknownVersion refuses a Down over a version this build does
	// not carry.
	MigrationUnknownVersion = svcsql.MigrationUnknownVersion
	// VersionTableInvalid refuses a version-table name that is not a plain
	// SQL identifier.
	VersionTableInvalid = svcsql.VersionTableInvalid
	// DuplicateMigration refuses two migrations declaring the same version.
	DuplicateMigration = svcsql.DuplicateMigration
)

// ParseDialect resolves a dialect name and never guesses. A recognised but
// unsupported engine returns [DialectRefused] carrying why; an unrecognised
// one returns [UnknownDialect].
func ParseDialect(name string) (dialect Dialect, err error) {
	//: delegate to the core value type, which owns the closed set.
	return coresql.ParseDialect(name)
}

// Irreversible is the [Step] a migration assigns to Down to declare, out loud,
// that it cannot be reversed.
func Irreversible(ctx context.Context, ex Executor) error {
	//: delegate so the sentinel is defined in exactly one place.
	return coresql.Irreversible(ctx, ex)
}

// NewTransactor returns the transaction manager for cfg.DB, applying
// cfg.Pool to it.
func NewTransactor(cfg Config) (manager Transactor, err error) {
	//: delegate to the service constructor.
	return svcsql.NewTransactor(cfg)
}

// NewChecker returns a liveness probe bounded by cfg.CheckTimeout.
func NewChecker(cfg Config) (probe Checker, err error) {
	//: delegate to the service constructor.
	return svcsql.NewChecker(cfg)
}

// NewMigrator returns the migration runner. It refuses a dialect with no
// session-scoped advisory lock, by name and at construction.
func NewMigrator(cfg Config, mig MigrateConfig) (runner Migrator, err error) {
	//: delegate to the service constructor.
	return svcsql.NewMigrator(cfg, mig)
}

// Statements returns a [Step] that runs the given statements in order on the
// transaction's executor.
//
// It is the smallest useful helper and deliberately not a file loader: it
// invents no directory layout, no naming convention and no parser. Where the
// text comes from — a literal, an embed.FS, a generator — stays yours.
func Statements(stmts ...string) Step {
	//: delegate to the service helper so the loop lives beside the runner.
	return svcsql.Statements(stmts...)
}

// Transact runs fn inside a transaction with the driver's default isolation.
//
// It is the ergonomic form of [Transactor.Transact] for the common case. The
// port itself keeps the options parameter so it never needs a second method
// (ADR 0039); this helper keeps the call site short.
func Transact(ctx context.Context, tm Transactor, fn TxFunc) error {
	//: the zero options are "whatever the database's own default is".
	return tm.Transact(ctx, TxOptions{}, fn)
}
