// Package sql — hosts the migration lock of an engine with no advisory lock:
// SQLite's own write lock on the database file, held for the whole run.
package sql

import (
	"context"
	"errors"
	"strings"

	coresql "github.com/kitsunium/sdk/internal/core/sql"
	kerrs "github.com/kitsunium/sdk/internal/kernel/errs"
)

// commitPhase labels a SQLite run whose final COMMIT failed: every migration
// the run applied is undone with it, so the database is where it was.
const commitPhase string = "commit"

// busyMarkers are the words SQLite itself gives SQLITE_BUSY — "database is
// locked" is sqlite3_errstr's text for the code, and drivers that name the
// code add "SQLITE_BUSY". A driver error carrying neither is read as a failure
// rather than as a lock held elsewhere: misreading the other way would retry
// a broken database until the budget ran out, and misreading this way stops
// the run — it never runs one unlocked.
var busyMarkers = []string{"database is locked", "SQLITE_BUSY"}

// underFileLock runs work inside ONE transaction that holds SQLite's write
// lock from its first statement to its commit.
//
// # Why the file's lock
//
// ADR 0055 §D7 chose the engine's advisory lock for one property: the server
// releases it when its holder dies. SQLite has no advisory lock, but its
// write lock has the same property one layer down — it is a lock on the
// database file, and the operating system releases a dead process's file
// locks. So the run holds it (ADR 0140): the transaction takes it with its
// first statement, every migration runs in a savepoint of it, and the commit
// releases it.
//
// # What changes against the advisory lock
//
// Each migration is still atomic on its own: a failing one rolls back to its
// savepoint, the ones before it are committed, and the run stops. A process
// that dies mid-run, though, loses the whole run rather than its last
// migration, because the run is one transaction. That leaves the version
// table matching the schema either way. And a step cannot run what SQLite
// refuses inside a transaction: VACUUM, or a PRAGMA such as journal_mode or
// foreign_keys.
func (m *migrator) underFileLock(ctx context.Context, work func(context.Context) error) error {
	//: the budget is an absolute instant on the injected clock, as the
	//: advisory loop's is.
	deadline := m.cfg.clk.Now().Add(m.plan.lockBudget)
	//: retry until the lock is taken or the budget is spent.
	for {
		busy, err := m.fileLockedRun(ctx, work)
		//: the run happened — or failed for a reason other than the lock.
		if !busy {
			//: the work's verdict, and the commit's beside it.
			return err
		}
		//: another connection holds the write lock; wait, or give up.
		if pauseErr := m.pause(ctx, deadline); pauseErr != nil {
			//: propagate MIGRATION_LOCK_TIMEOUT / the cancellation verdict.
			return pauseErr
		}
	}
}

// fileLockedRun makes one attempt: it opens the run's transaction, takes the
// write lock, and runs work. busy reports that another connection held the
// lock, so nothing ran and the transaction was rolled back; otherwise err is
// the work's verdict, with the transaction's beside it.
func (m *migrator) fileLockedRun(ctx context.Context, work func(context.Context) error) (busy bool, err error) {
	var outcome error
	began := false
	err = m.tx.Transact(ctx, coresql.TxOptionsValue{}, func(txCtx context.Context, ex coresql.Executor) error {
		began = true
		//: nothing runs before the lock is held.
		if lockErr := m.takeFileLock(txCtx, ex); lockErr != nil {
			busy = lockBusy(lockErr)
			//: rolled back either way; a real failure also names its phase.
			return failed(MigrationFailed, lockErr, kerrs.String("phase", lockPhase))
		}
		//: every migration runs in its own savepoint of this transaction.
		outcome = work(txCtx)
		//: COMMIT what applied, even when a later migration failed: the one
		//: that failed has already rolled back to its own savepoint.
		return nil
	})
	//: the transaction never began, busy: SQLite answered the lock at BEGIN
	//: itself — a connection opened with _txlock=immediate takes it there, and
	//: one that converts a fresh file to WAL takes it while it opens. The same
	//: lock, held elsewhere, answered one statement earlier.
	if !began && err != nil && lockBusy(err) {
		busy = true
	}
	//: a busy attempt is retried, and its rollback verdict with it.
	if busy {
		//: nothing ran.
		return true, nil
	}
	//: a run whose COMMIT failed lost every migration it applied.
	if err != nil && outcome == nil && !kerrs.HasCode(err, CodeMigrationFailed) {
		//: say which phase took the run with it.
		err = failed(MigrationFailed, err, kerrs.String("phase", commitPhase))
	}
	//: the work's verdict, and the transaction's beside it.
	return false, errors.Join(outcome, err)
}

// takeFileLock makes the run's transaction the database's one writer.
//
// The version table comes first because it is the table the lock statement
// names, and because creating it is itself a write the lock must cover; if it
// already exists that statement writes nothing, and the lock statement takes
// the lock.
func (m *migrator) takeFileLock(ctx context.Context, ex coresql.Executor) error {
	//: inside the transaction, so a lock that turns out to be busy undoes it.
	if _, err := ex.ExecContext(ctx, createTableSQL(m.plan.table)); err != nil {
		//: busy, or a real failure — the caller tells them apart.
		return err
	}
	//: the write that writes nothing, and takes the lock.
	_, err := ex.ExecContext(ctx, fileLockSQL(m.plan.table))
	//: nil once this transaction is the one writer.
	return err
}

// lockBusy reports whether err is SQLite answering that another connection
// holds the write lock. See busyMarkers for why a miss is read as a failure.
func lockBusy(err error) bool {
	//: a cancellation is the caller's decision, never a busy lock.
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		//: not a lock held elsewhere.
		return false
	}
	text := err.Error()
	//: SQLite's own words for SQLITE_BUSY, in either spelling.
	for _, marker := range busyMarkers {
		//: somebody else is writing.
		if strings.Contains(text, marker) {
			//: wait and retry.
			return true
		}
	}
	//: any other refusal is a failure.
	return false
}
