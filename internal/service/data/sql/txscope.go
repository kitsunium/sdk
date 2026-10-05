package sql

import "context"

// rootScope identifies the scope of a root transaction. A savepoint scope is
// identified by its savepoint counter, which starts at one, so zero is free
// for the root and sorts before every scope opened inside it.
const rootScope uint64 = 0

// txScope is one link of the per-context transaction chain: which transactor
// opened a transaction, its shared state, the executor the scope lent, and
// whatever scope was already on the context.
//
// A chain rather than a single value because an application with TWO databases
// is normal, and a Transact on database B inside a Transact on database A must
// open a real transaction on B — not a savepoint on A's. Walking the chain for
// the matching owner is what makes that true.
type txScope struct {
	// owner identifies the transactor whose transaction this is.
	owner *transactor
	// state is the shared per-transaction state.
	state *txState
	// exec is the executor this scope lent its unit of work. Join hands it to
	// a callee, so a statement issued under this context runs in this
	// transaction — and is refused once the scope has returned.
	exec *scopedExecutor
	// parent is the scope this one was opened inside, or nil.
	parent *txScope
	// id is rootScope for a root transaction and the savepoint counter for a
	// nested one. A function held under this scope carries it, so the
	// savepoint's rollback drops exactly what was held inside it.
	id uint64
}

// scopeOf returns the innermost scope carried by ctx, or nil.
func scopeOf(ctx context.Context) *txScope {
	//: a context with no transaction yields the untyped nil, not a panic.
	scope, _ := ctx.Value(scopeKey).(*txScope)
	//: nil means "no transaction is open on this context".
	return scope
}

// scopeFor returns the innermost scope on ctx that belongs to owner, or nil.
func scopeFor(ctx context.Context, owner *transactor) *txScope {
	//: walk outward: the innermost matching transaction is the one a nested
	//: call must join.
	for scope := scopeOf(ctx); scope != nil; scope = scope.parent {
		//: a scope belonging to another manager is another database.
		if scope.owner == owner {
			//: found the transaction this call must nest inside.
			return scope
		}
	}
	//: no transaction of ours on this context — the caller opens a real one.
	return nil
}

// withScope returns ctx carrying scope as its new innermost scope.
func withScope(ctx context.Context, scope *txScope) context.Context {
	//: the previous innermost scope becomes this one's parent, so a
	//: three-database interleaving still resolves each to its own manager.
	scope.parent = scopeOf(ctx)
	//: the chain, one link longer.
	return context.WithValue(ctx, scopeKey, scope)
}
