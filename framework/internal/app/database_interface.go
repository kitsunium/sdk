package app

import (
	"context"
	stdsql "database/sql"
	"time"

	"github.com/kitsunium/sdk/pkg/v1/security/secret"
)

// Engine is an SQL engine a database runs on. An engine module returns one —
// github.com/kitsunium/sdk/framework/connectors/postgres, …/mysql, …/sqlite —;
// the framework calls it when the database starts. Like an SDK port, it grows by
// sibling interfaces, never by a method.
type Engine interface {
	// Dialect is the SQL it speaks.
	Dialect() Dialect
	// Describe says what url points at — the driver, host and port, the
	// database, the TLS mode as written — for the graph and the logs: never
	// a user, never a password.
	Describe(url secret.Value) (DatabaseURLValue, error)
	// Open opens the pool url names. Before each new connection it asks
	// current for the URL as it is now.
	Open(url secret.Value, current func(context.Context) (secret.Value, error)) (*stdsql.DB, error)
}

// LockWaiter is an [Engine] whose connections wait a bounded time for a lock
// another connection holds — SQLite's busy timeout —, a sibling of Engine
// (ADR 0178 C2). The framework opens such an engine's pool with OpenWaiting instead
// of Open, and gives it a wait under <name>-timeout, so that a lost race on
// the lock comes back as the engine's refusal before the call's own
// deadline. An engine that does not implement it keeps its own wait.
type LockWaiter interface {
	// OpenWaiting opens the pool url names, as Open does, its connections
	// waiting wait for a lock when url writes no wait of its own.
	OpenWaiting(url secret.Value, current func(context.Context) (secret.Value, error), wait time.Duration) (*stdsql.DB, error)
}

// Classifier is an [Engine] that reads its driver's errors: Transient says
// whether a failed transaction is one the database asks to run again — on
// PostgreSQL SQLSTATE 40001 and 40P01, on MySQL errors 1213 and 1205, on
// SQLite a busy or locked database —, a sibling of Engine. The framework
// runs such a transaction again, the outermost level only, up to
// <name>-tx-retries times, waiting <name>-tx-retry-delay grown by 2 with
// full jitter on the app's clock, each retry a retries attribute on the
// transaction's span and a retry on the telemetry port. A transaction a
// [Service.Go] body opens runs once: the framework cannot know the body
// may run its function twice. An engine that does not implement it
// still has the SDK's own reading of SQLSTATE and SQLite result codes.
type Classifier interface {
	Transient(err error) bool
}

// DatabaseConfigurer configures a database: [Keeps], [Migrations].
type DatabaseConfigurer interface {
	databaseConfigure(o *databaseOptions)
}

// Keeper is what a database keeps: a service — every store it declares —,
// a store, or a module (ADR 0008) — every store of its services.
type Keeper interface {
	keep() kept
}

// MigrationExecutor is what a migration's step runs its statements on: a
// transaction, which a step cannot end.
type MigrationExecutor interface {
	// ExecContext runs a statement that returns no rows.
	ExecContext(ctx context.Context, query string, args ...any) (stdsql.Result, error)
	// QueryContext runs a query that returns rows; the caller closes them.
	QueryContext(ctx context.Context, query string, args ...any) (*stdsql.Rows, error)
	// QueryRowContext runs a query that returns at most one row.
	QueryRowContext(ctx context.Context, query string, args ...any) *stdsql.Row
}
