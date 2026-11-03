package queue

import (
	"context"
	"crypto/sha256"
	"encoding/hex"

	coresql "github.com/kitsunium/sdk/internal/core/data/sql"
)

// sqlPriorityMigrationPrefix starts the name of every migration
// SQLPriorityMigration builds, the table's name after it.
const sqlPriorityMigrationPrefix string = "queue priority "

// sqlPriorityIndexSuffix ends the name of the index SQLPriorityMigration
// creates over (dead, priority, due, id).
const sqlPriorityIndexSuffix string = "_priority"

// sqlPriorityIndexHash is how many bytes of the table name's SHA-256 a
// shortened index name keeps, so two long table names sharing a prefix still
// name two indexes.
const sqlPriorityIndexHash int = 4

// SQLPriorityMigration returns the migration that adds the priority
// column — a small integer, NOT NULL, 0 by default, where a broker with
// SQLConfig.Priority keeps a message's level — and the index over
// (dead, priority, due, id) its lease reads, to the table an SQL queue
// named table keeps on dialect, as a core/data/sql MigrationValue
// numbered version, beside the one [SQLMigration] returns. The rows
// already there read as core/data/queue.PriorityNormal.
//
// It is a migration of its own because SQLMigration's statements are
// never edited (ADR 0055, amended 2026-11-02). It does nothing when the
// table has the column or when there is no table, so it may run twice
// over one table. Its Down drops the index and the column, and every
// level in it: unset SQLConfig.Priority before running it. SQLite drops
// a column from version 3.35 on.
func SQLPriorityMigration(dialect coresql.Dialect, table string, version uint64) (coresql.MigrationValue, error) {
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
	statements := renderPriorityColumnStatements(dialect, table)
	migration := coresql.MigrationValue{
		Version: version,
		Name:    sqlPriorityMigrationPrefix + table,
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

// priorityColumnStatements are what SQLPriorityMigration sends to add the
// priority column and its index to a queue's table, or to drop them, on one
// dialect. Every engine reads the table's columns from its catalogue first —
// the table's name bound, never interpolated —, so a table that is not there
// or already has the column is left alone: an index cannot be created IF NOT
// EXISTS over a table that is not there.
type priorityColumnStatements struct {
	// probe counts the table's columns and, among them, the priority column:
	// (0, 0) for no table.
	probe string
	// add adds the column and the index, in order.
	add []string
	// drop drops the index and the column, in order.
	drop []string
}

// renderPriorityColumnStatements renders SQLPriorityMigration's statements
// over table on dialect. The table name was validated by the caller.
func renderPriorityColumnStatements(dialect coresql.Dialect, table string) priorityColumnStatements {
	q := dialect.QuoteIdent(table)
	index := dialect.QuoteIdent(priorityIndexName(table))
	columns := "(dead, " + sqlPriorityColumn + ", due, id)"
	//: the catalogue each engine keeps its columns in, and its DDL.
	switch dialect {
	//: PostgreSQL: information_schema, in the session's schema.
	case coresql.DialectPostgres:
		//: one column, one index.
		return priorityColumnStatements{
			probe: priorityProbe("column_name",
				"information_schema.columns WHERE table_schema = current_schema() AND table_name = "+dialect.Placeholder(1)),
			add: []string{
				"ALTER TABLE " + q + " ADD COLUMN " + sqlPriorityColumn + " smallint NOT NULL DEFAULT 0",
				"CREATE INDEX IF NOT EXISTS " + index + " ON " + q + " " + columns,
			},
			drop: []string{"DROP INDEX IF EXISTS " + index, "ALTER TABLE " + q + " DROP COLUMN " + sqlPriorityColumn},
		}
	//: MySQL and MariaDB: information_schema, in the connection's database;
	//: one ALTER, since each DDL statement commits on its own.
	case coresql.DialectMySQL:
		//: the column and the index together.
		return priorityColumnStatements{
			probe: priorityProbe("column_name",
				"information_schema.columns WHERE table_schema = DATABASE() AND table_name = "+dialect.Placeholder(1)),
			add: []string{"ALTER TABLE " + q + " ADD COLUMN " + sqlPriorityColumn +
				" SMALLINT NOT NULL DEFAULT 0, ADD INDEX " + index + " " + columns},
			drop: []string{"ALTER TABLE " + q + " DROP INDEX " + index + ", DROP COLUMN " + sqlPriorityColumn},
		}
	//: SQLite: the table_info pragma, as a table-valued function.
	default:
		//: a column then its index; dropped the other way round, since SQLite
		//: refuses to drop an indexed column.
		return priorityColumnStatements{
			probe: priorityProbe("name", "pragma_table_info("+dialect.Placeholder(1)+")"),
			add: []string{
				"ALTER TABLE " + q + " ADD COLUMN " + sqlPriorityColumn + " INTEGER NOT NULL DEFAULT 0",
				"CREATE INDEX IF NOT EXISTS " + index + " ON " + q + " " + columns,
			},
			drop: []string{"DROP INDEX IF EXISTS " + index, "ALTER TABLE " + q + " DROP COLUMN " + sqlPriorityColumn},
		}
	}
}

// priorityIndexName is the name of the index over table's levels: the table's
// name and a suffix, shortened — a hash of the whole name keeping it apart
// from another's — where that would pass MaxSQLTableLen, the longest name
// every engine accepts.
func priorityIndexName(table string) string {
	name := table + sqlPriorityIndexSuffix
	//: short enough as it is.
	if len(name) <= MaxSQLTableLen {
		return name
	}
	sum := sha256.Sum256([]byte(table))
	tag := "_" + hex.EncodeToString(sum[:sqlPriorityIndexHash])
	//: as much of the table's name as fits before the tag and the suffix.
	return table[:MaxSQLTableLen-len(tag)-len(sqlPriorityIndexSuffix)] + tag + sqlPriorityIndexSuffix
}

// priorityProbe renders the read of a table's columns from catalogue, whose
// column nameColumn names each: how many there are, and how many of them are
// the priority column.
func priorityProbe(nameColumn, catalogue string) string {
	//: (0, 0) when the catalogue lists no column of the table.
	return "SELECT COUNT(*), COALESCE(SUM(CASE WHEN " + nameColumn + " = '" + sqlPriorityColumn + "' THEN 1 ELSE 0 END), 0) FROM " + catalogue
}

// step is SQLPriorityMigration's Up — the column and its index added — or,
// with drop, its Down — both dropped —, each only where it changes something.
func (s priorityColumnStatements) step(table string, drop bool) coresql.Step {
	statements := s.add
	//: the reversal.
	if drop {
		statements = s.drop
	}
	//: read the catalogue, then change it only when the change is due.
	return func(ctx context.Context, ex coresql.Executor) error {
		var columns, levels int64
		//: the table's columns, and whether the priority column is among them.
		if err := ex.QueryRowContext(ctx, s.probe, table).Scan(&columns, &levels); err != nil {
			//: the driver's failure; the runner adds the version around it.
			return err
		}
		//: no table yet, or the column already where the step would leave it.
		if columns == 0 || (levels > 0) != drop {
			//: nothing to change.
			return nil
		}
		//: in order.
		for _, statement := range statements {
			//: the driver's failure; the runner adds the version around it.
			if _, err := ex.ExecContext(ctx, statement); err != nil {
				return err
			}
		}
		//: added or dropped.
		return nil
	}
}
