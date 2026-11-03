package sql

import (
	"context"
	stdsql "database/sql"
	"errors"
	"sync"
	"time"

	coresql "github.com/kitsunium/sdk/internal/core/data/sql"
	"github.com/kitsunium/sdk/internal/kernel/clock"
	kerrs "github.com/kitsunium/sdk/internal/kernel/errs"
)

// The operations a statement budget names when it cuts one.
const (
	operationExec    string = "exec"
	operationQuery   string = "query"
	operationPrepare string = "prepare"
)

// noRelease is the release of an unbounded statement: nothing to stop. A
// package-level value, so the unbounded path allocates nothing.
var noRelease func() = func() {}

// stmtBudget bounds one statement on the injected clock (Config's
// StatementTimeout). The zero value bounds nothing.
type stmtBudget struct {
	// limit is the budget; non-positive is none.
	limit time.Duration
	// clk is the clock the budget is measured on.
	clk clock.Timed
}

// bound returns the context a statement runs with — ctx, cut with
// StatementTimedOut as its cause once the budget elapses on the clock — and
// the release that ends the wait for a statement that returned in time.
//
// A watcher goroutine waits on the clock's timer, because a kernel clock
// fires a channel rather than a function; it ends with the timer, with ctx,
// or with the release, whichever comes first. A query is never released: its
// rows read through the cut context until they close, so the watcher lives
// until the budget elapses or ctx ends.
func (b stmtBudget) bound(ctx context.Context) (cut context.Context, release func()) {
	//: no budget: the statement runs on the caller's context alone.
	if b.limit <= 0 {
		//: nothing to stop.
		return ctx, noRelease
	}
	cut, cancel := context.WithCancelCause(ctx)
	timer := b.clk.NewTimer(b.limit)
	released := make(chan struct{})
	go func() {
		defer timer.Stop()
		select {
		//: the budget elapsed first: the statement is cut, its cause named.
		case <-timer.C():
			cancel(coresql.StatementTimedOut)
		//: the statement returned in time.
		case <-released:
		//: the caller's own end, which the cut context already carries.
		case <-cut.Done():
		}
	}()
	var once sync.Once
	//: closing twice would panic; a release is called at most once anyway.
	return cut, func() {
		once.Do(func() {
			close(released)
			cancel(nil)
		})
	}
}

// verdict is err as the caller reads it: joined with StatementTimedOut when
// the budget cut the statement, the operation named; otherwise as it came.
func (b stmtBudget) verdict(cut context.Context, operation string, err error) error {
	//: a statement that did not fail, ran with no budget, or was ended by the
	//: caller rather than cut by this budget, has nothing to add.
	if err == nil || b.limit <= 0 || !errors.Is(context.Cause(cut), coresql.StatementTimedOut) {
		//: as it came.
		return err
	}
	//: the verdict beside the driver's error.
	return failed(coresql.StatementTimedOut, err, kerrs.String("operation", operation))
}

// statementRunner is what a bounded statement runs on: a pool or a
// transaction, both of which database/sql gives the same four methods.
type statementRunner interface {
	ExecContext(ctx context.Context, query string, args ...any) (stdsql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*stdsql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *stdsql.Row
	PrepareContext(ctx context.Context, query string) (*stdsql.Stmt, error)
}

// exec runs a statement that returns no rows on on, within the budget.
func (b stmtBudget) exec(ctx context.Context, on statementRunner, query string, args []any) (stdsql.Result, error) {
	cut, release := b.bound(ctx)
	defer release()
	result, err := on.ExecContext(cut, query, args...)
	//: the cut, named.
	return result, b.verdict(cut, operationExec, err)
}

// query runs a statement that returns rows on on, within the budget — the
// reading of its rows included, so the cut context is never released early.
func (b stmtBudget) query(ctx context.Context, on statementRunner, query string, args []any) (*stdsql.Rows, error) {
	cut, _ := b.bound(ctx)
	rows, err := on.QueryContext(cut, query, args...)
	//: the cut, named.
	return rows, b.verdict(cut, operationQuery, err)
}

// prepare compiles a statement on on, the compilation within the budget.
func (b stmtBudget) prepare(ctx context.Context, on statementRunner, query string) (*stdsql.Stmt, error) {
	cut, release := b.bound(ctx)
	defer release()
	stmt, err := on.PrepareContext(cut, query)
	//: the cut, named.
	return stmt, b.verdict(cut, operationPrepare, err)
}

// timedPool is a pool whose statements run under the statement budget: what
// Join and Reader answer outside a transaction when Config.StatementTimeout is
// set. It satisfies core/data/sql.Executor and its sibling Preparer, as the
// *sql.DB it stands for does.
type timedPool struct {
	// db is the pool the statements run on.
	db *stdsql.DB
	// budget bounds each of them.
	budget stmtBudget
}

// executors returns the pools the transactor hands out outside a
// transaction: the primary's, and the one reads are routed to — the
// replica's when one is configured.
func (r resolved) executors() (primary, reader coresql.Executor) {
	primary = r.pool(r.db)
	//: no replica: reads run where writes do.
	if r.replica == nil {
		//: the primary's pool, twice.
		return primary, primary
	}
	//: the replica's pool for reads.
	return primary, r.pool(r.replica)
}

// pool is db as the transactor hands it out: itself when no budget is set,
// so an unbounded caller still reaches the *sql.DB it configured.
func (r resolved) pool(db *stdsql.DB) coresql.Executor {
	//: no budget, nothing to wrap.
	if r.budget.limit <= 0 {
		//: the caller's own pool.
		return db
	}
	//: the pool, each statement bounded.
	return timedPool{db: db, budget: r.budget}
}

// ExecContext runs a statement that returns no rows, within the budget.
func (p timedPool) ExecContext(ctx context.Context, query string, args ...any) (stdsql.Result, error) {
	//: bounded, the cut named.
	return p.budget.exec(ctx, p.db, query, args)
}

// QueryContext runs a statement that returns rows, within the budget — the
// reading of its rows included.
func (p timedPool) QueryContext(ctx context.Context, query string, args ...any) (*stdsql.Rows, error) {
	//: bounded until the rows close or the budget elapses.
	return p.budget.query(ctx, p.db, query, args)
}

// QueryRowContext runs a statement expected to return at most one row,
// within the budget; a cut surfaces from Scan as the context's end.
func (p timedPool) QueryRowContext(ctx context.Context, query string, args ...any) *stdsql.Row {
	cut, _ := p.budget.bound(ctx)
	//: the row reads through the cut context until Scan.
	return p.db.QueryRowContext(cut, query, args...)
}

// PrepareContext compiles a statement on the pool, the compilation within
// the budget.
func (p timedPool) PrepareContext(ctx context.Context, query string) (*stdsql.Stmt, error) {
	//: bounded, the cut named.
	return p.budget.prepare(ctx, p.db, query)
}
