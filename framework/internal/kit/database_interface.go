package kit

import (
	"context"
	stdsql "database/sql"

	"github.com/kitsunium/sdk/pkg/v1/data/sql"
	"github.com/kitsunium/sdk/pkg/v1/security/secret"
)

// Engine is an SQL engine a database runs on. An engine module returns one —
// github.com/kitsunium/sdk/framework/connectors/postgres, …/mysql, …/sqlite —;
// kit calls it when the database starts. Like an SDK port, it grows by
// sibling interfaces, never by a method.
type Engine interface {
	// Dialect is the SQL the SDK speaks to it.
	Dialect() sql.Dialect
	// Describe says what url points at — the driver, host and port, the
	// database, the TLS mode as written — for the graph and the logs: never
	// a user, never a password.
	Describe(url secret.Value) (DatabaseURLValue, error)
	// Open opens the pool url names. Before each new connection it asks
	// current for the URL as it is now.
	Open(url secret.Value, current func(context.Context) (secret.Value, error)) (*stdsql.DB, error)
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
