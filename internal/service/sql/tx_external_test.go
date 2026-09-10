// Package sql_test — the transaction suite: who commits, what nesting sends
// on the wire, and what a failed sub-transaction does to the one around it.
package sql_test

import (
	"context"
	stdsql "database/sql"
	"errors"
	"slices"
	"testing"

	coresql "github.com/kitsunium/sdk/internal/core/sql"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	svcsql "github.com/kitsunium/sdk/internal/service/sql"
)

// errWork is the failure a test's unit of work returns.
var errWork = errors.New("unit of work failed")

// newTransactor wires a manager over a scripted server.
func newTransactor(t *testing.T, f *fakeDB) coresql.Transactor {
	t.Helper()
	db := closeOnCleanup(t, f.open())
	tm, err := svcsql.NewTransactor(svcsql.Config{
		DB: db, Dialect: coresql.DialectPostgres,
		Pool: svcsql.PoolConfig{MaxOpen: 4},
	})
	if err != nil {
		t.Fatalf("NewTransactor: %v", err)
	}
	return tm
}

// TestTransactCommitsOnNil is the ordinary path, and it pins that the commit
// is the MANAGER's — the unit of work never sends one.
func TestTransactCommitsOnNil(t *testing.T) {
	t.Parallel()
	f := newFakeDB()
	tm := newTransactor(t, f)
	err := tm.Transact(t.Context(), coresql.TxOptionsValue{}, func(ctx context.Context, ex coresql.Executor) error {
		_, execErr := ex.ExecContext(ctx, "UPDATE t SET c = 1")
		return execErr
	})
	if err != nil {
		t.Fatalf("Transact: %v", err)
	}
	want := []string{"BEGIN", "UPDATE t SET c = 1", "COMMIT"}
	if got := f.statements(); !slices.Equal(got, want) {
		t.Fatalf("statements = %v, want %v", got, want)
	}
}

// TestTransactRollsBackOnErrorAndReturnsItVerbatim pins the promise that a
// caller's own errors.Is still answers across the boundary — the reason the
// unit of work's error is never re-labelled by errs.Wrap (origin-wins would
// hand it the SDK's code).
func TestTransactRollsBackOnErrorAndReturnsItVerbatim(t *testing.T) {
	t.Parallel()
	f := newFakeDB()
	tm := newTransactor(t, f)
	err := tm.Transact(t.Context(), coresql.TxOptionsValue{}, func(context.Context, coresql.Executor) error {
		return errWork
	})
	if !errors.Is(err, errWork) {
		t.Fatalf("Transact = %v, want the caller's own error", err)
	}
	if !f.sent("ROLLBACK") || f.sent("COMMIT") {
		t.Fatalf("statements = %v, want a ROLLBACK and no COMMIT", f.statements())
	}
}

// TestCommitFailureSaysTheWorkHadNoEffect pins the one fact a caller must not
// have to infer, and the side-by-side join that keeps the driver's error
// reachable.
func TestCommitFailureSaysTheWorkHadNoEffect(t *testing.T) {
	t.Parallel()
	f := newFakeDB()
	f.commitErr = errScripted
	tm := newTransactor(t, f)
	err := tm.Transact(t.Context(), coresql.TxOptionsValue{}, func(context.Context, coresql.Executor) error {
		return nil
	})
	if !errs.HasCode(err, svcsql.CodeCommitFailed) {
		t.Fatalf("Transact = %v, want COMMIT_FAILED", err)
	}
	if !errors.Is(err, errScripted) {
		t.Fatal("the driver's own error did not survive the join")
	}
}

// TestBeginFailureRunsNothing pins that a transaction the driver refuses
// leaves no trace of the unit of work.
func TestBeginFailureRunsNothing(t *testing.T) {
	t.Parallel()
	f := newFakeDB()
	f.beginErr = errScripted
	tm := newTransactor(t, f)
	ran := false
	err := tm.Transact(t.Context(), coresql.TxOptionsValue{}, func(context.Context, coresql.Executor) error {
		ran = true
		return nil
	})
	if !errs.HasCode(err, svcsql.CodeBeginFailed) {
		t.Fatalf("Transact = %v, want BEGIN_FAILED", err)
	}
	if ran {
		t.Fatal("the unit of work ran without a transaction")
	}
}

// TestNestedTransactIssuesASavepointAndReleasesIt pins the exact wire
// sequence of a nested scope. The statement ORDER is the entire contract of a
// savepoint implementation, so it is asserted as a sequence and not as a set.
func TestNestedTransactIssuesASavepointAndReleasesIt(t *testing.T) {
	t.Parallel()
	f := newFakeDB()
	tm := newTransactor(t, f)
	err := tm.Transact(t.Context(), coresql.TxOptionsValue{}, func(ctx context.Context, ex coresql.Executor) error {
		if _, execErr := ex.ExecContext(ctx, "OUTER"); execErr != nil {
			return execErr
		}
		return tm.Transact(ctx, coresql.TxOptionsValue{}, func(inner context.Context, innerEx coresql.Executor) error {
			_, execErr := innerEx.ExecContext(inner, "INNER")
			return execErr
		})
	})
	if err != nil {
		t.Fatalf("Transact: %v", err)
	}
	want := []string{
		"BEGIN", "OUTER", "SAVEPOINT ktn_sp_1", "INNER",
		"RELEASE SAVEPOINT ktn_sp_1", "COMMIT",
	}
	if got := f.statements(); !slices.Equal(got, want) {
		t.Fatalf("statements = %v, want %v", got, want)
	}
}

// TestOnlyOneBeginIsEverSent is the negative half of the previous test:
// nesting must not open a SECOND transaction, which on a real pool would
// deadlock against the first.
func TestOnlyOneBeginIsEverSent(t *testing.T) {
	t.Parallel()
	f := newFakeDB()
	tm := newTransactor(t, f)
	verdictIgnored(t, tm.Transact(t.Context(), coresql.TxOptionsValue{}, func(ctx context.Context, _ coresql.Executor) error {
		return tm.Transact(ctx, coresql.TxOptionsValue{}, func(deeper context.Context, _ coresql.Executor) error {
			return tm.Transact(deeper, coresql.TxOptionsValue{}, func(context.Context, coresql.Executor) error {
				return nil
			})
		})
	}))
	begins := 0
	for _, stmt := range f.statements() {
		if stmt == "BEGIN" {
			begins++
		}
	}
	if begins != 1 {
		t.Fatalf("sent %d BEGINs for three nested scopes, want 1: %v", begins, f.statements())
	}
}

// TestACaughtNestedFailureLeavesTheOuterTransactionUsable is the answer to
// the third savepoint question, made executable: the inner scope's work is
// undone, the outer transaction keeps going, and it commits.
func TestACaughtNestedFailureLeavesTheOuterTransactionUsable(t *testing.T) {
	t.Parallel()
	f := newFakeDB()
	tm := newTransactor(t, f)
	err := tm.Transact(t.Context(), coresql.TxOptionsValue{}, func(ctx context.Context, ex coresql.Executor) error {
		//: the optional step fails and is CAUGHT — the whole point of a
		//: savepoint is that this is a supported thing to do.
		inner := tm.Transact(ctx, coresql.TxOptionsValue{}, func(context.Context, coresql.Executor) error {
			return errWork
		})
		if !errors.Is(inner, errWork) {
			t.Errorf("nested Transact = %v, want the inner error verbatim", inner)
		}
		//: the transaction must still accept work after a caught failure.
		_, execErr := ex.ExecContext(ctx, "AFTER")
		return execErr
	})
	if err != nil {
		t.Fatalf("outer Transact = %v, want nil — a caught inner failure must not condemn it", err)
	}
	want := []string{
		"BEGIN", "SAVEPOINT ktn_sp_1", "ROLLBACK TO SAVEPOINT ktn_sp_1", "AFTER", "COMMIT",
	}
	if got := f.statements(); !slices.Equal(got, want) {
		t.Fatalf("statements = %v, want %v", got, want)
	}
}

// TestAnUncaughtNestedFailureRollsBackEverything is the default: propagate
// the inner error and the outer scope rolls back in turn.
func TestAnUncaughtNestedFailureRollsBackEverything(t *testing.T) {
	t.Parallel()
	f := newFakeDB()
	tm := newTransactor(t, f)
	err := tm.Transact(t.Context(), coresql.TxOptionsValue{}, func(ctx context.Context, _ coresql.Executor) error {
		return tm.Transact(ctx, coresql.TxOptionsValue{}, func(context.Context, coresql.Executor) error {
			return errWork
		})
	})
	if !errors.Is(err, errWork) {
		t.Fatalf("Transact = %v, want the inner error", err)
	}
	if f.sent("COMMIT") || !f.sent("ROLLBACK") {
		t.Fatalf("statements = %v, want a ROLLBACK and no COMMIT", f.statements())
	}
}

// TestAFailedSavepointRollbackPoisonsTheTransaction is the case the whole
// poison mechanism exists for. When the recovery statement itself fails, the
// SDK no longer knows what the engine kept — so nothing may be committed.
func TestAFailedSavepointRollbackPoisonsTheTransaction(t *testing.T) {
	t.Parallel()
	f := newFakeDB()
	f.failOn("ROLLBACK TO SAVEPOINT ktn_sp_1")
	tm := newTransactor(t, f)
	var afterPoison error
	err := tm.Transact(t.Context(), coresql.TxOptionsValue{}, func(ctx context.Context, ex coresql.Executor) error {
		//: the caller CATCHES the inner failure, exactly as the usable case
		//: above does — and this time catching it is not enough.
		verdictIgnored(t, tm.Transact(ctx, coresql.TxOptionsValue{}, func(context.Context, coresql.Executor) error {
			return errWork
		}))
		_, afterPoison = ex.ExecContext(ctx, "AFTER")
		return nil
	})
	if !errs.HasCode(afterPoison, svcsql.CodeTxPoisoned) {
		t.Fatalf("post-poison Exec = %v, want TX_POISONED", afterPoison)
	}
	if !errs.HasCode(err, svcsql.CodeTxPoisoned) {
		t.Fatalf("Transact = %v, want TX_POISONED", err)
	}
	if f.sent("COMMIT") {
		t.Fatalf("a poisoned transaction was committed: %v", f.statements())
	}
	if f.sent("AFTER") {
		t.Fatal("a statement reached the driver after the transaction was poisoned")
	}
}

// TestAFailedReleasePoisonsTooCoversTheForgottenHalf: a RELEASE that fails
// leaves a savepoint the SDK believes is gone, so the transaction's state no
// longer matches its bookkeeping. It is the same verdict as a failed rollback
// and it is easy to forget, which is why it has its own test.
func TestAFailedReleasePoisonsTooCoversTheForgottenHalf(t *testing.T) {
	t.Parallel()
	f := newFakeDB()
	f.failOn("RELEASE SAVEPOINT ktn_sp_1")
	tm := newTransactor(t, f)
	err := tm.Transact(t.Context(), coresql.TxOptionsValue{}, func(ctx context.Context, _ coresql.Executor) error {
		return tm.Transact(ctx, coresql.TxOptionsValue{}, func(context.Context, coresql.Executor) error {
			return nil
		})
	})
	if !errs.HasCode(err, svcsql.CodeSavepointFailed) {
		t.Fatalf("Transact = %v, want SAVEPOINT_FAILED", err)
	}
	if f.sent("COMMIT") {
		t.Fatalf("a transaction with an unreleasable savepoint was committed: %v", f.statements())
	}
}

// TestAFailedSavepointCreationLeavesTheOuterScopeUntouched pins the
// asymmetry: a scope that never started cannot have condemned anything.
func TestAFailedSavepointCreationLeavesTheOuterScopeUntouched(t *testing.T) {
	t.Parallel()
	f := newFakeDB()
	f.failOn("SAVEPOINT ktn_sp_1")
	tm := newTransactor(t, f)
	err := tm.Transact(t.Context(), coresql.TxOptionsValue{}, func(ctx context.Context, ex coresql.Executor) error {
		inner := tm.Transact(ctx, coresql.TxOptionsValue{}, func(context.Context, coresql.Executor) error {
			t.Error("the unit of work ran although its savepoint was refused")
			return nil
		})
		if !errs.HasCode(inner, svcsql.CodeSavepointFailed) {
			t.Errorf("nested Transact = %v, want SAVEPOINT_FAILED", inner)
		}
		_, execErr := ex.ExecContext(ctx, "AFTER")
		return execErr
	})
	if err != nil {
		t.Fatalf("outer Transact = %v, want nil", err)
	}
	if !f.sent("COMMIT") {
		t.Fatalf("statements = %v, want a COMMIT", f.statements())
	}
}

// TestSavepointNamesAreNeverReusedWithinOneTransaction pins the counter that
// never resets. Reuse is legal in every supported dialect and it is a trap: a
// repeated name shadows rather than replaces, so RELEASE frees the wrong one.
func TestSavepointNamesAreNeverReusedWithinOneTransaction(t *testing.T) {
	t.Parallel()
	f := newFakeDB()
	tm := newTransactor(t, f)
	err := tm.Transact(t.Context(), coresql.TxOptionsValue{}, func(ctx context.Context, _ coresql.Executor) error {
		for range 3 {
			//: three SEQUENTIAL scopes: each is released before the next
			//: opens, which is precisely when a naive implementation reuses
			//: the name.
			if inner := tm.Transact(ctx, coresql.TxOptionsValue{}, func(context.Context, coresql.Executor) error {
				return nil
			}); inner != nil {
				return inner
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("Transact: %v", err)
	}
	for _, name := range []string{"ktn_sp_1", "ktn_sp_2", "ktn_sp_3"} {
		if !f.sent("SAVEPOINT " + name) {
			t.Fatalf("statements = %v, want a SAVEPOINT %s", f.statements(), name)
		}
	}
}

// TestNestedTransactRefusesNonZeroOptions pins the refusal that keeps a
// caller from being handed a weaker transaction than they asked for.
func TestNestedTransactRefusesNonZeroOptions(t *testing.T) {
	t.Parallel()
	f := newFakeDB()
	tm := newTransactor(t, f)
	verdictIgnored(t, tm.Transact(t.Context(), coresql.TxOptionsValue{}, func(ctx context.Context, _ coresql.Executor) error {
		for _, opts := range []coresql.TxOptionsValue{
			{ReadOnly: true}, {Isolation: stdsql.LevelSerializable},
		} {
			inner := tm.Transact(ctx, opts, func(context.Context, coresql.Executor) error {
				t.Error("a nested scope ran with options a savepoint cannot honour")
				return nil
			})
			if !errs.HasCode(inner, coresql.CodeNestedIsolation) {
				t.Errorf("nested Transact(%v) = %v, want NESTED_ISOLATION", opts, inner)
			}
		}
		return nil
	}))
}

// TestTwoManagersDoNotNestInEachOther is the two-database case. A Transact on
// B inside a Transact on A must open a REAL transaction on B, because A's
// savepoint means nothing there.
func TestTwoManagersDoNotNestInEachOther(t *testing.T) {
	t.Parallel()
	first, second := newFakeDB(), newFakeDB()
	alpha, beta := newTransactor(t, first), newTransactor(t, second)
	err := alpha.Transact(t.Context(), coresql.TxOptionsValue{}, func(ctx context.Context, _ coresql.Executor) error {
		return beta.Transact(ctx, coresql.TxOptionsValue{}, func(inner context.Context, ex coresql.Executor) error {
			_, execErr := ex.ExecContext(inner, "ON B")
			return execErr
		})
	})
	if err != nil {
		t.Fatalf("Transact: %v", err)
	}
	if !second.sent("BEGIN") || !second.sent("ON B") {
		t.Fatalf("database B statements = %v, want its own transaction", second.statements())
	}
	for _, stmt := range first.statements() {
		if stmt == "ON B" {
			t.Fatal("database B's work reached database A")
		}
	}
	if second.sent("SAVEPOINT ktn_sp_1") {
		t.Fatal("database B opened a savepoint of database A's transaction")
	}
}

// TestARetiredExecutorRefusesAndReachesNoDriver covers the leak that
// database/sql cannot catch: an inner scope's handle kept alive while the
// OUTER transaction is still open would otherwise execute happily.
func TestARetiredExecutorRefusesAndReachesNoDriver(t *testing.T) {
	t.Parallel()
	f := newFakeDB()
	tm := newTransactor(t, f)
	err := tm.Transact(t.Context(), coresql.TxOptionsValue{}, func(ctx context.Context, _ coresql.Executor) error {
		var leaked coresql.Executor
		if inner := tm.Transact(ctx, coresql.TxOptionsValue{}, func(_ context.Context, ex coresql.Executor) error {
			leaked = ex
			return nil
		}); inner != nil {
			return inner
		}
		//: the inner scope has returned; its handle must be inert even though
		//: the transaction underneath is very much alive.
		_, execErr := leaked.ExecContext(ctx, "LEAKED")
		if !errs.HasCode(execErr, svcsql.CodeTxClosed) {
			t.Errorf("stale Exec = %v, want TX_CLOSED", execErr)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("Transact: %v", err)
	}
	if f.sent("LEAKED") {
		t.Fatal("a statement from a retired executor reached the driver")
	}
}

// TestQueryRowOnARetiredExecutorReachesNoDriver pins the named stdlib
// limitation: *sql.Row cannot carry a typed error, so the guarantee is that
// NOTHING EXECUTES and Scan reports a failure.
func TestQueryRowOnARetiredExecutorReachesNoDriver(t *testing.T) {
	t.Parallel()
	f := newFakeDB()
	tm := newTransactor(t, f)
	verdictIgnored(t, tm.Transact(t.Context(), coresql.TxOptionsValue{}, func(ctx context.Context, _ coresql.Executor) error {
		var leaked coresql.Executor
		verdictIgnored(t, tm.Transact(ctx, coresql.TxOptionsValue{}, func(_ context.Context, ex coresql.Executor) error {
			leaked = ex
			return nil
		}))
		var scanned int
		if err := leaked.QueryRowContext(ctx, "LEAKED ROW").Scan(&scanned); err == nil {
			t.Error("Scan on a retired executor returned nil")
		}
		return nil
	}))
	if f.sent("LEAKED ROW") {
		t.Fatal("a QueryRow from a retired executor reached the driver")
	}
}

// TestAPanicRollsBackAndKeepsItsOwnStack pins the deliberate non-recovery: a
// panic is not a database outcome, so the transaction is undone and the panic
// continues to the caller who can actually diagnose it.
func TestAPanicRollsBackAndKeepsItsOwnStack(t *testing.T) {
	t.Parallel()
	f := newFakeDB()
	tm := newTransactor(t, f)
	func() {
		defer func() {
			if recovered := recover(); recovered != "boom" {
				t.Errorf("recovered %v, want the original panic value", recovered)
			}
		}()
		verdictIgnored(t, tm.Transact(t.Context(), coresql.TxOptionsValue{}, func(context.Context, coresql.Executor) error {
			panic("boom")
		}))
	}()
	if !f.sent("ROLLBACK") || f.sent("COMMIT") {
		t.Fatalf("statements = %v, want a ROLLBACK and no COMMIT", f.statements())
	}
}

// TestAPanicInANestedScopeUndoesOnlyItsSavepoint pins the same rule one level
// down: the outer transaction must not be left carrying half an abandoned
// unit of work.
func TestAPanicInANestedScopeUndoesOnlyItsSavepoint(t *testing.T) {
	t.Parallel()
	f := newFakeDB()
	tm := newTransactor(t, f)
	func() {
		//: the panic is expected; swallowing it here is what lets the test
		//: assert what the runner did on its way out.
		//: the panic is expected; swallowing it here is what lets the test
		//: assert what the runner did on its way out.
		defer func() { recover() }() //nolint:staticcheck // the value is the panic, not an error
		verdictIgnored(t, tm.Transact(t.Context(), coresql.TxOptionsValue{}, func(ctx context.Context, _ coresql.Executor) error {
			return tm.Transact(ctx, coresql.TxOptionsValue{}, func(context.Context, coresql.Executor) error {
				panic("boom")
			})
		}))
	}()
	if !f.sent("ROLLBACK TO SAVEPOINT ktn_sp_1") {
		t.Fatalf("statements = %v, want the savepoint undone", f.statements())
	}
}

// TestANilUnitOfWorkIsRefusedBeforeAnythingOpens pins that a wiring fault
// never costs a connection.
func TestANilUnitOfWorkIsRefusedBeforeAnythingOpens(t *testing.T) {
	t.Parallel()
	f := newFakeDB()
	tm := newTransactor(t, f)
	if err := tm.Transact(t.Context(), coresql.TxOptionsValue{}, nil); !errs.HasCode(err, svcsql.CodeConfigInvalid) {
		t.Fatalf("Transact(nil) = %v, want CONFIG_INVALID", err)
	}
	if len(f.statements()) != 0 {
		t.Fatalf("statements = %v, want none", f.statements())
	}
}
