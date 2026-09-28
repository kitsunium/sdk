// Package sql_test — the transactor's two ADR 0039 siblings: where a statement
// issued under a context runs (Join), and what waits for a commit (Defer).
package sql_test

import (
	"context"
	"slices"
	"testing"

	coresql "github.com/kitsunium/sdk/internal/core/sql"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	svcsql "github.com/kitsunium/sdk/internal/service/sql"
)

// siblings returns the manager over f as the two capabilities under test.
func siblings(t *testing.T, f *fakeDB) (coresql.Transactor, coresql.Joiner, coresql.Deferrer) {
	t.Helper()
	tm := newTransactor(t, f)
	joiner, canJoin := tm.(coresql.Joiner)
	deferrer, canDefer := tm.(coresql.Deferrer)
	if !canJoin || !canDefer {
		t.Fatalf("the SDK's transactor is Joiner=%v Deferrer=%v, want both", canJoin, canDefer)
	}
	return tm, joiner, deferrer
}

// TestJoinOutsideATransactionIsThePool pins the ordinary read path of a store:
// no transaction on the context, so the statement runs on the pool, outside
// any transaction, and nothing opens one on its behalf.
func TestJoinOutsideATransactionIsThePool(t *testing.T) {
	t.Parallel()
	f := newFakeDB()
	_, joiner, _ := siblings(t, f)
	ex, inTx := joiner.Join(t.Context())
	if inTx {
		t.Fatal("Join reported a transaction on a context that carries none")
	}
	if _, err := ex.ExecContext(t.Context(), "ON THE POOL"); err != nil {
		t.Fatalf("Exec on the pool: %v", err)
	}
	if got, want := f.statements(), []string{"ON THE POOL"}; !slices.Equal(got, want) {
		t.Fatalf("statements = %v, want %v and no BEGIN", got, want)
	}
}

// TestJoinInsideATransactionRunsInIt pins the reason Join exists: a callee
// handed the context runs its statement IN its caller's transaction — between
// the BEGIN and the COMMIT — rather than on a second connection.
func TestJoinInsideATransactionRunsInIt(t *testing.T) {
	t.Parallel()
	f := newFakeDB()
	tm, joiner, _ := siblings(t, f)
	err := tm.Transact(t.Context(), coresql.TxOptionsValue{}, func(ctx context.Context, _ coresql.Executor) error {
		ex, inTx := joiner.Join(ctx)
		if !inTx {
			t.Error("Join did not see the transaction the context carries")
		}
		_, execErr := ex.ExecContext(ctx, "JOINED")
		return execErr
	})
	if err != nil {
		t.Fatalf("Transact: %v", err)
	}
	if got, want := f.statements(), []string{"BEGIN", "JOINED", "COMMIT"}; !slices.Equal(got, want) {
		t.Fatalf("statements = %v, want %v", got, want)
	}
}

// TestJoinAnswersTheInnermostScope pins which executor a nested context gets:
// its own scope's, which stops working when that scope returns while the
// transaction around it goes on.
func TestJoinAnswersTheInnermostScope(t *testing.T) {
	t.Parallel()
	f := newFakeDB()
	tm, joiner, _ := siblings(t, f)
	err := tm.Transact(t.Context(), coresql.TxOptionsValue{}, func(ctx context.Context, _ coresql.Executor) error {
		var inner coresql.Executor
		if nestedErr := tm.Transact(ctx, coresql.TxOptionsValue{}, func(nested context.Context, _ coresql.Executor) error {
			inner, _ = joiner.Join(nested)
			return nil
		}); nestedErr != nil {
			return nestedErr
		}
		if _, execErr := inner.ExecContext(ctx, "STALE"); !errs.HasCode(execErr, svcsql.CodeTxClosed) {
			t.Errorf("the nested scope's executor after it returned = %v, want TX_CLOSED", execErr)
		}
		outer, _ := joiner.Join(ctx)
		_, execErr := outer.ExecContext(ctx, "OUTER")
		return execErr
	})
	if err != nil {
		t.Fatalf("Transact: %v", err)
	}
	if f.sent("STALE") || !f.sent("OUTER") {
		t.Fatalf("statements = %v, want OUTER and never STALE", f.statements())
	}
}

// TestALeakedContextNeverFallsBackToThePool pins the refusal that keeps a
// statement inside the transaction it was written for: once the transaction
// is over, its context still names it, and Join answers the retired executor
// rather than the pool.
func TestALeakedContextNeverFallsBackToThePool(t *testing.T) {
	t.Parallel()
	f := newFakeDB()
	tm, joiner, _ := siblings(t, f)
	var leaked context.Context
	if err := tm.Transact(t.Context(), coresql.TxOptionsValue{}, func(ctx context.Context, _ coresql.Executor) error {
		leaked = ctx
		return nil
	}); err != nil {
		t.Fatalf("Transact: %v", err)
	}
	ex, inTx := joiner.Join(leaked)
	if !inTx {
		t.Fatal("Join forgot the transaction a leaked context names")
	}
	if _, err := ex.ExecContext(t.Context(), "AFTER THE END"); !errs.HasCode(err, svcsql.CodeTxClosed) {
		t.Fatalf("a statement through a finished transaction = %v, want TX_CLOSED", err)
	}
	if f.sent("AFTER THE END") {
		t.Fatal("the statement reached the driver")
	}
}

// TestJoinIgnoresAnotherTransactorsTransaction is the two-database case: a
// transaction on A says nothing about where a statement on B runs.
func TestJoinIgnoresAnotherTransactorsTransaction(t *testing.T) {
	t.Parallel()
	first, second := newFakeDB(), newFakeDB()
	alpha := newTransactor(t, first)
	_, beta, _ := siblings(t, second)
	err := alpha.Transact(t.Context(), coresql.TxOptionsValue{}, func(ctx context.Context, _ coresql.Executor) error {
		ex, inTx := beta.Join(ctx)
		if inTx {
			t.Error("B's Join answered A's transaction")
		}
		_, execErr := ex.ExecContext(ctx, "ON B")
		return execErr
	})
	if err != nil {
		t.Fatalf("Transact: %v", err)
	}
	if got, want := second.statements(), []string{"ON B"}; !slices.Equal(got, want) {
		t.Fatalf("database B statements = %v, want %v", got, want)
	}
}

// TestHeldFunctionsRunAfterTheCommitInOrder is the ordinary path of Defer:
// nothing runs while the transaction is open, everything runs once the COMMIT
// stood, in the order it was held.
func TestHeldFunctionsRunAfterTheCommitInOrder(t *testing.T) {
	t.Parallel()
	f := newFakeDB()
	tm, _, deferrer := siblings(t, f)
	var ran []string
	var sawCommit []bool
	hold := func(ctx context.Context, label string) {
		if !deferrer.Defer(ctx, func() {
			ran = append(ran, label)
			sawCommit = append(sawCommit, slices.Contains(f.statements(), "COMMIT"))
		}) {
			t.Errorf("Defer(%s) did not hold inside a transaction", label)
		}
	}
	err := tm.Transact(t.Context(), coresql.TxOptionsValue{}, func(ctx context.Context, _ coresql.Executor) error {
		hold(ctx, "first")
		hold(ctx, "second")
		if len(ran) != 0 {
			t.Errorf("held functions ran before the commit: %v", ran)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("Transact: %v", err)
	}
	if !slices.Equal(ran, []string{"first", "second"}) {
		t.Fatalf("held functions ran as %v, want first then second", ran)
	}
	if !slices.Equal(sawCommit, []bool{true, true}) {
		t.Fatalf("held functions saw the COMMIT = %v, want both after it", sawCommit)
	}
}

// TestNothingHeldRunsForARollback covers the three ways a root transaction
// ends without a commit: the unit of work fails, the COMMIT is refused, and a
// panic unwinds it.
func TestNothingHeldRunsForARollback(t *testing.T) {
	t.Parallel()
	for name, end := range map[string]struct {
		work      func() error
		commitErr error
		panics    bool
	}{
		"the unit of work fails": {work: func() error { return errWork }},
		"the commit is refused":  {work: func() error { return nil }, commitErr: errScripted},
		"a panic":                {work: func() error { panic("boom") }, panics: true},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f := newFakeDB()
			f.commitErr = end.commitErr
			tm, _, deferrer := siblings(t, f)
			ran := false
			defer func() {
				recovered := recover()
				if (recovered != nil) != end.panics {
					t.Errorf("recovered %v, want a panic: %v", recovered, end.panics)
				}
				if ran {
					t.Error("a held function ran for a transaction that did not commit")
				}
			}()
			verdictIgnored(t, tm.Transact(t.Context(), coresql.TxOptionsValue{}, func(ctx context.Context, _ coresql.Executor) error {
				deferrer.Defer(ctx, func() { ran = true })
				return end.work()
			}))
		})
	}
}

// TestASavepointsRollbackDropsWhatItHeld pins the savepoint rule: a caught
// nested failure drops what its scope held and nothing else, and the outer
// transaction commits the rest.
func TestASavepointsRollbackDropsWhatItHeld(t *testing.T) {
	t.Parallel()
	f := newFakeDB()
	tm, _, deferrer := siblings(t, f)
	var ran []string
	err := tm.Transact(t.Context(), coresql.TxOptionsValue{}, func(ctx context.Context, _ coresql.Executor) error {
		deferrer.Defer(ctx, func() { ran = append(ran, "outer") })
		caught := tm.Transact(ctx, coresql.TxOptionsValue{}, func(nested context.Context, _ coresql.Executor) error {
			deferrer.Defer(nested, func() { ran = append(ran, "failed savepoint") })
			return tm.Transact(nested, coresql.TxOptionsValue{}, func(deeper context.Context, _ coresql.Executor) error {
				deferrer.Defer(deeper, func() { ran = append(ran, "released inside the failed one") })
				return errWork
			})
		})
		if caught == nil {
			t.Error("the nested failure did not reach its caller")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("Transact: %v", err)
	}
	if !slices.Equal(ran, []string{"outer"}) {
		t.Fatalf("ran %v, want only the outer scope's function", ran)
	}
}

// TestAReleasedSavepointHandsItsHoldsToTheScopeAroundIt pins the other half:
// a savepoint that succeeds keeps what it held, and a later rollback of the
// scope around it drops it with everything else.
func TestAReleasedSavepointHandsItsHoldsToTheScopeAroundIt(t *testing.T) {
	t.Parallel()
	f := newFakeDB()
	tm, _, deferrer := siblings(t, f)
	var ran []string
	err := tm.Transact(t.Context(), coresql.TxOptionsValue{}, func(ctx context.Context, _ coresql.Executor) error {
		if nestedErr := tm.Transact(ctx, coresql.TxOptionsValue{}, func(nested context.Context, _ coresql.Executor) error {
			deferrer.Defer(nested, func() { ran = append(ran, "kept") })
			return nil
		}); nestedErr != nil {
			return nestedErr
		}
		dropped := tm.Transact(ctx, coresql.TxOptionsValue{}, func(outerSavepoint context.Context, _ coresql.Executor) error {
			if innerErr := tm.Transact(outerSavepoint, coresql.TxOptionsValue{}, func(inner context.Context, _ coresql.Executor) error {
				deferrer.Defer(inner, func() { ran = append(ran, "released, then undone around it") })
				return nil
			}); innerErr != nil {
				return innerErr
			}
			return errWork
		})
		if dropped == nil {
			t.Error("the failing savepoint did not report its failure")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("Transact: %v", err)
	}
	if !slices.Equal(ran, []string{"kept"}) {
		t.Fatalf("ran %v, want only what the released savepoint held", ran)
	}
}

// TestDeferHoldsNothingWithoutAnOpenTransaction pins the false answers: no
// transaction on the context, another transactor's transaction, and a
// transaction that is already over. In each, fn stays the caller's and the
// transactor never runs it.
func TestDeferHoldsNothingWithoutAnOpenTransaction(t *testing.T) {
	t.Parallel()
	f := newFakeDB()
	tm, _, deferrer := siblings(t, f)
	ran := false
	if deferrer.Defer(t.Context(), func() { ran = true }) {
		t.Error("Defer held a function on a context with no transaction")
	}
	other := newTransactor(t, newFakeDB())
	verdictIgnored(t, other.Transact(t.Context(), coresql.TxOptionsValue{}, func(ctx context.Context, _ coresql.Executor) error {
		if deferrer.Defer(ctx, func() { ran = true }) {
			t.Error("Defer held a function on another transactor's transaction")
		}
		return nil
	}))
	var leaked context.Context
	verdictIgnored(t, tm.Transact(t.Context(), coresql.TxOptionsValue{}, func(ctx context.Context, _ coresql.Executor) error {
		leaked = ctx
		return nil
	}))
	if deferrer.Defer(leaked, func() { ran = true }) {
		t.Error("Defer held a function on a transaction that already committed")
	}
	if ran {
		t.Fatal("the transactor ran a function it did not hold")
	}
}

// TestANilHeldFunctionOnlyReportsTheTransaction pins that a nil fn is an
// answer about the context, not a panic at the commit.
func TestANilHeldFunctionOnlyReportsTheTransaction(t *testing.T) {
	t.Parallel()
	tm, _, deferrer := siblings(t, newFakeDB())
	err := tm.Transact(t.Context(), coresql.TxOptionsValue{}, func(ctx context.Context, _ coresql.Executor) error {
		if !deferrer.Defer(ctx, nil) {
			t.Error("Defer(nil) inside a transaction answered false")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("Transact: %v", err)
	}
}

// TestAPanickingHeldFunctionReachesTheCallerAfterTheCommit pins what a held
// function's panic does: the commit already stood, the panic reaches the
// caller of the outermost Transact, and the functions held after it do not
// run.
func TestAPanickingHeldFunctionReachesTheCallerAfterTheCommit(t *testing.T) {
	t.Parallel()
	f := newFakeDB()
	tm, _, deferrer := siblings(t, f)
	laterRan := false
	defer func() {
		if recovered := recover(); recovered != "held" {
			t.Fatalf("recovered %v, want the held function's own panic", recovered)
		}
		if !f.sent("COMMIT") {
			t.Fatal("the panic came before the commit")
		}
		if laterRan {
			t.Fatal("a function held after the panicking one ran")
		}
	}()
	verdictIgnored(t, tm.Transact(t.Context(), coresql.TxOptionsValue{}, func(ctx context.Context, _ coresql.Executor) error {
		deferrer.Defer(ctx, func() { panic("held") })
		deferrer.Defer(ctx, func() { laterRan = true })
		return nil
	}))
	t.Fatal("Transact returned instead of re-raising the held function's panic")
}
