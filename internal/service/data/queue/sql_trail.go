package queue

import (
	"context"
	"strconv"

	coresql "github.com/kitsunium/sdk/internal/core/data/sql"
	svcsql "github.com/kitsunium/sdk/internal/service/data/sql"
)

// trailColumnStatements are the statements SQLTrailMigration sends to add
// the trail column to a queue's table, or to drop it, on one dialect.
//
// PostgreSQL says "only when it is (not) there" in the statement itself.
// SQLite and MySQL have no IF EXISTS nor IF NOT EXISTS on a column, so the
// migration reads the table's columns from the catalogue first — with the
// table's name bound, never interpolated — and sends the ALTER only when it
// changes something: a run that MySQL's implicit commit stopped after its ALTER, a
// table SQLMigration created with the column, and a table not created yet
// all leave nothing to do.
type trailColumnStatements struct {
	// probe counts the table's columns and, among them, the trail column:
	// (0, 0) for no table. Empty on PostgreSQL.
	probe string
	// add adds the column.
	add string
	// drop drops it.
	drop string
}

// renderTrailColumnStatements renders SQLTrailMigration's statements over
// table on dialect. The table name was validated by the caller.
func renderTrailColumnStatements(dialect coresql.Dialect, table string) trailColumnStatements {
	q := dialect.QuoteIdent(table)
	column := sqlTrailColumn + " " + trailColumnType(dialect)
	//: the catalogue each engine keeps its columns in.
	switch dialect {
	//: PostgreSQL: the statement is its own condition.
	case coresql.DialectPostgres:
		//: nothing to read first.
		return trailColumnStatements{
			add:  "ALTER TABLE IF EXISTS " + q + " ADD COLUMN IF NOT EXISTS " + column,
			drop: "ALTER TABLE IF EXISTS " + q + " DROP COLUMN IF EXISTS " + sqlTrailColumn,
		}
	//: MySQL and MariaDB: information_schema, in the connection's database.
	case coresql.DialectMySQL:
		//: the columns of the table, in the current database.
		return trailColumnStatements{
			probe: trailProbe("column_name",
				"information_schema.columns WHERE table_schema = DATABASE() AND table_name = "+dialect.Placeholder(1)),
			add:  "ALTER TABLE " + q + " ADD COLUMN " + column,
			drop: "ALTER TABLE " + q + " DROP COLUMN " + sqlTrailColumn,
		}
	//: SQLite: the table_info pragma, as a table-valued function.
	default:
		//: the columns of the table.
		return trailColumnStatements{
			probe: trailProbe("name", "pragma_table_info("+dialect.Placeholder(1)+")"),
			add:   "ALTER TABLE " + q + " ADD COLUMN " + column,
			drop:  "ALTER TABLE " + q + " DROP COLUMN " + sqlTrailColumn,
		}
	}
}

// trailProbe renders the read of a table's columns from catalogue, whose
// column nameColumn names each: how many there are, and how many of them
// are the trail column.
func trailProbe(nameColumn, catalogue string) string {
	//: (0, 0) when the catalogue lists no column of the table.
	return "SELECT COUNT(*), COALESCE(SUM(CASE WHEN " + nameColumn + " = '" + sqlTrailColumn + "' THEN 1 ELSE 0 END), 0) FROM " + catalogue
}

// step is SQLTrailMigration's Up — the column added — or, with drop, its
// Down — the column dropped —, each only where it changes something.
func (s trailColumnStatements) step(table string, drop bool) coresql.Step {
	statement := s.add
	//: the reversal.
	if drop {
		statement = s.drop
	}
	//: PostgreSQL's statement decides for itself.
	if s.probe == "" {
		//: one statement, sent as it is.
		return svcsql.Statements(statement)
	}
	//: read the catalogue, then change it only when the change is due.
	return func(ctx context.Context, ex coresql.Executor) error {
		var columns, trails int64
		//: the table's columns, and whether the trail is among them.
		if err := ex.QueryRowContext(ctx, s.probe, table).Scan(&columns, &trails); err != nil {
			//: the driver's failure; the runner adds the version around it.
			return err
		}
		//: no table yet — SQLMigration creates it with the column —, or the
		//: column already where the step would leave it.
		if columns == 0 || (trails > 0) != drop {
			//: nothing to change.
			return nil
		}
		_, err := ex.ExecContext(ctx, statement)
		//: added or dropped, or the driver's failure.
		return err
	}
}

// trailColumnType is the trail column's type on dialect: bytes, compared as
// bytes, NULL for a dead letter with no trail.
func trailColumnType(dialect coresql.Dialect) string {
	//: each engine's binary type.
	switch dialect {
	//: sized, as every MySQL byte column is.
	case coresql.DialectMySQL:
		//: maxDeadTrail codes and their commas.
		return "VARBINARY(" + strconv.Itoa(sqlTrailColumnLen) + ") NULL"
	//: PostgreSQL's bytes.
	case coresql.DialectPostgres:
		//: unbounded.
		return "bytea"
	//: SQLite's.
	default:
		//: compared byte for byte.
		return "BLOB"
	}
}
