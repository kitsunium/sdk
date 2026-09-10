// Package sql_test — the cancellation suite: what a context that ends does to
// the transaction it was carrying, and what the cleanup statements do about it.
//
// These are separated from the transaction suite because they all assert the
// same one property from four angles: a statement that runs BECAUSE the
// caller's context ended must not travel ON that context.
package sql_test

import (
	"context"
	stdsql "database/sql"
	"database/sql/driver"
	"errors"
	"slices"
	"testing"
	"time"

	coresql "github.com/kitsunium/sdk/internal/core/sql"
	"github.com/kitsunium/sdk/internal/kernel/clock"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	svcsql "github.com/kitsunium/sdk/internal/service/sql"
)

// TestAPerScopeTimeoutDoesNotCondemnTheOuterTransaction is the defect this
// file exists for.
//
// Bounding ONE nested scope with its own deadline is an ordinary thing to do:
// the outer transaction is allowed a minute, one optional step inside it is
// allowed a hundred milliseconds. When that step's context ends, the SDK must
// still be able to roll back to its savepoint — and it can only do that on a
// context that is not the expired one.
//
// Running ROLLBACK TO SAVEPOINT on the dead context fails before it reaches
// the driver ((*sql.Tx).grabConn checks ctx.Done first), the runner reads that
// as "the engine's state is unknown", poisons the transaction, and a perfectly
// healthy outer transaction is destroyed by a timeout that was scoped to one
// step of it.
func TestAPerScopeTimeoutDoesNotCondemnTheOuterTransaction(t *testing.T) {
	t.Parallel()
	f := newFakeDB()
	tm := newTransactor(t, f)
	err := tm.Transact(t.Context(), coresql.TxOptionsValue{}, func(ctx context.Context, ex coresql.Executor) error {
		//: the caller bounds ONE optional step. The outer context is intact.
		inner, cancel := context.WithCancel(ctx)
		nested := tm.Transact(inner, coresql.TxOptionsValue{}, func(context.Context, coresql.Executor) error {
			//: the step gives up because its own budget ended.
			cancel()
			return context.Canceled
		})
		if !errors.Is(nested, context.Canceled) {
			t.Errorf("nested Transact = %v, want the cancellation verbatim", nested)
		}
		//: the savepoint must have been undone on a context that outlived
		//: the one that caused the failure.
		if errs.HasCode(nested, svcsql.CodeSavepointFailed) {
			t.Errorf("nested Transact = %v, want no SAVEPOINT_FAILED — "+
				"the undo must not travel on the dead context", nested)
		}
		//: and the outer transaction must still accept work.
		_, execErr := ex.ExecContext(ctx, "AFTER")
		return execErr
	})
	if err != nil {
		t.Fatalf("outer Transact = %v, want nil — one scope's deadline is not the transaction's", err)
	}
	want := []string{"BEGIN", "SAVEPOINT ktn_sp_1", "ROLLBACK TO SAVEPOINT ktn_sp_1", "AFTER", "COMMIT"}
	if got := f.statements(); !slices.Equal(got, want) {
		t.Fatalf("statements = %v, want %v", got, want)
	}
}

// TestAPerScopeTimeoutStillReleasesASucceedingSavepoint is the same property
// on the success half. A scope whose work COMPLETED and whose context then
// ended must still have its savepoint released, or the outer transaction
// accumulates one savepoint per nested call for the rest of its life.
func TestAPerScopeTimeoutStillReleasesASucceedingSavepoint(t *testing.T) {
	t.Parallel()
	f := newFakeDB()
	tm := newTransactor(t, f)
	err := tm.Transact(t.Context(), coresql.TxOptionsValue{}, func(ctx context.Context, _ coresql.Executor) error {
		inner, cancel := context.WithCancel(ctx)
		//: the work succeeded; the budget expired on the way out.
		return tm.Transact(inner, coresql.TxOptionsValue{}, func(context.Context, coresql.Executor) error {
			cancel()
			return nil
		})
	})
	if err != nil {
		t.Fatalf("Transact = %v, want nil", err)
	}
	if !f.sent("RELEASE SAVEPOINT ktn_sp_1") {
		t.Fatalf("statements = %v, want the RELEASE to have been sent", f.statements())
	}
}

// TestACancelledRootTransactionRollsBackAndReportsTheCancellation pins that
// the ROOT path was already correct, and why: (*sql.Tx).Rollback takes no
// context at all, and database/sql's own awaitDone goroutine has already
// retired the transaction, so the runner's Rollback reports ErrTxDone — which
// is a settled transaction, not a failure to report.
func TestACancelledRootTransactionRollsBackAndReportsTheCancellation(t *testing.T) {
	t.Parallel()
	f := newFakeDB()
	tm := newTransactor(t, f)
	ctx, cancel := context.WithCancel(t.Context())
	err := tm.Transact(ctx, coresql.TxOptionsValue{}, func(context.Context, coresql.Executor) error {
		cancel()
		return context.Canceled
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Transact = %v, want the cancellation verbatim", err)
	}
	//: ErrTxDone is the normal outcome here and must not be dressed up as a
	//: ROLLBACK_FAILED — the transaction really did roll back.
	if errs.HasCode(err, svcsql.CodeRollbackFailed) {
		t.Fatalf("Transact = %v, want no ROLLBACK_FAILED for an already-settled transaction", err)
	}
	if f.sent("COMMIT") {
		t.Fatalf("statements = %v, want no COMMIT", f.statements())
	}
}

// TestAnAlreadyFinishedTransactionIsNotPoisoned separates the two facts the
// poison flag must never confuse. Poison means the engine's state is UNKNOWN.
// A transaction database/sql has already retired is a KNOWN state — everything
// was rolled back — so the undo reporting ErrTxDone is not a poisoning, and
// the caller gets their own error rather than a diagnosis about savepoints.
func TestAnAlreadyFinishedTransactionIsNotPoisoned(t *testing.T) {
	t.Parallel()
	f := newFakeDB()
	tm := newTransactor(t, f)
	var nested error
	//: the OUTER context is the one cancelled, so database/sql retires the
	//: whole Tx underneath the nested scope.
	ctx, cancel := context.WithCancel(t.Context())
	err := tm.Transact(ctx, coresql.TxOptionsValue{}, func(inner context.Context, _ coresql.Executor) error {
		nested = tm.Transact(inner, coresql.TxOptionsValue{}, func(context.Context, coresql.Executor) error {
			//: kill the transaction from under both scopes.
			cancel()
			//: give database/sql's awaitDone goroutine the chance to run.
			<-inner.Done()
			return errWork
		})
		return nested
	})
	if !errors.Is(nested, errWork) {
		t.Fatalf("nested Transact = %v, want the unit of work's error", nested)
	}
	if errs.HasCode(nested, svcsql.CodeTxPoisoned) {
		t.Fatalf("nested Transact = %v, want no TX_POISONED — a retired transaction is a KNOWN state", nested)
	}
	if !errors.Is(err, errWork) {
		t.Fatalf("outer Transact = %v, want the unit of work's error", err)
	}
	//: and a caller routing on database/sql's own verdict is never handed it
	//: in place of their error — ErrTxDone is swallowed by the runner, which
	//: is the whole point of settled().
	if errors.Is(nested, stdsql.ErrTxDone) {
		t.Fatalf("nested Transact = %v, want no ErrTxDone leaking to the caller", nested)
	}
}

// TestAnAbandonedMigrationStillSendsItsUnlock is the same rule on the
// migration lock. A deploy cancelled mid-run — a node drain, a CI job a human
// stopped — is the case the courtesy unlock exists for, and it is exactly the
// case where sending it on the caller's own context would send nothing.
//
// Closing the connection is still the GUARANTEE (the server drops every
// session-scoped lock when the session ends). The explicit unlock is what
// makes the next runner immediate instead of waiting for a TCP close to
// propagate, and it is worth having on the path where it matters most.
func TestAnAbandonedMigrationStillSendsItsUnlock(t *testing.T) {
	t.Parallel()
	f := grantLock(newFakeDB())
	ctx, cancel := context.WithCancel(t.Context())
	//: fail the version-table read so Up abandons right after taking the
	//: lock, with the caller's context already dead.
	f.on(readApplied, func(int) ([]string, [][]driver.Value, error) {
		cancel()
		return nil, nil, errScripted
	})
	runner, db := newMigrator(t, f, clock.NewManualClock(time.Unix(0, 0)),
		coresql.MigrationValue{Version: 1, Name: "one", Up: svcsql.Statements("UP 1"), Down: coresql.Irreversible},
	)
	if err := runner.Up(ctx); err == nil {
		t.Fatal("Up = nil, want the version-table failure")
	}
	if !f.sent(unlock) {
		t.Fatalf("statements = %v, want the unlock sent on a detached context", f.statements())
	}
	//: the dedicated connection is returned to the pool either way.
	if inUse := db.Stats().InUse; inUse != 0 {
		t.Fatalf("%d connections still checked out after an abandoned run", inUse)
	}
}
