// Package sql — hosts the transactor's two ADR 0039 siblings: where a
// statement issued under a context runs, and what waits for a commit.
package sql

import (
	"context"

	coresql "github.com/kitsunium/sdk/internal/core/data/sql"
)

// Join returns the executor a statement issued under ctx runs on: the
// innermost scope of the transaction ctx carries for this transactor, else the
// pool (core/data/sql.Joiner, ADR 0139).
//
// A scope that has already returned still answers with its own, retired,
// executor rather than with the pool: a context that outlived its transaction
// gets TX_CLOSED on its next statement instead of running it, silently,
// outside the transaction it was written for.
func (t *transactor) Join(ctx context.Context) (ex coresql.Executor, inTx bool) {
	//: OUR transaction only — another transactor's is another database.
	if scope := scopeFor(ctx, t); scope != nil {
		//: the executor that scope lent its unit of work.
		return scope.exec, true
	}
	//: no transaction of ours: the pool, one statement at a time.
	return t.cfg.db, false
}

// Defer holds fn until the transaction ctx carries for this transactor
// commits, and reports whether it did (core/data/sql.Deferrer, ADR 0139).
//
// fn is tagged with the scope ctx names, so a savepoint that rolls back drops
// what was held inside it, and one that is released leaves it to the scope
// around it. The root runs what is left, in order, once its commit stood.
func (t *transactor) Defer(ctx context.Context, fn func()) bool {
	scope := scopeFor(ctx, t)
	//: no transaction of ours: nothing to wait for, fn stays the caller's.
	if scope == nil {
		//: not held.
		return false
	}
	//: false once the root has settled — the commit is behind it.
	return scope.state.hold(scope.id, fn)
}
