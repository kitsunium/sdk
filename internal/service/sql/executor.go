// Package sql — hosts the scope-bound Executor handed to a TxFunc, and the
// two guards that stand between a stale handle and a live transaction.
package sql

import (
	"context"
	stdsql "database/sql"
	"sync/atomic"
)

// deadContext is an already-cancelled context, built once at init.
//
// It is how [scopedExecutor.QueryRowContext] refuses: *sql.Row has no
// exported constructor and no exported error field, so a typed refusal cannot
// be handed back — but (*sql.Tx).QueryRowContext checks ctx.Done() in
// grabConn BEFORE it acquires a connection, so delegating with a dead context
// guarantees the statement never leaves the process, and Scan reports the
// cancellation.
//
// Package-level rather than derived per call: the caller's context values are
// irrelevant to a statement that will not run, and building one cancelled
// context per refusal would allocate on the path that exists to do nothing.
var deadContext = func() context.Context {
	ctx, cancel := context.WithCancel(context.Background())
	//: born cancelled — that IS the value.
	cancel()
	//: every read through it stops in grabConn.
	return ctx
}()

// scopedExecutor is the core/sql.Executor a TxFunc receives. It is valid for
// the duration of that call and refuses afterwards.
//
// Two things it deliberately is NOT: it is not a *sql.Tx (which would carry
// Commit and Rollback into the unit of work) and it is not a copy of the
// transaction (which would need a second commit path). It is a guard in front
// of the one transaction its scope belongs to.
type scopedExecutor struct {
	// state is the shared transaction state — the tx itself and the poison
	// flag every scope of this transaction reads.
	state *txState
	// live is cleared when the scope returns. Atomic because a caller that
	// leaks the executor to another goroutine is exactly the case this
	// field exists to catch, and racing on the check would make the race
	// detector fire instead of the guard.
	live atomic.Bool
}

// newScopedExecutor returns an executor valid until [scopedExecutor.retire].
func newScopedExecutor(state *txState) *scopedExecutor {
	//: born live; retire is the only thing that changes that.
	ex := &scopedExecutor{state: state}
	//: explicit rather than relying on the zero value, which is false.
	ex.live.Store(true)
	//: the handle the unit of work will run on.
	return ex
}

// retire invalidates the executor. Called when the scope that lent it returns
// — on success, on failure and on the panic path alike.
func (e *scopedExecutor) retire() {
	//: every later Exec/Query on this handle now refuses.
	e.live.Store(false)
}

// usable reports why the executor may not run a statement, or nil.
//
// Two refusals, in the order that makes a diagnosis readable: a stale handle
// is the caller's bug, a poisoned transaction is the database's state. Both
// must be checked BEFORE the statement reaches the driver — for a nested
// scope, the outer transaction is still open, so a stale handle would happily
// execute against it.
func (e *scopedExecutor) usable() error {
	//: the scope that lent this handle has already returned.
	if !e.live.Load() {
		//: the caller's bug, named as one.
		return failed(TxClosed, nil)
	}
	//: a savepoint rollback failed somewhere in this transaction.
	if poison := e.state.poisonedErr(); poison != nil {
		//: the original cause travels beside the verdict.
		return failed(TxPoisoned, poison)
	}
	//: the handle may run.
	return nil
}

// ExecContext runs a statement that returns no rows.
func (e *scopedExecutor) ExecContext(
	ctx context.Context, query string, args ...any,
) (result stdsql.Result, err error) {
	//: refuse before the driver sees anything.
	if err := e.usable(); err != nil {
		//: nothing ran.
		return nil, err
	}
	//: the transaction is the caller's; database/sql owns the retry policy.
	return e.state.tx.ExecContext(ctx, query, args...)
}

// QueryContext runs a statement that returns rows.
func (e *scopedExecutor) QueryContext(
	ctx context.Context, query string, args ...any,
) (rows *stdsql.Rows, err error) {
	//: refuse before the driver sees anything.
	if err := e.usable(); err != nil {
		//: nothing ran.
		return nil, err
	}
	//: rows belong to the caller, who closes them.
	return e.state.tx.QueryContext(ctx, query, args...)
}

// QueryRowContext runs a statement expected to return at most one row.
//
// # A named stdlib limitation
//
// This is the one method that cannot carry a typed refusal. *sql.Row has no
// exported constructor and no exported error field, so there is no way to
// hand back a Row whose Scan returns [TxClosed] or [TxPoisoned].
//
// What matters more than the code is that NOTHING EXECUTES, and that is
// guaranteed: on a refused handle the call is delegated with an
// already-cancelled context, and (*sql.Tx).QueryRowContext checks ctx.Done
// before it acquires a connection. Scan then reports the cancellation.
// TestQueryRowOnARetiredExecutorReachesNoDriver pins both halves.
//
// A caller who needs the typed code for a single-row read uses QueryContext.
func (e *scopedExecutor) QueryRowContext(ctx context.Context, query string, args ...any) *stdsql.Row {
	//: the ordinary path — a usable handle runs the statement as given.
	if err := e.usable(); err == nil {
		//: at most one row, scanned by the caller.
		return e.state.tx.QueryRowContext(ctx, query, args...)
	}
	//: same call shape, guaranteed not to reach the driver.
	return e.state.tx.QueryRowContext(deadContext, query, args...)
}

// PrepareContext compiles a statement for repeated execution inside this
// scope. It satisfies the ADR 0039 sibling core/sql.Preparer; the returned
// statement is bound to the transaction and dies with it.
func (e *scopedExecutor) PrepareContext(
	ctx context.Context, query string,
) (stmt *stdsql.Stmt, err error) {
	//: refuse before the driver sees anything.
	if err := e.usable(); err != nil {
		//: nothing was compiled.
		return nil, err
	}
	//: the statement is the caller's, who closes it.
	return e.state.tx.PrepareContext(ctx, query)
}
