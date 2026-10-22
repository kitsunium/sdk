package queue

import (
	coresql "github.com/kitsunium/sdk/internal/core/data/sql"
	svcsql "github.com/kitsunium/sdk/internal/service/data/sql"
)

// sqlMigrationPrefix starts the name of every migration SQLMigration builds, so
// a version table read during an incident says what created the table.
const sqlMigrationPrefix string = "queue "

// sqlTrailMigrationPrefix starts the name of every migration
// SQLTrailMigration builds, beside SQLMigration's in the caller's version
// table.
const sqlTrailMigrationPrefix string = "queue trail "

// SQLMigration returns the migration that creates the one table an SQL queue
// named table keeps on dialect, as a core/data/sql MigrationValue numbered version,
// for the caller's Migrator to run under the caller's version table. Its Down
// drops the table, and every message in it — queued, leased and dead.
//
// The SDK numbers nothing: a migration is a value its consumer constructs
// (ADR 0055 §D12), and only the consumer knows where the queue's table sits in
// its own history. Its one statement does nothing when the table already
// exists — or is already gone — so applying it over a table a previous
// deployment made changes nothing (ADR 0151). The table it creates has the
// trail column SQLConfig.Trail writes; one an earlier release created gets
// it from [SQLTrailMigration].
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

// SQLTrailMigration returns the migration that adds the nullable trail
// column, where a broker with SQLConfig.Trail keeps a dead letter's trail,
// to the table an SQL queue named table keeps on dialect, as a
// core/data/sql MigrationValue numbered version, beside the one
// [SQLMigration] returns. The rows already there keep their columns as
// they are, and their dead letters read back with no trail.
//
// It does nothing when the table has the column — SQLMigration creates it
// with one — or when there is no table, so it may run before or after
// SQLMigration and twice over one table. Its Down drops the column, and
// every trail in it, when the table has one: unset SQLConfig.Trail before
// running it. SQLite drops a column from version 3.35 on.
func SQLTrailMigration(dialect coresql.Dialect, table string, version uint64) (coresql.MigrationValue, error) {
	//: a dialect the broker can spell.
	if !dialect.Valid() {
		//: SQLQueueMisconfigured, naming the dialect.
		return coresql.MigrationValue{}, sqlMisconfigured("Dialect", "not one the SDK speaks: "+dialect.String())
	}
	//: the name the statements interpolate.
	if err := validateSQLTable(table); err != nil {
		//: SQLQueueMisconfigured, naming the problem.
		return coresql.MigrationValue{}, err
	}
	statements := renderTrailColumnStatements(dialect, table)
	migration := coresql.MigrationValue{
		Version: version,
		Name:    sqlTrailMigrationPrefix + table,
		Up:      statements.step(table, false),
		Down:    statements.step(table, true),
	}
	//: a version the version table can hold.
	if err := migration.Validate(); err != nil {
		//: InvalidMigration, naming the version.
		return coresql.MigrationValue{}, err
	}
	//: ready for the caller's Migrator.
	return migration, nil
}
