package docstore

import (
	"context"

	coresql "github.com/kitsunium/sdk/internal/core/data/sql"
	svcsql "github.com/kitsunium/sdk/internal/service/data/sql"
)

// sqlMigrationPrefix starts the name of every migration SQLMigration builds,
// so a version table read during an incident says what created the tables.
const sqlMigrationPrefix string = "docstore "

// sqlVersionsMigrationPrefix starts the name of every migration
// SQLVersionsMigration builds.
const sqlVersionsMigrationPrefix string = "docstore versions "

// sqlTrashIndexMigrationPrefix starts the name of every migration
// SQLVersionsTrashIndexMigration builds.
const sqlTrashIndexMigrationPrefix string = "docstore trash index "

// SQLMigration returns the migration that creates the two tables a SQL store
// named table keeps on dialect — its documents' and its index rows' — as a
// core/data/sql MigrationValue numbered version, for the caller's Migrator to run
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
//
// Its Checksum fingerprints the statements Up runs (ADR 0055, amended
// 2026-11-02): a version table that recorded it refuses a build whose
// statements differ, MIGRATION_CHANGED, and one that recorded the version
// without a checksum is given this one by the next locked run. The
// statements are therefore never edited: a later column comes as a
// migration of its own.
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
	up := createTableStatements(dialect, table)
	migration := coresql.MigrationValue{
		Version: version,
		Name:    sqlMigrationPrefix + table,
		Up:      svcsql.Statements(up...),
		Down:    svcsql.Statements(dropTableStatements(dialect, table)...),
		//: what Up runs, fingerprinted: an install that recorded it refuses a
		//: build whose statements differ (#1056).
		Checksum: svcsql.ChecksumOf(up...),
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
// per version — as a core/data/sql MigrationValue numbered version, beside the one
// [SQLMigration] returns. A store opened with SQLConfig.Versions needs it; one
// without never touches it. Its Down drops the table, and every version in it:
// run it to turn versions off for good (ADR 0143).
//
// Its one statement does nothing when the table already exists, or is already
// gone, as SQLMigration's do. Its Checksum fingerprints that statement, as
// SQLMigration's does.
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
	up := createVersionsTableStatements(dialect, table)
	migration := coresql.MigrationValue{
		Version: version,
		Name:    sqlVersionsMigrationPrefix + table,
		Up:      svcsql.Statements(up...),
		Down:    svcsql.Statements(dropVersionsTableStatements(dialect, table)...),
		//: what Up runs, fingerprinted, as SQLMigration's (#1056).
		Checksum: svcsql.ChecksumOf(up...),
	}
	//: a version the version table can hold.
	if err := migration.Validate(); err != nil {
		//: InvalidMigration, naming the version.
		return coresql.MigrationValue{}, err
	}
	//: ready for the caller's Migrator.
	return migration, nil
}

// SQLVersionsTrashIndexMigration returns the migration that indexes the trash
// of a SQL store named table on dialect — the rows numbered 0 of
// <table>___vs, which Tombstones reads — as a core/data/sql MigrationValue
// numbered version, after the one [SQLVersionsMigration] returns. Without
// it, Tombstones scans every version of every document to find a handful
// of trashed ones; with it, the database reads the trash alone, in key
// order (ADR 0143, amended 2026-11-02).
//
// The index is <table>___tr: on PostgreSQL and SQLite a partial index over
// doc_key WHERE num = 0, on MySQL a key over (num, doc_key), which has no
// partial index. A versions table an earlier release created gets it from
// this migration, and so does a new one: run it once, after
// SQLVersionsMigration, wherever versions are kept. Up does nothing when
// the index exists — MySQL, which has no IF NOT EXISTS for an index, asks
// information_schema first — and Down drops it, doing nothing when it is
// gone; no version is touched either way.
func SQLVersionsTrashIndexMigration(dialect coresql.Dialect, table string, version uint64) (coresql.MigrationValue, error) {
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
	create, drop := trashIndexStatements(dialect, table)
	migration := coresql.MigrationValue{
		Version: version,
		Name:    sqlTrashIndexMigrationPrefix + table,
		Up:      svcsql.Statements(create),
		Down:    svcsql.Statements(drop),
	}
	//: MySQL: each direction asks whether the index is there first.
	if dialect == coresql.DialectMySQL {
		migration.Up = whenTrashIndex(table, false, create)
		migration.Down = whenTrashIndex(table, true, drop)
	}
	//: a version the version table can hold.
	if err := migration.Validate(); err != nil {
		//: InvalidMigration, naming the version.
		return coresql.MigrationValue{}, err
	}
	//: ready for the caller's Migrator.
	return migration, nil
}

// whenTrashIndex is a MySQL migration step that sends stmt only when the
// trash index of table's store exists — present — or does not, so a run
// MySQL's implicit commit stopped before the version was recorded completes
// when it runs again. The driver's own error travels verbatim, as
// Statements' does.
func whenTrashIndex(table string, present bool, stmt string) coresql.Step {
	//: captured once; the step is reusable.
	return func(ctx context.Context, ex coresql.Executor) error {
		var n int64
		//: the dictionary's answer, both names bound.
		if err := ex.QueryRowContext(ctx, trashIndexExistsStatement, versionsTable(table), trashIndex(table)).Scan(&n); err != nil {
			//: the runner adds the version and the direction around it.
			return err
		}
		//: already as the step would leave it.
		if (n > 0) != present {
			//: nothing to do.
			return nil
		}
		_, err := ex.ExecContext(ctx, stmt)
		//: the index added or dropped, or the driver's refusal.
		return err
	}
}
