package app

import (
	"context"

	iapp "github.com/kitsunium/sdk/framework/internal/app"
)

// The SQL dialects an [Engine] speaks: kit's own names (ADR 0179 Q5), which
// the store subsystem maps onto the SDK's.
const (
	// DialectPostgres is PostgreSQL.
	DialectPostgres Dialect = iapp.DialectPostgres
	// DialectMySQL is MySQL or MariaDB.
	DialectMySQL Dialect = iapp.DialectMySQL
	// DialectSQLite is SQLite.
	DialectSQLite Dialect = iapp.DialectSQLite
)

// Dialect is the SQL an [Engine] speaks: [DialectPostgres], [DialectMySQL] or
// [DialectSQLite], kit's own names (ADR 0179 Q5) — the graph names the
// engine by it.
type Dialect = iapp.Dialect

// DatabaseURL is what a database's URL points at, as its engine describes it
// for the graph, the Studio, the logs and every error: never a user, never a
// password, never a parameter.
type DatabaseURL = iapp.DatabaseURLValue

// Migration is one versioned change of a database's schema and its
// reversal, kit's own (ADR 0179 step 10): the shape the SDK's sql runs,
// which the store subsystem hands it.
//
//	app.Migration{
//		Version: 20261017120000,
//		Name:    "notes",
//		Up:      app.Statements("CREATE TABLE notes (id TEXT PRIMARY KEY, body TEXT)"),
//		Down:    app.Statements("DROP TABLE notes"),
//	}
type Migration = iapp.MigrationValue

// MigrationStep is one direction of a [Migration], run on ex — the
// transaction its version row is written in, so that both commit
// together.
type MigrationStep = iapp.MigrationStep

// MigrationExecutor is what a migration's step runs its statements on: a
// transaction, which a step cannot end.
type MigrationExecutor = iapp.MigrationExecutor

// Keeps says what the database keeps: a service's stores, a store, a
// module's stores. The most precise wins: a store kept by name, then its
// service, then its module, then the default database — the one declared
// without Keeps —, then the data directory, then memory. [InMemory] wins
// over all of them, on the app as on a store.
func Keeps(things ...Keepable) DatabaseOption {
	//: the implementation is framework/internal/app's; this facade only forwards.
	return iapp.Keeps(things...)
}

// Migrations are migrations, kit's own [Migration]s (ADR 0179 step 10),
// run when the database starts (<name>-migrate: start) or by
// `<product> migrate up`, kit's own first, each set under its own version
// table — and so its own lock. On MySQL a DDL statement commits by itself:
// write one per migration. The store subsystem runs them: a program that
// declares a database imports framework/app/store, as its engine module
// does.
//
// Given to [Database], they are the product's, under schema_migrations.
// Given to [NewModule], they are the module's (ADR 0008), for the data it
// keeps outside kit's stores: run on the database that keeps the module,
// under <module>_migrations.
func Migrations(m ...Migration) interface {
	DatabaseOption
	ModulePart
} {
	//: the implementation is framework/internal/app's; this facade only forwards.
	return iapp.Migrations(m...)
}

// Statements is the [MigrationStep] that runs stmts in order on its
// executor, stopping at the first that fails — whose error it returns as
// the driver said it.
func Statements(stmts ...string) MigrationStep {
	//: the implementation is framework/internal/app's; this facade only forwards.
	return iapp.Statements(stmts...)
}

// Irreversible is the Down of a migration that cannot be reversed, said out
// loud rather than left nil: it refuses, with [CodeMigrationIrreversible],
// and `<product> migrate down` below its version stops there.
func Irreversible(ctx context.Context, ex MigrationExecutor) error {
	//: the implementation is framework/internal/app's; this facade only forwards.
	return iapp.Irreversible(ctx, ex)
}

// Database declares a database the product keeps its stores in, named name,
// on engine — an engine module's, which the product's main imports:
//
//	import (
//		"github.com/kitsunium/sdk/framework/connectors/postgres"
//		"github.com/kitsunium/sdk/framework/connectors/sqlite"
//	)
//
//	var App = app.NewApp("vigie", intake.Service, desk.Service, audit.Service).With(
//		// Keeps every store no other database keeps.
//		app.Database("database", postgres.Engine()),
//		// The audit trail apart.
//		app.Database("archive", sqlite.Engine(), app.Keeps(audit.Service)),
//	)
//
// name is 1 to 49 lower-case letters, digits and dashes, not starting with
// "kit-", unique among the app's databases. It names the database's URL, the
// secret <name>-url — the variable <APP>_<NAME>_URL, or <APP>_<NAME>_URL_FILE
// naming a file that holds it, then the environment's secret store —, and
// its settings: <name>-max-open (10), <name>-max-idle (2), <name>-max-lifetime
// (30m), <name>-max-idle-time (0: none), <name>-timeout (5s), <name>-check-timeout
// (5s) and <name>-migrate (start, or manual). Outside dev the URL is
// required and writes its TLS mode; in dev a database without one leaves its
// stores in the data directory, and a SQLite database without one is the
// file <data>/<name>.sqlite. [InMemory] on the app opens none.
//
//go:noinline
func Database(name string, engine Engine, opts ...DatabaseOption) AppOption {
	//: the implementation is framework/internal/app's; this facade only forwards.
	return iapp.Database(name, engine, opts...)
}
