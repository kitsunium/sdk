// Package sql — hosts the version table: its portable shape, the reads and
// the two writes that keep it in step with the schema.
package sql

import (
	"context"
	stdsql "database/sql"
	"strconv"

	coresql "github.com/kitsunium/sdk/internal/core/sql"
	kerrs "github.com/kitsunium/sdk/internal/kernel/errs"
)

// tablePhase labels every error raised while reading or writing the version
// table, so a failed run says which phase it died in.
const tablePhase string = "version-table"

// The bind positions of the version row's three columns, 1-based as every
// supported placeholder grammar counts them. Named because a bare 2 and 3 in
// an INSERT are exactly the literals a column reorder silently invalidates.
const (
	bindVersion   int = iota + 1 // 1
	bindName                     // 2
	bindAppliedAt                // 3
)

// createTableSQL renders the version table's definition.
//
// Three columns and no more, all of them portable:
//
//   - version BIGINT PRIMARY KEY — the identity. BIGINT is spelled the same
//     by PostgreSQL, MySQL and SQLite.
//   - name TEXT NOT NULL — so a history read during an incident says
//     something a human can act on.
//   - applied_at_unix BIGINT NOT NULL — seconds since the epoch, NOT a
//     timestamp type. TIMESTAMP / DATETIME / TEXT diverge in spelling, in
//     precision and in time-zone handling across the three engines; an
//     integer does not. The runner reads it from the injected clock, so the
//     value is deterministic in a test.
//
// The identifier is interpolated, not bound — a table name cannot be a
// parameter. It was validated against identifierPattern at construction,
// which is the whole defence.
func createTableSQL(table string) string {
	//: IF NOT EXISTS is supported by all three dialects; the engines that do
	//: not support it are the ones already refused by name in core/sql.
	return "CREATE TABLE IF NOT EXISTS " + table +
		" (version BIGINT NOT NULL PRIMARY KEY, name TEXT NOT NULL, applied_at_unix BIGINT NOT NULL)"
}

// ensureTable creates the bookkeeping table if it is absent.
func (m *migrator) ensureTable(ctx context.Context) error {
	//: the pool itself satisfies core/sql.Executor — no transaction is
	//: needed for an idempotent DDL statement, and wrapping one would be
	//: meaningless on an engine with non-transactional DDL anyway.
	_, err := m.cfg.db.ExecContext(ctx, createTableSQL(m.plan.table))
	//: a table the runner cannot create makes every later step a guess.
	if err != nil {
		//: the driver's error travels beside the verdict.
		return failed(MigrationFailed, err, kerrs.String("phase", tablePhase))
	}
	//: the history is readable.
	return nil
}

// applied reads every recorded version and the highest of them.
func (m *migrator) applied(ctx context.Context) (versions map[uint64]bool, highest uint64, err error) {
	rows, err := m.cfg.db.QueryContext(ctx, "SELECT version FROM "+m.plan.table+" ORDER BY version")
	//: an unreadable history stops the run rather than producing a plan.
	if err != nil {
		//: the driver's error travels beside the verdict.
		return nil, 0, failed(MigrationFailed, err, kerrs.String("phase", tablePhase))
	}
	//: rows are this function's, so this function closes them.
	defer func() {
		//: a close error after a successful scan still invalidates the read.
		if cerr := rows.Close(); cerr != nil && err == nil {
			//: the driver's error travels beside the verdict.
			err = failed(MigrationFailed, cerr, kerrs.String("phase", tablePhase))
		}
	}()
	//: collected into a set: membership is the only question ever asked.
	return scanVersions(rows)
}

// pending returns the migrations not yet applied, in ascending order, and
// refuses the two-branches-merged hazard.
func (m *migrator) pending(
	applied map[uint64]bool, highest uint64,
) (queued []coresql.MigrationValue, err error) {
	//: capacity is the whole set: the first run applies all of it.
	queued = make([]coresql.MigrationValue, 0, len(m.plan.migrations))
	//: the plan is already ascending, so this preserves the order.
	for _, migration := range m.plan.migrations {
		//: an applied migration is never re-run; the version IS the identity.
		if applied[migration.Version] {
			//: nothing to do.
			continue
		}
		//: a pending migration BELOW the highest applied one would slot under
		//: a change that already shipped — the schema would then match
		//: neither branch's expectation, and the version table could not
		//: express what happened.
		if migration.Version < highest {
			//: both numbers are the caller's own.
			return nil, kerrs.Wrap(MigrationOutOfOrder, kerrs.WrapParams{},
				kerrs.String("version", strconv.FormatUint(migration.Version, decimalBase)),
				kerrs.String("applied", strconv.FormatUint(highest, decimalBase)))
		}
		queued = append(queued, migration)
	}
	//: the dry run and the real run compute this identically, in one place.
	return queued, nil
}

// record writes the version row for a migration that just applied.
func (m *migrator) record(ctx context.Context, ex coresql.Executor, migration coresql.MigrationValue) error {
	query := "INSERT INTO " + m.plan.table + " (version, name, applied_at_unix) VALUES (" +
		placeholder(m.cfg.dialect, bindVersion) + ", " + placeholder(m.cfg.dialect, bindName) + ", " +
		placeholder(m.cfg.dialect, bindAppliedAt) + ")"
	//: bound as int64 because that is what BIGINT is on every engine, and
	//: MigrationValue.Validate has already refused anything that would not
	//: fit. The instant comes from the injected clock, never from time.Now.
	_, err := ex.ExecContext(ctx, query,
		int64(migration.Version), migration.Name, m.cfg.clk.Now().Unix())
	//: the row and the schema change commit together, or neither does.
	return err
}

// forget removes the version row for a migration that was just reversed.
func (m *migrator) forget(ctx context.Context, ex coresql.Executor, migration coresql.MigrationValue) error {
	query := "DELETE FROM " + m.plan.table + " WHERE version = " + placeholder(m.cfg.dialect, bindVersion)
	//: same binding rule as record.
	_, err := ex.ExecContext(ctx, query, int64(migration.Version))
	//: the row and the reversal commit together, or neither does.
	return err
}

// scanVersions collects the version column into a set and tracks the highest.
func scanVersions(rows *stdsql.Rows) (versions map[uint64]bool, highest uint64, err error) {
	versions = make(map[uint64]bool)
	//: one pass over the whole recorded history; membership is the only
	//: question ever asked of it, so it collects into a set.
	for rows.Next() {
		var recorded int64
		//: a row the driver cannot decode makes the whole history unreliable.
		if serr := rows.Scan(&recorded); serr != nil {
			//: the driver's error travels beside the verdict.
			return nil, 0, failed(MigrationFailed, serr, kerrs.String("phase", tablePhase))
		}
		//: a negative version cannot come from this runner, so the table has
		//: been written by something else — refuse rather than wrap it into a
		//: colossal uint64 and silently reorder the history.
		if recorded < 0 {
			//: no driver error to attach; the row itself is the fact.
			return nil, 0, failed(MigrationFailed, nil,
				kerrs.String("phase", tablePhase), kerrs.Int64("version", recorded))
		}
		version := uint64(recorded)
		versions[version] = true
		//: the read is ORDER BY version, but the highest is tracked
		//: explicitly rather than assumed from the last row: an engine is
		//: free to return an equal-keyed set in any order it likes.
		if version > highest {
			//: the ceiling every pending migration must sit above.
			highest = version
		}
	}
	//: rows.Err reports a failure that ended the iteration early, which
	//: Next() reports only as "no more rows".
	if rerr := rows.Err(); rerr != nil {
		//: the driver's error travels beside the verdict.
		return nil, 0, failed(MigrationFailed, rerr, kerrs.String("phase", tablePhase))
	}
	//: the complete recorded history.
	return versions, highest, nil
}
