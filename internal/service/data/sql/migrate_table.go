package sql

import (
	"context"
	stdsql "database/sql"
	"strconv"

	coresql "github.com/kitsunium/sdk/internal/core/data/sql"
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
	bindChecksum                 // 4
)

// bindBackfillChecksum and bindBackfillVersion are the bind positions of the
// backfill's UPDATE, whose SET comes before its WHERE.
const (
	bindBackfillChecksum int = iota + 1 // 1
	bindBackfillVersion                 // 2
)

// createTableSQL renders the version table's definition.
//
// Three columns, all of them portable, and a fourth for a set that declares
// checksums:
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
//   - checksum TEXT, nullable — the fingerprint of the statements the
//     migration ran (#1056), NULL for one that declares none. Only a set
//     that declares a checksum creates it, so a set that declares none sends
//     the statement it always sent; a three-column table gets it from the
//     first locked run whose set declares one (ensureTable).
//
// The identifier is interpolated, not bound — a table name cannot be a
// parameter. It was validated by isIdentifier at construction,
// which is the whole defence.
func createTableSQL(table string, checksums bool) string {
	columns := "version BIGINT NOT NULL PRIMARY KEY, name TEXT NOT NULL, applied_at_unix BIGINT NOT NULL"
	//: the fourth column, for a set that declares a checksum.
	if checksums {
		columns += ", checksum TEXT"
	}
	//: IF NOT EXISTS is supported by all three dialects; the engines that do
	//: not support it are the ones already refused by name in core/data/sql.
	return "CREATE TABLE IF NOT EXISTS " + table + " (" + columns + ")"
}

// checksumProbeSQL renders the read that answers whether the version table
// has its checksum column: a SELECT of it that matches no row, which an
// engine refuses when the column is absent and answers with nothing
// otherwise. Reading the catalogue instead would be three dialects' worth
// of information_schema, pragma_table_info and their quirks.
func checksumProbeSQL(table string) string {
	//: matches no row on every engine.
	return "SELECT checksum FROM " + table + " WHERE 1 = 0"
}

// addChecksumSQL renders the statement that gives a three-column version
// table, created by an earlier release, its checksum column. The three
// engines spell it alike, and a nullable column needs no default.
func addChecksumSQL(table string) string {
	//: the column createTableSQL declares.
	return "ALTER TABLE " + table + " ADD COLUMN checksum TEXT"
}

// ensureTable creates the bookkeeping table if it is absent and, under the
// migration lock, gives one created before checksums existed its checksum
// column when the set declares a checksum.
//
// It runs where ctx says. Under an advisory lock that is the pool — no
// transaction is needed for an idempotent DDL statement, and wrapping one would
// be meaningless on an engine with non-transactional DDL anyway. Under SQLite's
// file lock it is the run's own transaction: the pool's other connections are
// exactly the writers the lock is holding off, this one included.
func (m *migrator) ensureTable(ctx context.Context, locked bool) error {
	ex, _ := m.tx.Join(ctx)
	_, err := ex.ExecContext(ctx, createTableSQL(m.plan.table, m.plan.checksums))
	//: a table the runner cannot create makes every later step a guess.
	if err != nil {
		//: the driver's error travels beside the verdict.
		return failed(coresql.MigrationFailed, err, kerrs.String("phase", tablePhase))
	}
	//: a set that declares no checksum never reads or writes the column, and
	//: Plan, which takes no lock and applies nothing, never adds it.
	if !m.plan.checksums || !locked || m.hasChecksumColumn(ctx) {
		//: the history is readable.
		return nil
	}
	_, err = ex.ExecContext(ctx, addChecksumSQL(m.plan.table))
	//: a table that cannot take the column cannot record a checksum.
	if err != nil {
		//: the driver's error travels beside the verdict.
		return failed(coresql.MigrationFailed, err, kerrs.String("phase", tablePhase))
	}
	//: the column is there, every row recorded before it NULL in it.
	return nil
}

// hasChecksumColumn reports whether the version table has its checksum
// column, by the probe checksumProbeSQL renders. Any refusal is read as an
// absent column: a failure that is something else fails again at the ALTER
// or at the read that follows, with the driver's own error.
func (m *migrator) hasChecksumColumn(ctx context.Context) bool {
	ex, _ := m.tx.Join(ctx)
	rows, err := ex.QueryContext(ctx, checksumProbeSQL(m.plan.table))
	//: refused: the column is absent.
	if err != nil {
		//: no column to read.
		return false
	}
	//: the probe returns no row; closing it is the whole of reading it, and a
	//: close error says the read failed.
	return rows.Close() == nil
}

// applied reads every recorded version and the highest of them, where ctx
// says, for the reason ensureTable gives. A set that declares checksums reads
// them too and refuses, before anything is applied, an applied migration
// whose declared checksum is not the recorded one (direction names the run);
// under the lock it then backfills the versions recorded without one.
func (m *migrator) applied(
	ctx context.Context, locked bool, direction string,
) (versions map[uint64]bool, highest uint64, err error) {
	//: the checksum column is read only for a set that declares one — and,
	//: outside the lock, only where an earlier run added it.
	if m.plan.checksums && (locked || m.hasChecksumColumn(ctx)) {
		//: versions, checksums, the comparison and the backfill.
		return m.verified(ctx, locked, direction)
	}
	ex, _ := m.tx.Join(ctx)
	rows, err := ex.QueryContext(ctx, "SELECT version FROM "+m.plan.table+" ORDER BY version")
	//: an unreadable history stops the run rather than producing a plan.
	if err != nil {
		//: the driver's error travels beside the verdict.
		return nil, 0, failed(coresql.MigrationFailed, err, kerrs.String("phase", tablePhase))
	}
	//: rows are this function's, so this function closes them.
	defer func() {
		//: a close error after a successful scan still invalidates the read.
		if cerr := rows.Close(); cerr != nil && err == nil {
			//: the driver's error travels beside the verdict.
			err = failed(coresql.MigrationFailed, cerr, kerrs.String("phase", tablePhase))
		}
	}()
	//: collected into a set: membership is the only question ever asked.
	return scanVersions(rows)
}

// verified reads the recorded versions with their checksums, refuses a
// changed migration, and under the lock backfills the unrecorded checksums
// the set declares.
func (m *migrator) verified(
	ctx context.Context, locked bool, direction string,
) (versions map[uint64]bool, highest uint64, err error) {
	sums, highest, err := m.recordedChecksums(ctx)
	//: an unreadable history stops the run.
	if err != nil {
		//: propagate MIGRATION_FAILED unchanged.
		return nil, 0, err
	}
	missing, changed := m.compareChecksums(sums, direction)
	//: a migration whose text is not the text that ran.
	if changed != nil {
		//: MIGRATION_CHANGED, the version and the direction, never the text.
		return nil, 0, changed
	}
	//: Plan applies nothing; Up and Down record what they verified.
	if locked {
		//: each unrecorded checksum, written where ctx says.
		if err := m.backfill(ctx, missing); err != nil {
			//: propagate MIGRATION_FAILED unchanged.
			return nil, 0, err
		}
	}
	versions = make(map[uint64]bool, len(sums))
	//: membership, as every caller asks it.
	for version := range sums {
		versions[version] = true
	}
	//: the complete, verified history.
	return versions, highest, nil
}

// compareChecksums holds every declared checksum against the recorded one,
// before anything runs: it returns the applied migrations recorded before
// checksums existed — the next locked run fills them —, or MIGRATION_CHANGED
// for the first whose text is not the text that ran.
func (m *migrator) compareChecksums(
	sums map[uint64]stdsql.NullString, direction string,
) (missing []coresql.MigrationValue, changed error) {
	//: every declared checksum, in the plan's order.
	for _, migration := range m.plan.migrations {
		recorded, applied := sums[migration.Version]
		//: a migration never applied, or one that declares nothing, is not
		//: compared.
		if !applied || migration.Checksum == "" {
			//: nothing to compare.
			continue
		}
		//: recorded before checksums existed: the next locked run fills it.
		if !recorded.Valid {
			missing = append(missing, migration)
			//: nothing to compare yet.
			continue
		}
		//: the text that ran is not the text this build carries.
		if recorded.String != migration.Checksum {
			//: the version and the direction, never the text.
			return nil, kerrs.Wrap(coresql.MigrationChanged, kerrs.WrapParams{},
				versionField(migration), directionField(direction))
		}
	}
	//: every applied text is the text that ran.
	return missing, nil
}

// recordedChecksums reads every recorded version with its checksum, NULL for
// one recorded without.
func (m *migrator) recordedChecksums(
	ctx context.Context,
) (sums map[uint64]stdsql.NullString, highest uint64, err error) {
	ex, _ := m.tx.Join(ctx)
	rows, err := ex.QueryContext(ctx, "SELECT version, checksum FROM "+m.plan.table+" ORDER BY version")
	//: an unreadable history stops the run rather than producing a plan.
	if err != nil {
		//: the driver's error travels beside the verdict.
		return nil, 0, failed(coresql.MigrationFailed, err, kerrs.String("phase", tablePhase))
	}
	//: rows are this function's, so this function closes them.
	defer func() {
		//: a close error after a successful scan still invalidates the read.
		if cerr := rows.Close(); cerr != nil && err == nil {
			//: the driver's error travels beside the verdict.
			err = failed(coresql.MigrationFailed, cerr, kerrs.String("phase", tablePhase))
		}
	}()
	sums = make(map[uint64]stdsql.NullString)
	//: one pass over the whole recorded history.
	for rows.Next() {
		var recorded int64
		var sum stdsql.NullString
		//: a row the driver cannot decode makes the whole history unreliable.
		if serr := rows.Scan(&recorded, &sum); serr != nil {
			//: the driver's error travels beside the verdict.
			return nil, 0, failed(coresql.MigrationFailed, serr, kerrs.String("phase", tablePhase))
		}
		//: written by something else than this runner, as scanVersions says.
		if recorded < 0 {
			//: the row itself is the fact.
			return nil, 0, failed(coresql.MigrationFailed, nil,
				kerrs.String("phase", tablePhase), kerrs.Int64("version", recorded))
		}
		sums[uint64(recorded)] = sum
		highest = max(highest, uint64(recorded))
	}
	//: a failure that ended the iteration early.
	if rerr := rows.Err(); rerr != nil {
		//: the driver's error travels beside the verdict.
		return nil, 0, failed(coresql.MigrationFailed, rerr, kerrs.String("phase", tablePhase))
	}
	//: the complete recorded history.
	return sums, highest, nil
}

// backfill writes the declared checksum of each migration recorded without
// one. The WHERE keeps a checksum another writer recorded meanwhile.
func (m *migrator) backfill(ctx context.Context, missing []coresql.MigrationValue) error {
	ex, _ := m.tx.Join(ctx)
	query := "UPDATE " + m.plan.table + " SET checksum = " + m.cfg.dialect.Placeholder(bindBackfillChecksum) +
		" WHERE version = " + m.cfg.dialect.Placeholder(bindBackfillVersion) + " AND checksum IS NULL"
	//: one row each, in ascending order.
	for _, migration := range missing {
		//: bound as BIGINT, as record binds it.
		if _, err := ex.ExecContext(ctx, query, migration.Checksum, int64(migration.Version)); err != nil {
			//: the driver's error travels beside the verdict.
			return failed(coresql.MigrationFailed, err, kerrs.String("phase", tablePhase), versionField(migration))
		}
	}
	//: every declared checksum is recorded.
	return nil
}

// checksumArg is a declared checksum as the version row binds it: NULL for a
// migration that declares none, so "unverified" is never an empty string a
// later comparison would read as a fingerprint.
func checksumArg(sum string) any {
	//: unverified.
	if sum == "" {
		//: NULL.
		return nil
	}
	//: the fingerprint.
	return sum
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
			return nil, kerrs.Wrap(coresql.MigrationOutOfOrder, kerrs.WrapParams{},
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
	//: a set that declares checksums records them, NULL for one that does not.
	if m.plan.checksums {
		query := "INSERT INTO " + m.plan.table + " (version, name, applied_at_unix, checksum) VALUES (" +
			m.cfg.dialect.Placeholder(bindVersion) + ", " + m.cfg.dialect.Placeholder(bindName) + ", " +
			m.cfg.dialect.Placeholder(bindAppliedAt) + ", " + m.cfg.dialect.Placeholder(bindChecksum) + ")"
		_, err := ex.ExecContext(ctx, query,
			int64(migration.Version), migration.Name, m.cfg.clk.Now().Unix(), checksumArg(migration.Checksum))
		//: the row and the schema change commit together, or neither does.
		return err
	}
	query := "INSERT INTO " + m.plan.table + " (version, name, applied_at_unix) VALUES (" +
		m.cfg.dialect.Placeholder(bindVersion) + ", " + m.cfg.dialect.Placeholder(bindName) + ", " +
		m.cfg.dialect.Placeholder(bindAppliedAt) + ")"
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
	query := "DELETE FROM " + m.plan.table + " WHERE version = " + m.cfg.dialect.Placeholder(bindVersion)
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
			return nil, 0, failed(coresql.MigrationFailed, serr, kerrs.String("phase", tablePhase))
		}
		//: a negative version cannot come from this runner, so the table has
		//: been written by something else — refuse rather than wrap it into a
		//: colossal uint64 and silently reorder the history.
		if recorded < 0 {
			//: no driver error to attach; the row itself is the fact.
			return nil, 0, failed(coresql.MigrationFailed, nil,
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
		return nil, 0, failed(coresql.MigrationFailed, rerr, kerrs.String("phase", tablePhase))
	}
	//: the complete recorded history.
	return versions, highest, nil
}
