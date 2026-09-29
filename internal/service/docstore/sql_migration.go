// Package docstore — the SQL store's tables, as a migration the caller runs
// under its own version table.
package docstore

import (
	coresql "github.com/kitsunium/sdk/internal/core/sql"
	svcsql "github.com/kitsunium/sdk/internal/service/sql"
)

// sqlMigrationPrefix starts the name of every migration SQLMigration builds,
// so a version table read during an incident says what created the tables.
const sqlMigrationPrefix string = "docstore "

// sqlVersionsMigrationPrefix starts the name of every migration
// SQLVersionsMigration builds.
const sqlVersionsMigrationPrefix string = "docstore versions "

// SQLMigration returns the migration that creates the two tables a SQL store
// named table keeps on dialect — its documents' and its index rows' — as a
// core/sql MigrationValue numbered version, for the caller's Migrator to run
// under the caller's version table. Its Down drops both tables, and every
// document in them.
//
// The SDK numbers nothing: a migration is a value its consumer constructs
// (ADR 0055 §D12), and only the consumer knows where the store's tables sit
// in its own history.
//
// Every statement does nothing when its table already exists — or is already
// gone — so a run MySQL's implicit commit stopped halfway completes when it
// runs again, and applying the migration over tables a previous deployment
// made changes nothing (ADR 0139).
func SQLMigration(dialect coresql.Dialect, table string, version uint64) (coresql.MigrationValue, error) {
	//: a dialect the store can spell.
	if !dialect.Valid() {
		//: StoreMisconfigured, naming the dialect.
		return coresql.MigrationValue{}, misconfigured("Dialect", "not one the SDK speaks: "+dialect.String())
	}
	//: the name every statement interpolates.
	if err := validateTable(table); err != nil {
		//: StoreMisconfigured, naming the problem.
		return coresql.MigrationValue{}, err
	}
	migration := coresql.MigrationValue{
		Version: version,
		Name:    sqlMigrationPrefix + table,
		Up:      svcsql.Statements(createTableStatements(dialect, table)...),
		Down:    svcsql.Statements(dropTableStatements(dialect, table)...),
	}
	//: a version the version table can hold.
	if err := migration.Validate(); err != nil {
		//: InvalidMigration, naming the version.
		return coresql.MigrationValue{}, err
	}
	//: ready for the caller's Migrator.
	return migration, nil
}

// SQLVersionsMigration returns the migration that creates the table a SQL
// store named table keeps its versions in on dialect — <table>___vs, one row
// per version — as a core/sql MigrationValue numbered version, beside the one
// [SQLMigration] returns. A store opened with SQLConfig.Versions needs it; one
// without never touches it. Its Down drops the table, and every version in it:
// run it to turn versions off for good (ADR 0143).
//
// Its one statement does nothing when the table already exists, or is already
// gone, as SQLMigration's do.
func SQLVersionsMigration(dialect coresql.Dialect, table string, version uint64) (coresql.MigrationValue, error) {
	//: a dialect the store can spell.
	if !dialect.Valid() {
		//: StoreMisconfigured, naming the dialect.
		return coresql.MigrationValue{}, misconfigured("Dialect", "not one the SDK speaks: "+dialect.String())
	}
	//: the name every statement interpolates.
	if err := validateTable(table); err != nil {
		//: StoreMisconfigured, naming the problem.
		return coresql.MigrationValue{}, err
	}
	migration := coresql.MigrationValue{
		Version: version,
		Name:    sqlVersionsMigrationPrefix + table,
		Up:      svcsql.Statements(createVersionsTableStatements(dialect, table)...),
		Down:    svcsql.Statements(dropVersionsTableStatements(dialect, table)...),
	}
	//: a version the version table can hold.
	if err := migration.Validate(); err != nil {
		//: InvalidMigration, naming the version.
		return coresql.MigrationValue{}, err
	}
	//: ready for the caller's Migrator.
	return migration, nil
}
