package sql

import (
	"context"
	stdsql "database/sql"
	"iter"
	"strconv"
	"strings"

	coresql "github.com/kitsunium/sdk/internal/core/data/sql"
	kerrs "github.com/kitsunium/sdk/internal/kernel/errs"
)

// The bound-parameter ceilings InsertRows clamps a statement to: SQLite's
// SQLITE_MAX_VARIABLE_NUMBER as the oldest releases ship it, and the 16-bit
// count PostgreSQL's and MySQL's wire protocols carry.
const (
	sqliteMaxParams int = 999
	serverMaxParams int = 65535
)

// Each iterates rows, scanning each with scan, and closes rows when the
// iteration ends — at the last row, at the first failure, or when the loop
// stops early. A failure — scan's, rows.Err's or the close's — is yielded
// once, as the last element, beside T's zero value:
//
//	rows, err := ex.QueryContext(ctx, "SELECT id, name FROM accounts")
//	if err != nil {
//		return err
//	}
//	for account, err := range sql.Each(rows, scanAccount) {
//		if err != nil {
//			return err
//		}
//		use(account)
//	}
//
// It is the Next/Scan/Err/Close loop every caller writes, written once; it
// scans nothing itself, since mapping a row is the caller's (ADR 0055 §D1).
// A nil rows yields nothing.
func Each[T any](rows *stdsql.Rows, scan func(*stdsql.Rows) (T, error)) iter.Seq2[T, error] {
	return func(yield func(T, error) bool) {
		//: nothing to read, nothing to close.
		if rows == nil {
			//: an empty sequence.
			return
		}
		//: the rows are closed however the loop ends, the close's own failure
		//: yielded when nothing failed before it.
		closed := false
		defer func() {
			//: the paths below close explicitly; this one is a stop or a panic.
			if !closed {
				_ = rows.Close()
			}
		}()
		for rows.Next() {
			value, err := scan(rows)
			//: a row the caller cannot read ends the iteration, said once.
			if err != nil {
				closed = true
				//: the rows closed before the failure is said; nothing after it.
				yieldFailure(yield, firstFailure(err, rows.Close()))
				return
			}
			//: the caller stopped: the deferred close runs.
			if !yield(value, nil) {
				//: no failure to report to a loop that has left.
				return
			}
		}
		closed = true
		//: the failure that ended the iteration early, which Next reports only
		//: as "no more rows", else the close's own: a close that fails after a
		//: complete read still fails the read.
		if err := firstFailure(rows.Err(), rows.Close()); err != nil {
			yieldFailure(yield, err)
		}
	}
}

// firstFailure is err, or closeErr when err is nil: one failure is said, the
// first, and a close's only when nothing failed before it.
func firstFailure(err, closeErr error) error {
	//: the failure that came first.
	if err != nil {
		//: said alone.
		return err
	}
	//: nil when the close succeeded too.
	return closeErr
}

// yieldFailure yields err once beside T's zero value: the last thing a walk
// says, whatever the caller answers.
func yieldFailure[T any](yield func(T, error) bool, err error) {
	var zero T
	//: the only yield; there is nothing after it either way.
	if !yield(zero, err) {
		//: the caller stopped.
		return
	}
}

// InsertSpec is the statement [InsertRows] writes: the table, its columns
// in the order each row gives its values, and how many rows one statement
// carries at most.
type InsertSpec struct {
	// Table is the table the rows go into, interpolated as given — bare,
	// or already quoted with [coresql.Dialect].QuoteIdent. Validate it
	// first.
	Table string
	// Columns are the table's columns each row gives a value for, in that
	// order, interpolated as given like Table.
	Columns []string
	// PerStatement is how many rows one statement carries at most,
	// clamped to the engine's bound-parameter ceiling divided by the
	// number of columns; a non-positive one takes that ceiling.
	PerStatement int
}

// InsertRows inserts rows into into.Table, into.PerStatement rows per
// statement, on ex, and returns how many rows the statements report
// inserted.
//
// Each statement reads INSERT INTO table (cols) VALUES (…), (…), its bind
// markers spelled by d and numbered across the statement. into.Table and
// into.Columns are interpolated exactly as given — bare, or already quoted
// with [coresql.Dialect].QuoteIdent — and must have been validated by the
// caller, which is the whole defence of an interpolated identifier; the
// values are always bound.
//
// into.PerStatement is clamped to what d's engine binds in one statement —
// 999 parameters on SQLite, the oldest ceiling it ships with, and 65535 on
// PostgreSQL and MySQL — divided by the number of columns; a non-positive
// one takes that ceiling. A row whose width is not the number of columns,
// no columns at all or a dialect that is not Valid is refused with
// [coresql.ConfigInvalid] before any statement runs. A statement's failure
// is returned verbatim, with the count of rows the statements before it
// inserted; run InsertRows in a transaction for all or none.
func InsertRows(ctx context.Context, ex coresql.Executor, d coresql.Dialect, into InsertSpec, rows [][]any) (inserted int64, err error) {
	//: a statement the engine could not parse is refused before it is sent.
	if err := insertable(d, into.Columns, rows); err != nil {
		//: CONFIG_INVALID, nothing sent.
		return 0, err
	}
	cols := into.Columns
	per := rowsPerStatement(d, len(cols), into.PerStatement)
	head := "INSERT INTO " + into.Table + " (" + strings.Join(cols, ", ") + ") VALUES "
	args := make([]any, 0, min(len(rows), per)*len(cols))
	//: a bounded number of rows per statement, in the order given.
	for start := 0; start < len(rows); start += per {
		chunk := rows[start:min(start+per, len(rows))]
		args = args[:0]
		for _, row := range chunk {
			args = append(args, row...)
		}
		result, execErr := ex.ExecContext(ctx, valuesSQL(head, d, len(chunk), len(cols)), args...)
		//: verbatim, with what the statements before it inserted.
		if execErr != nil {
			//: the caller's transaction decides what stands.
			return inserted, execErr
		}
		//: a driver that cannot count is read as having inserted the chunk.
		if n, countErr := result.RowsAffected(); countErr == nil {
			inserted += n
		} else {
			inserted += int64(len(chunk))
		}
	}
	//: every row sent.
	return inserted, nil
}

// insertable refuses what could never be a statement: an unusable dialect, no
// column, or a row whose width is not the columns'.
func insertable(d coresql.Dialect, cols []string, rows [][]any) error {
	//: an unset dialect renders no marker.
	if !d.Valid() {
		//: the missing field names which half is absent.
		return kerrs.Wrap(coresql.ConfigInvalid, kerrs.WrapParams{}, kerrs.String("missing", "dialect"))
	}
	//: an INSERT names at least one column.
	if len(cols) == 0 {
		//: the missing field names which half is absent.
		return kerrs.Wrap(coresql.ConfigInvalid, kerrs.WrapParams{}, kerrs.String("missing", "columns"))
	}
	//: every row binds one value per column.
	for i, row := range rows {
		//: a short or long row would shift every value after it.
		if len(row) != len(cols) {
			//: the row's position, never its values.
			return kerrs.Wrap(coresql.ConfigInvalid, kerrs.WrapParams{},
				kerrs.String("invalid", "row width"), kerrs.Int("row", i), kerrs.Int("width", len(row)),
				kerrs.Int("columns", len(cols)))
		}
	}
	//: a statement can be rendered.
	return nil
}

// rowsPerStatement clamps asked to what one statement of d binds.
func rowsPerStatement(d coresql.Dialect, width, asked int) int {
	ceiling := serverMaxParams
	//: SQLite's ceiling is far lower on the releases still shipped.
	if d == coresql.DialectSQLite {
		ceiling = sqliteMaxParams
	}
	most := max(ceiling/width, 1)
	//: unset, or above what the engine binds: the most it binds.
	if asked <= 0 || asked > most {
		//: the engine's own ceiling.
		return most
	}
	//: the caller's own bound.
	return asked
}

// valuesSQL renders head followed by n tuples of width markers, numbered
// across the statement.
func valuesSQL(head string, d coresql.Dialect, n, width int) string {
	var b strings.Builder
	b.Grow(len(head) + n*width*len(strconv.Itoa(n*width))*2)
	b.WriteString(head)
	//: one tuple per row.
	for row := range n {
		//: rows are comma-separated.
		if row > 0 {
			b.WriteString(", ")
		}
		b.WriteString("(")
		//: one marker per column.
		for col := 1; col <= width; col++ {
			//: columns are comma-separated.
			if col > 1 {
				b.WriteString(", ")
			}
			b.WriteString(d.Placeholder(row*width + col))
		}
		b.WriteString(")")
	}
	//: n rows in one round trip.
	return b.String()
}
