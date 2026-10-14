// Package sql_test — the transactor's third ADR 0039 sibling: what waits for
// the work of a scope to be undone (OnAbort, ADR 0139 as amended on
// 2026-10-14), the complement of Defer.
package sql_test

import (
	"context"
	"slices"
	"testing"

	coresql "github.com/kitsunium/sdk/internal/core/data/sql"
)

// aborter returns the manager over f and its Aborter and Deferrer.
func aborter(t *testing.T, f *fakeDB) (coresql.Transactor, coresql.Aborter, coresql.Deferrer) {
	t.Helper()
	tm, _, deferrer := siblings(t, f)
	onAbort, canAbort := tm.(coresql.Aborter)
	if !canAbort {
		t.Fatal("the SDK's transactor is not a core/data/sql Aborter")
	}
	return tm, onAbort, deferrer
}

// TestAbortedFunctionsRunAfterEachWayARootDoesNotCommit covers the four ways a
// root transaction ends without a commit — the unit of work fails, a poisoned
// transaction refuses its commit, the COMMIT itself fails, and a panic unwinds
// it — and pins, for each, that the functions held for an abort run once,
// newest first, after the ROLLBACK, on a context the caller's cancellation
// does not reach, while what was held for the commit does not run.
func TestAbortedFunctionsRunAfterEachWayARootDoesNotCommit(t *testing.T) {
	t.Parallel()
	for name, end := range map[string]struct {
		work      func(ctx context.Context, tm coresql.Transactor) error
		failOn    string
		commitErr error
		panics    bool
		rollback  bool
	}{
		"the unit of work fails": {
			work:     func(context.Context, coresql.Transactor) error { return errWork },
			rollback: true,
		},
		"a poisoned transaction refuses its commit": {
			work: func(ctx context.Context, tm coresql.Transactor) error {
				verdictIgnored(t, tm.Transact(ctx, coresql.TxOptionsValue{}, func(context.Context, coresql.Executor) error {
					return errWork
				}))
				return nil
			},
			failOn:   "ROLLBACK TO SAVEPOINT ktn_sp_1",
			rollback: true,
		},
		"the commit fails": {
			work:      func(context.Context, coresql.Transactor) error { return nil },
			commitErr: errScripted,
		},
		"a panic": {
			work:     func(context.Context, coresql.Transactor) error { panic("boom") },
			panics:   true,
			rollback: true,
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f := newFakeDB()
			f.commitErr = end.commitErr
			if end.failOn != "" {
				f.failOn(end.failOn)
			}
			tm, onAbort, deferrer := aborter(t, f)
			var ran []string
			var sawRollback, sawEnd []bool
			committed := false
			hold := func(scope context.Context, label string) {
				// The context OnAbort is given has ended by the time the
				// function runs, as a caller's often has after a rollback.
				ended, cancel := context.WithCancel(scope)
				defer cancel()
				if !onAbort.OnAbort(ended, func(detached context.Context) {
					ran = append(ran, label)
					sawRollback = append(sawRollback, slices.Contains(f.statements(), "ROLLBACK"))
					sawEnd = append(sawEnd, detached.Err() != nil)
				}) {
					t.Errorf("OnAbort(%s) did not hold inside a transaction", label)
				}
			}
			defer func() {
				recovered := recover()
				if (recovered != nil) != end.panics {
					t.Errorf("recovered %v, want a panic: %v", recovered, end.panics)
				}
				if committed {
					t.Error("a function held for the commit ran for a transaction that did not commit")
				}
				if !slices.Equal(ran, []string{"second", "first"}) {
					t.Fatalf("aborted functions ran as %v, want second then first", ran)
				}
				if end.rollback && !slices.Equal(sawRollback, []bool{true, true}) {
					t.Errorf("aborted functions saw the ROLLBACK = %v, want both after it", sawRollback)
				}
				if !slices.Equal(sawEnd, []bool{false, false}) {
					t.Errorf("aborted functions saw their context ended = %v, want a detached one", sawEnd)
				}
			}()
			verdictIgnored(t, tm.Transact(t.Context(), coresql.TxOptionsValue{}, func(scope context.Context, _ coresql.Executor) error {
				hold(scope, "first")
				deferrer.Defer(scope, func() { committed = true })
				hold(scope, "second")
				if len(ran) != 0 {
					t.Errorf("aborted functions ran before the end: %v", ran)
				}
				return end.work(scope, tm)
			}))
		})
	}
}

// TestACommitDropsWhatWaitedForAnAbort pins the complement: a commit that
// stood runs what was held for it and nothing held for an abort.
func TestACommitDropsWhatWaitedForAnAbort(t *testing.T) {
	t.Parallel()
	tm, onAbort, deferrer := aborter(t, newFakeDB())
	var ran []string
	err := tm.Transact(t.Context(), coresql.TxOptionsValue{}, func(ctx context.Context, _ coresql.Executor) error {
		onAbort.OnAbort(ctx, func(context.Context) { ran = append(ran, "aborted") })
		deferrer.Defer(ctx, func() { ran = append(ran, "committed") })
		return nil
	})
	if err != nil {
		t.Fatalf("Transact: %v", err)
	}
	if !slices.Equal(ran, []string{"committed"}) {
		t.Fatalf("ran %v, want only the commit's function", ran)
	}
}

// TestASavepointsRollbackRunsWhatItHeldForAnAbort pins what "aborted" means
// for a nested scope: its savepoint rolled back, so its work is undone even
// though the transaction around it commits. What it and the scopes inside it
// held for an abort runs at once, after the ROLLBACK TO SAVEPOINT and before
// its Transact returns; what the outer scope held is dropped with the commit.
func TestASavepointsRollbackRunsWhatItHeldForAnAbort(t *testing.T) {
	t.Parallel()
	f := newFakeDB()
	tm, onAbort, _ := aborter(t, f)
	var ran []string
	var undone []bool
	hold := func(ctx context.Context, label string) {
		onAbort.OnAbort(ctx, func(context.Context) {
			ran = append(ran, label)
			undone = append(undone, slices.Contains(f.statements(), "ROLLBACK TO SAVEPOINT ktn_sp_1"))
		})
	}
	err := tm.Transact(t.Context(), coresql.TxOptionsValue{}, func(ctx context.Context, _ coresql.Executor) error {
		hold(ctx, "outer")
		caught := tm.Transact(ctx, coresql.TxOptionsValue{}, func(nested context.Context, _ coresql.Executor) error {
			hold(nested, "failed savepoint")
			if innerErr := tm.Transact(nested, coresql.TxOptionsValue{}, func(deeper context.Context, _ coresql.Executor) error {
				hold(deeper, "released inside the failed one")
				return nil
			}); innerErr != nil {
				return innerErr
			}
			return errWork
		})
		if caught == nil {
			t.Error("the nested failure did not reach its caller")
		}
		if !slices.Equal(ran, []string{"released inside the failed one", "failed savepoint"}) {
			t.Errorf("when the savepoint returned, ran %v, want its two functions, newest first", ran)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("Transact: %v", err)
	}
	if !slices.Equal(ran, []string{"released inside the failed one", "failed savepoint"}) {
		t.Fatalf("ran %v, want only the failed savepoint's functions — the outer one committed", ran)
	}
	if !slices.Equal(undone, []bool{true, true}) {
		t.Fatalf("the functions saw the ROLLBACK TO SAVEPOINT = %v, want both after it", undone)
	}
}

// TestAReleasedSavepointHandsItsAbortsToTheScopeAroundIt pins the other half:
// a savepoint that succeeds keeps what it held for an abort, a commit drops
// it, and a rollback of the transaction around it runs it — once.
func TestAReleasedSavepointHandsItsAbortsToTheScopeAroundIt(t *testing.T) {
	t.Parallel()
	for name, outcome := range map[string]struct {
		err  error
		want []string
	}{
		"the transaction commits":    {want: nil},
		"the transaction rolls back": {err: errWork, want: []string{"released"}},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			tm, onAbort, _ := aborter(t, newFakeDB())
			var ran []string
			verdictIgnored(t, tm.Transact(t.Context(), coresql.TxOptionsValue{}, func(ctx context.Context, _ coresql.Executor) error {
				if nestedErr := tm.Transact(ctx, coresql.TxOptionsValue{}, func(nested context.Context, _ coresql.Executor) error {
					onAbort.OnAbort(nested, func(context.Context) { ran = append(ran, "released") })
					return nil
				}); nestedErr != nil {
					return nestedErr
				}
				if len(ran) != 0 {
					t.Errorf("a released savepoint ran %v", ran)
				}
				return outcome.err
			}))
			if !slices.Equal(ran, outcome.want) {
				t.Fatalf("ran %v, want %v", ran, outcome.want)
			}
		})
	}
}

// TestAPanicInASavepointRunsWhatItHeldForAnAbort pins the nested panic path:
// the savepoint is undone on the way out, what it held for an abort runs, and
// the panic continues to the caller.
func TestAPanicInASavepointRunsWhatItHeldForAnAbort(t *testing.T) {
	t.Parallel()
	f := newFakeDB()
	tm, onAbort, _ := aborter(t, f)
	var ran []string
	defer func() {
		if recovered := recover(); recovered != "nested" {
			t.Fatalf("recovered %v, want the nested panic", recovered)
		}
		if !slices.Equal(ran, []string{"nested", "outer"}) {
			t.Fatalf("ran %v, want the savepoint's function, then the root's", ran)
		}
	}()
	verdictIgnored(t, tm.Transact(t.Context(), coresql.TxOptionsValue{}, func(ctx context.Context, _ coresql.Executor) error {
		onAbort.OnAbort(ctx, func(context.Context) { ran = append(ran, "outer") })
		return tm.Transact(ctx, coresql.TxOptionsValue{}, func(nested context.Context, _ coresql.Executor) error {
			onAbort.OnAbort(nested, func(context.Context) { ran = append(ran, "nested") })
			panic("nested")
		})
	}))
	t.Fatal("Transact returned instead of re-raising the panic")
}

// TestOnAbortHoldsNothingWithoutAnOpenTransaction pins the false answers, as
// Defer gives them: no transaction on the context, another transactor's
// transaction, and a transaction that is already over. A nil fn inside one
// answers true and holds nothing.
func TestOnAbortHoldsNothingWithoutAnOpenTransaction(t *testing.T) {
	t.Parallel()
	tm, onAbort, _ := aborter(t, newFakeDB())
	ran := false
	if onAbort.OnAbort(t.Context(), func(context.Context) { ran = true }) {
		t.Error("OnAbort held a function on a context with no transaction")
	}
	other := newTransactor(t, newFakeDB())
	verdictIgnored(t, other.Transact(t.Context(), coresql.TxOptionsValue{}, func(ctx context.Context, _ coresql.Executor) error {
		if onAbort.OnAbort(ctx, func(context.Context) { ran = true }) {
			t.Error("OnAbort held a function on another transactor's transaction")
		}
		return nil
	}))
	var leaked context.Context
	verdictIgnored(t, tm.Transact(t.Context(), coresql.TxOptionsValue{}, func(ctx context.Context, _ coresql.Executor) error {
		leaked = ctx
		if !onAbort.OnAbort(ctx, nil) {
			t.Error("OnAbort(nil) inside a transaction answered false")
		}
		return errWork
	}))
	if onAbort.OnAbort(leaked, func(context.Context) { ran = true }) {
		t.Error("OnAbort held a function on a transaction that already ended")
	}
	if ran {
		t.Fatal("the transactor ran a function it did not hold")
	}
}
