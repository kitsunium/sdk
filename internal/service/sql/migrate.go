// Package sql — hosts the migration runner: the order, the version table, the
// dry run, and the two directions.
package sql

import (
	"context"
	"errors"
	"maps"
	"slices"
	"strconv"

	coresql "github.com/kitsunium/sdk/internal/core/sql"
	kerrs "github.com/kitsunium/sdk/internal/kernel/errs"
)

// directionUp / directionDown label a migration failure so the error says
// which way the schema was moving when it broke.
const (
	directionUp   string = "up"
	directionDown string = "down"
)

// migrator is the concrete core/sql.Migrator. It stays unexported behind
// [NewMigrator] (IFACE-PLUGIN).
type migrator struct {
	// cfg is the validated, clamped connection configuration.
	cfg resolved
	// plan is the validated, sorted migration set and its budgets.
	plan migratePlan
	// tx opens one transaction per migration. It is the SAME manager type a
	// consumer uses, so a migration step and application code see identical
	// transaction semantics.
	tx coresql.Transactor
}

// NewMigrator returns the migration runner for cfg.DB.
//
// It REFUSES a dialect with no session-scoped advisory lock, at construction
// and by name. Running unlocked is not offered as a fallback: a runner that
// silently drops mutual exclusion is at its most dangerous in exactly the
// situation it exists for — two instances of a deployment starting together.
func NewMigrator(cfg Config, mig MigrateConfig) (runner coresql.Migrator, err error) {
	res, err := cfg.resolve()
	//: a refused Config leaves the caller's DB exactly as it was.
	if err != nil {
		//: propagate CONFIG_INVALID / POOL_MISCONFIGURED unchanged.
		return nil, err
	}
	//: no advisory lock means no promise this runner is willing to make.
	if !res.dialect.SupportsAdvisoryLock() {
		//: name the dialect; it is the caller's own configuration.
		return nil, kerrs.Wrap(MigrationLockUnsupported, kerrs.WrapParams{},
			kerrs.String("dialect", res.dialect.String()))
	}
	plan, err := mig.resolve()
	//: an unrunnable migration set is refused before any connection is used.
	if err != nil {
		//: propagate the set's own verdict unchanged.
		return nil, err
	}
	//: one manager, shared by every migration transaction.
	return &migrator{cfg: res, plan: plan, tx: &transactor{cfg: res}}, nil
}

// Plan reports what [migrator.Up] would apply, without applying anything and
// without taking the migration lock.
//
// It DOES create the version table if it is absent, and that is stated rather
// than hidden: reading a table that does not exist is not a portable
// operation — every engine reports it with its own SQLSTATE — so the only
// alternative would be to guess from a driver error string. Creating the
// runner's own bookkeeping table is not a schema migration.
//
// The price of not taking the lock is also stated: on PostgreSQL, two
// concurrent CREATE TABLE IF NOT EXISTS can race on pg_class and one of them
// gets a unique violation. That is a spurious failure the caller retries, not
// a corrupted schema — and it is a better trade than a dry run that blocks
// behind a real migration for its entire duration.
func (m *migrator) Plan(ctx context.Context) (pending []coresql.MigrationValue, err error) {
	//: the bookkeeping table must exist before it can be read.
	if err := m.ensureTable(ctx); err != nil {
		//: propagate MIGRATION_FAILED unchanged.
		return nil, err
	}
	applied, highest, err := m.applied(ctx)
	//: an unreadable version table makes every answer a guess.
	if err != nil {
		//: propagate MIGRATION_FAILED unchanged.
		return nil, err
	}
	//: the set difference, in the order it would be applied.
	return m.pending(applied, highest)
}

// Up applies every pending migration in ascending version order, each in its
// own transaction, under the migration lock.
func (m *migrator) Up(ctx context.Context) (err error) {
	lock, err := m.acquire(ctx)
	//: nothing is applied without the lock — that is the whole guarantee.
	if err != nil {
		//: propagate MIGRATION_LOCK_TIMEOUT / MIGRATION_FAILED unchanged.
		return err
	}
	//: the lock is released even if a migration panics; the connection close
	//: inside release is what makes the guarantee survive a crash too. Its
	//: failure is JOINED rather than discarded, for the reason RollbackFailed
	//: gives: a teardown that also breaks is a second defect, and reporting
	//: only one of the two hides the other. A stuck unlock costs the NEXT
	//: runner a full LockTimeout, which is worth a caller's attention.
	defer func() { err = errors.Join(err, lock.release(ctx)) }()
	//: the plan is recomputed UNDER the lock: the one computed before it
	//: could have been made stale by the holder that just finished.
	pending, err := m.Plan(ctx)
	//: an unreadable or inconsistent history stops the run.
	if err != nil {
		//: propagate the planning verdict unchanged.
		return err
	}
	//: ascending order, one transaction each, stopping at the first failure.
	for _, migration := range pending {
		//: a half-applied set is not retried past its own failure.
		if err := m.applyOne(ctx, migration); err != nil {
			//: propagate MIGRATION_FAILED unchanged.
			return err
		}
	}
	//: the schema is at the highest version this build carries.
	return nil
}

// Down reverses every applied migration above target, in descending order.
func (m *migrator) Down(ctx context.Context, target uint64) (err error) {
	lock, err := m.acquire(ctx)
	//: nothing is reversed without the lock either.
	if err != nil {
		//: propagate MIGRATION_LOCK_TIMEOUT / MIGRATION_FAILED unchanged.
		return err
	}
	//: released on every path, including a panicking Down step; the release
	//: verdict is joined for the same reason Up joins it.
	defer func() { err = errors.Join(err, lock.release(ctx)) }()
	//: the bookkeeping table must exist before it can be read.
	if err := m.ensureTable(ctx); err != nil {
		//: propagate MIGRATION_FAILED unchanged.
		return err
	}
	applied, _, err := m.applied(ctx)
	//: an unreadable version table makes every reversal a guess.
	if err != nil {
		//: propagate MIGRATION_FAILED unchanged.
		return err
	}
	//: descending order, one transaction each.
	return m.reverseAll(ctx, applied, target)
}

// reverseAll walks the applied set downwards to target.
func (m *migrator) reverseAll(ctx context.Context, applied map[uint64]bool, target uint64) error {
	//: checked BEFORE anything is reversed. An unknown version above the
	//: target was applied AFTER the ones this build carries, so undoing the
	//: known ones first would reverse them underneath a change still in
	//: place — the exact ordering violation Down exists to prevent.
	if err := m.assertNoUnknown(applied, target); err != nil {
		//: nothing was reversed.
		return err
	}
	//: descending, so a migration is never reversed before one that was
	//: applied after it.
	for _, migration := range slices.Backward(m.plan.migrations) {
		//: at or below the target, and anything never applied, is skipped.
		if migration.Version <= target || !applied[migration.Version] {
			//: nothing to undo.
			continue
		}
		//: a failure stops the walk: continuing would reverse a migration
		//: whose successor is still applied.
		if err := m.revertOne(ctx, migration); err != nil {
			//: propagate MIGRATION_FAILED unchanged.
			return err
		}
	}
	//: the schema is back at the target version.
	return nil
}

// assertNoUnknown refuses a reversal while the database records a version
// this build cannot reverse.
//
// The database being AHEAD of the code is a real deployment state — a rollback
// to a previous image is exactly how it happens — and guessing is not an
// option: reversing a change whose Down step is not present would be inventing
// one.
func (m *migrator) assertNoUnknown(applied map[uint64]bool, target uint64) error {
	known := make(map[uint64]bool, len(m.plan.migrations))
	//: the set this build can actually reverse — a Down step it does not
	//: carry is one it would have to invent.
	for _, migration := range m.plan.migrations {
		known[migration.Version] = true
	}
	//: sorted so the reported version is deterministic across runs.
	remaining := slices.Sorted(maps.Keys(applied))
	//: ascending, so the LOWEST unreversible version is the one reported —
	//: it is the one an operator has to deal with first.
	for _, version := range remaining {
		//: below the target it stays applied on purpose.
		if version <= target || known[version] {
			//: nothing to say about this one.
			continue
		}
		//: the database knows a migration this binary does not.
		return kerrs.Wrap(MigrationUnknownVersion, kerrs.WrapParams{},
			kerrs.String("version", strconv.FormatUint(version, decimalBase)))
	}
	//: every remaining version is either intentional or reversible.
	return nil
}

// applyOne runs one migration's Up and records it, in ONE transaction.
func (m *migrator) applyOne(ctx context.Context, migration coresql.MigrationValue) error {
	err := m.tx.Transact(ctx, coresql.TxOptionsValue{}, func(txCtx context.Context, ex coresql.Executor) error {
		//: the step runs on the SAME executor as the bookkeeping row, which
		//: is what makes the two commit together on a transactional-DDL
		//: engine — see the package CLAUDE.md for the MySQL caveat.
		if stepErr := migration.Up(txCtx, ex); stepErr != nil {
			//: verbatim, so the caller's errors.Is still answers.
			return stepErr
		}
		//: the version row is written by the same transaction it describes.
		return m.record(txCtx, ex, migration)
	})
	//: nothing applied, nothing recorded.
	if err != nil {
		//: the step's own error travels beside the verdict.
		return failed(MigrationFailed, err, versionField(migration), directionField(directionUp))
	}
	//: applied and recorded.
	return nil
}

// revertOne runs one migration's Down and forgets it, in ONE transaction.
func (m *migrator) revertOne(ctx context.Context, migration coresql.MigrationValue) error {
	err := m.tx.Transact(ctx, coresql.TxOptionsValue{}, func(txCtx context.Context, ex coresql.Executor) error {
		//: an Irreversible step fails here, with the caller's own declaration.
		if stepErr := migration.Down(txCtx, ex); stepErr != nil {
			//: verbatim, so errs.HasCode(err, CodeMigrationIrreversible) works.
			return stepErr
		}
		//: the version row is removed by the same transaction that undid it.
		return m.forget(txCtx, ex, migration)
	})
	//: nothing reversed, nothing forgotten.
	if err != nil {
		//: the step's own error travels beside the verdict.
		return failed(MigrationFailed, err, versionField(migration), directionField(directionDown))
	}
	//: reversed and forgotten.
	return nil
}

// versionField renders a migration's version as an error field.
func versionField(migration coresql.MigrationValue) kerrs.FieldValue {
	//: the version is the caller's own number, never a secret.
	return kerrs.String("version", strconv.FormatUint(migration.Version, decimalBase))
}

// directionField renders which way the schema was moving.
func directionField(direction string) kerrs.FieldValue {
	//: "up" or "down" — the two constants above.
	return kerrs.String("direction", direction)
}
