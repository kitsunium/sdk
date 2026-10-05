package queue

import (
	coresql "github.com/kitsunium/sdk/internal/core/data/sql"
	svcsql "github.com/kitsunium/sdk/internal/service/data/sql"
)

// sqlMigrationPrefix starts the name of every migration SQLMigration builds, so
// a version table read during an incident says what created the table.
const sqlMigrationPrefix string = "queue "

// SQLMigration returns the migration that creates the one table an SQL queue
// named table keeps on dialect, as a core/data/sql MigrationValue numbered version,
// for the caller's Migrator to run under the caller's version table. Its Down
// drops the table, and every message in it — queued, leased and dead.
//
// The SDK numbers nothing: a migration is a value its consumer constructs
// (ADR 0055 §D12), and only the consumer knows where the queue's table sits in
// its own history. Its one statement does nothing when the table already
// exists — or is already gone — so applying it over a table a previous
// deployment made changes nothing (ADR 0151).
func SQLMigration(dialect coresql.Dialect, table string, version uint64) (coresql.MigrationValue, error) {
	//: a dialect the broker can spell.
	if !dialect.Valid() {
		//: SQLQueueMisconfigured, naming the dialect.
		return coresql.MigrationValue{}, sqlMisconfigured("Dialect", "not one the SDK speaks: "+dialect.String())
	}
	//: the name the statement interpolates.
	if err := validateSQLTable(table); err != nil {
		//: SQLQueueMisconfigured, naming the problem.
		return coresql.MigrationValue{}, err
	}
	migration := coresql.MigrationValue{
		Version: version,
		Name:    sqlMigrationPrefix + table,
		Up:      svcsql.Statements(createQueueTableStatements(dialect, table)...),
		Down:    svcsql.Statements(dropQueueTableStatements(dialect, table)...),
	}
	//: a version the version table can hold.
	if err := migration.Validate(); err != nil {
		//: InvalidMigration, naming the version.
		return coresql.MigrationValue{}, err
	}
	//: ready for the caller's Migrator.
	return migration, nil
}
