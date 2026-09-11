// Package sql — hosts the per-context transaction chain: which transactor
// opened a transaction, and how a nested call finds its own.
package sql

import "context"

// txScope is one link of the per-context transaction chain: which transactor
// opened a transaction, its shared state, and whatever scope was already on
// the context.
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
	// parent is the scope this one was opened inside, or nil.
	parent *txScope
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

// withScope returns ctx carrying a new innermost scope.
func withScope(ctx context.Context, owner *transactor, state *txState) context.Context {
	//: the previous innermost scope becomes this one's parent, so a
	//: three-database interleaving still resolves each to its own manager.
	return context.WithValue(ctx, scopeKey, &txScope{owner: owner, state: state, parent: scopeOf(ctx)})
}
