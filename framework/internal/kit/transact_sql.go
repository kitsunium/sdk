package kit

import (
	"context"
	"fmt"
	"time"

	"github.com/kitsunium/sdk/pkg/v1/data/sql"
	"github.com/kitsunium/sdk/pkg/v1/errs"
)

// A transaction on a database (ADR 0004) is the database's own: the SDK's
// Transactor opens it, and a level nested in it is a savepoint. The SDK runs
// a unit of work inside a function it calls, where kit's transaction begins
// at its first store call on the database, in the middle of the product's
// function: each level's transaction or savepoint is opened on a goroutine
// of its own, which waits inside the SDK's function until the level ends,
// and hands the context that carries it to the store calls of the level.
// The level's end is that function's return — nil commits or releases, an
// error rolls back —, and it waits for the SDK to settle it: nothing of a
// level outlives it.

// sqlScope is one level's transaction or savepoint on its database: the
// context the SDK's function was given, and the way to end it.
type sqlScope struct {
	// ctx carries the SDK's transaction for this level: a store call under
	// it runs in the transaction, in this level's savepoint.
	ctx context.Context
	// end hands the level's outcome to the SDK's function; done is what the
	// SDK's Transact returned, and caught a panic of a function the SDK held
	// until the commit — a store's write hook —, which the level's end
	// raises again on its own goroutine.
	end    chan error
	done   chan error
	caught any
}

// scopeBase is where a level's transaction opens: the context it is begun
// under — a savepoint when it carries one of tm's — and the transactor.
type scopeBase struct {
	ctx context.Context
	tm  sql.Transactor
}

// openScope opens a transaction on at's transactor and returns once the
// SDK runs its function in it, or refused it. wait bounds the wait: the call
// that needs the transaction's.
//
// Goroutine lifecycle: one goroutine runs the SDK's Transact until the
// level's close hands it its outcome, or until wait ends first; either way
// done says it returned, and nothing of the level outlives it.
func openScope(wait context.Context, at scopeBase) (*sqlScope, error) {
	s := &sqlScope{end: make(chan error, 1), done: make(chan error, 1)}
	tm := at.tm
	began, cancel := context.WithCancel(at.ctx)
	ready := make(chan struct{})
	go func() {
		defer cancel()
		s.done <- s.run(began, tm, ready)
	}()
	select {
	case <-ready:
		return s, nil
	case err := <-s.done:
		return nil, err
	case <-wait.Done():
		cancel()
		s.end <- wait.Err()
		<-s.done
		return nil, wait.Err()
	}
}

// errHeldPanicked is what run returns when a function the SDK held until the
// commit panicked: close raises the panic again before anyone reads it.
var errHeldPanicked = errs.New(CodeTransactionPanic, "TRANSACTION_HELD_PANICKED", "a function held until the commit panicked",
	"kit: a function the SDK ran after a transaction's commit panicked; the level's end raises the panic again")

// run is the SDK's Transact of the scope, its function waiting for the
// level's end. A panic of what the SDK runs after the commit is caught, for
// the level's end to raise.
func (s *sqlScope) run(ctx context.Context, tm sql.Transactor, ready chan struct{}) (err error) {
	defer func() {
		if p := recover(); p != nil {
			s.caught = p
			err = errHeldPanicked
		}
	}()
	return tm.Transact(ctx, sql.TxOptions{}, func(ctx context.Context, _ sql.Executor) error {
		s.ctx = ctx
		close(ready)
		return <-s.end
	})
}

// close ends the level with its outcome — nil commits the transaction or
// releases the savepoint, an error rolls it back — and returns what the
// database answered: the commit's refusal, or the outcome itself.
func (s *sqlScope) close(outcome error) error {
	s.end <- outcome
	err := <-s.done
	if s.caught != nil {
		panic(s.caught)
	}
	return err
}

// joinCall is the context a store call on database r runs with, inside the
// transaction ctx carries: the level's transaction on r — opened now, at
// the transaction's first call on r, and with it the savepoints of the
// levels between — else ctx itself, on the pool. A write to r in a
// transaction that belongs to another database, or to the data directory,
// is refused; a read runs on the pool, as it would outside.
func joinCall(ctx context.Context, a *App, r *databaseRun, store string, write bool) (context.Context, error) {
	u := unitOf(ctx)
	if u == nil {
		return ctx, nil
	}
	t := u.tx
	t.mu.Lock()
	if t.app == nil {
		t.app = a
	}
	switch {
	case t.db == nil && !t.local:
		t.db = r
	case t.db == r:
	case !write:
		t.mu.Unlock()
		return ctx, nil
	case t.local:
		t.mu.Unlock()
		return nil, spanRefusal(store, localName, fmt.Sprintf("database %q", r.d.name))
	default:
		name := t.db.d.name
		t.mu.Unlock()
		return nil, spanRefusal(store, fmt.Sprintf("database %q", name), fmt.Sprintf("database %q", r.d.name))
	}
	t.mu.Unlock()
	scope, err := u.sqlScope(ctx, r)
	if err != nil {
		return nil, err
	}
	return scope.ctx, nil
}

// sqlScope is the level's transaction or savepoint on r, opened now when it
// has none — the levels around it first, which then have theirs.
func (u *unit) sqlScope(wait context.Context, r *databaseRun) (*sqlScope, error) {
	u.scopeMu.Lock()
	defer u.scopeMu.Unlock()
	if u.scope != nil {
		return u.scope, nil
	}
	base := u.tx.base
	if u.parent != nil {
		outer, err := u.parent.sqlScope(wait, r)
		if err != nil {
			return nil, err
		}
		base = outer.ctx
	}
	tm := r.transactor()
	if tm == nil {
		return nil, Unavailable(fmt.Sprintf("database %q is not open", r.d.name))
	}
	s, err := openScope(wait, scopeBase{ctx: base, tm: tm})
	if err != nil {
		return nil, r.said(err)
	}
	u.scope = s
	return s, nil
}

// callContext is the context one store call on r runs with: in the level's
// transaction when ctx carries one on r, else ctx; bounded by
// <name>-timeout, and ended with ctx. done releases it.
func callContext(ctx context.Context, a *App, r *databaseRun, store string, write bool) (context.Context, func(), error) {
	bounded, cancel := context.WithTimeout(ctx, r.timeout())
	joined, err := joinCall(bounded, a, r, store, write)
	if err != nil {
		cancel()
		return nil, func() {}, err
	}
	if joined == bounded {
		return bounded, cancel, nil
	}
	// The transaction's context ends with the transaction; the call ends
	// with its caller too, and within its own bound.
	call, cancelCall := context.WithTimeout(joined, r.timeout())
	stop := context.AfterFunc(ctx, cancelCall)
	return call, func() { stop(); cancelCall(); cancel() }, nil
}

// timeout is <name>-timeout, as the start resolved it.
func (r *databaseRun) timeout() time.Duration {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.callTimeout > 0 {
		return r.callTimeout
	}
	return 5 * time.Second
}
