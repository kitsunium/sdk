// Package sql — hosts the four ports themselves. Kept apart from sql.go so
// the package's contract is one file: what a caller may implement, and what
// the SDK promises to accept.
package sql

import (
	"context"
	stdsql "database/sql"
)

// Executor is the surface a statement runs against: a pool, a connection, or
// a transaction. Implementations MUST be safe for concurrent use, with the
// documented exception of a transaction-scoped executor, which is confined to
// its scope (see internal/service/sql).
//
// The method set is a strict SUBSET of what *sql.DB, *sql.Tx and *sql.Conn
// already expose, so all three satisfy this port with no adapter. That is the
// point: the SDK is not wrapping database/sql, it is naming the part of it a
// caller should be allowed to reach.
//
// It is FROZEN at three methods. pkg/v1/sql aliases it, so under ADR 0039 a
// fourth method would break every downstream double at compile time with no
// deprecation window. New capabilities arrive as SIBLING interfaces the
// implementation is type-asserted for — [Preparer] is the first.
//
// # What is deliberately absent
//
// There is no Commit, no Rollback and no Begin. A function that receives an
// Executor therefore CANNOT end the transaction it was handed: the method does
// not exist, so the mistake does not compile. Ownership of a transaction stays
// with the [Transactor] that opened it (ADR 0055 §D3).
type Executor interface {
	// ExecContext runs a statement that returns no rows.
	ExecContext(ctx context.Context, query string, args ...any) (stdsql.Result, error)
	// QueryContext runs a statement that returns rows. The caller closes them.
	QueryContext(ctx context.Context, query string, args ...any) (*stdsql.Rows, error)
	// QueryRowContext runs a statement expected to return at most one row.
	QueryRowContext(ctx context.Context, query string, args ...any) *stdsql.Row
}

// Preparer is the first ADR 0039 sibling of [Executor]: an executor that can
// also prepare a statement for repeated execution. Discover it by type
// assertion rather than by widening Executor.
//
// *sql.DB, *sql.Tx and *sql.Conn all satisfy it; a transaction-scoped executor
// does too, and the prepared statement it returns is bound to that
// transaction and dies with it.
type Preparer interface {
	// PrepareContext compiles a statement. The caller closes it.
	PrepareContext(ctx context.Context, query string) (*stdsql.Stmt, error)
}

// Transactor owns transactions. Implementations MUST be safe for concurrent
// use.
//
// It is FROZEN at one method, and the options travel in a struct rather than
// as a second method, precisely so the port never needs a sibling for
// "Transact, but read-only" (ADR 0039).
//
// IFACE-PLUGIN: the concrete manager stays unexported behind its constructor
// in internal/service/sql.
type Transactor interface {
	// Transact runs fn inside a transaction and commits when fn returns nil.
	//
	// If ctx already carries a transaction opened by THIS Transactor, fn runs
	// inside a SAVEPOINT of that transaction instead of a new one, and opts
	// must be the zero value — a savepoint cannot change isolation or
	// read-only-ness, so asking for either is refused with [NestedIsolation]
	// rather than silently ignored (ADR 0055 §D5).
	//
	// A panic inside fn is NOT recovered: the deferred rollback still runs,
	// and the panic reaches the caller with its original stack.
	Transact(ctx context.Context, opts TxOptionsValue, fn TxFunc) error
}

// Checker reports whether the database is reachable. Implementations MUST be
// safe for concurrent use.
type Checker interface {
	// Check probes the database under a bounded budget and returns nil when
	// it answered. A non-nil error is always typed and never carries the
	// connection string.
	Check(ctx context.Context) error
}

// Migrator applies an ordered, versioned set of [MigrationValue] to a schema,
// under mutual exclusion. Implementations MUST be safe for concurrent use, and
// MUST be safe against a SECOND PROCESS running the same migrations at the
// same instant.
//
// IFACE-PLUGIN: the concrete runner stays unexported behind its constructor in
// internal/service/sql.
type Migrator interface {
	// Plan reports what Up would apply, in the order it would apply it,
	// WITHOUT applying anything and without taking the migration lock. It is
	// the dry run: a read of the version table and a set difference.
	Plan(ctx context.Context) ([]MigrationValue, error)
	// Up applies every pending migration in ascending version order, each in
	// its own transaction, under the migration lock.
	Up(ctx context.Context) error
	// Down reverses every applied migration whose version is STRICTLY GREATER
	// than target, in descending order, each in its own transaction, under the
	// migration lock. Down(ctx, 0) reverses everything.
	Down(ctx context.Context, target uint64) error
}
