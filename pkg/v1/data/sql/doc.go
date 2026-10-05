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
// # Joining the caller's transaction, and waiting for its commit
//
// The transactor [NewTransactor] returns has two more capabilities, each an
// interface of its own beside [Transactor], found by type assertion (ADR 0139):
//
//	ex, inTx := tm.(sql.Joiner).Join(ctx)       // where a statement under ctx runs
//	held := tm.(sql.Deferrer).Defer(ctx, notify) // notify runs once ctx's transaction commits
//
// [Joiner] answers the executor of the transaction ctx carries — so a
// repository called inside its caller's transaction reads what that
// transaction wrote — or the pool when it carries none. A context that
// outlived its transaction still names it, and its executor refuses rather
// than fall back to the pool. [Deferrer] holds a function until that
// transaction commits: a rollback drops it, and so does the rollback of the
// savepoint it was held in. It is how a message, a mail or a store's write
// hook waits for the work it announces to be committed. The SDK's document
// store over SQL is built on both.
//
// # Migrations
//
// [NewMigrator] applies an ordered, versioned set under a lock that excludes
// every other runner and dies with its holder, so two instances starting
// together cannot apply the same migration twice and a runner killed mid-run
// leaves nothing held. On PostgreSQL and MySQL it is the session's advisory
// lock, which the server drops with the connection. On SQLite it is the
// database file's write lock (ADR 0140): the run is ONE transaction that takes
// it with its first statement, each migration a savepoint of it, and the
// operating system drops a dead process's file locks. A SQLite run needs a
// single connection, and a migration there cannot run what SQLite refuses
// inside a transaction — VACUUM, or PRAGMA journal_mode.
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
// [Migrator].Plan is the dry run: it reports what [Migrator].Up would apply,
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
