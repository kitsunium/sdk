package sql

import (
	"context"
	"slices"
	"sync"

	stdsql "database/sql"

	coresql "github.com/kitsunium/sdk/internal/core/data/sql"
)

// heldFn is one function held until the commit, tagged with the scope it was
// held in.
type heldFn struct {
	// fn runs after the commit.
	fn func()
	// scope is the id of the scope ctx named when fn was held.
	scope uint64
}

// abortFn is one function held until the work of its scope is undone, tagged
// with that scope and handed the context it was held under, detached.
type abortFn struct {
	// ctx is the context OnAbort was given, without its ending: a function
	// that cleans up after a rollback must not die of the cancellation that
	// caused it.
	ctx context.Context
	// fn runs after the rollback.
	fn func(ctx context.Context)
	// scope is the id of the scope ctx named when fn was held.
	scope uint64
}

// txState is the state ONE transaction shares across its root scope and every
// savepoint nested inside it.
//
// It is reachable only through the context chain, and only by the transactor
// that created it, so two managers over two different databases never see each
// other's transactions.
type txState struct {
	// mu serialises the savepoint counter, the poison flag and the held
	// functions. The underlying *sql.Tx is itself safe for concurrent use, but
	// the counter must not hand two goroutines the same savepoint name.
	//
	// RWMutex rather than Mutex because the poison flag is READ on every
	// statement (executor.usable) and written at most once per transaction:
	// the read side is the hot one, and it is the one that must not serialise
	// two goroutines sharing a transaction.
	mu sync.RWMutex
	// tx is the single database transaction every scope runs on.
	tx *stdsql.Tx
	// poisoned is non-nil once the transaction can no longer be trusted:
	// a savepoint rollback failed, so what the engine has kept is unknown.
	poisoned error
	// held are the functions waiting for the commit, in the order they were
	// held (the Deferrer sibling, ADR 0139).
	held []heldFn
	// aborted are the functions waiting for the work of their scope to be
	// undone, in the order they were held (the Aborter sibling, ADR 0139 as
	// amended on 2026-10-14).
	aborted []abortFn
	// counter numbers savepoints and NEVER resets inside one transaction, so
	// a name is never reused — see savepointName for why reuse is a trap.
	counter uint64
	// dialect selects the savepoint grammar.
	dialect coresql.Dialect
	// ended is set once the root scope has committed or rolled back: nothing
	// is held after that, because there is no commit left to wait for.
	ended bool
}

// nextSavepoint reserves the next savepoint for this transaction: its number,
// which identifies the scope it opens, and its name.
func (s *txState) nextSavepoint() (id uint64, name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	//: monotonic and never reset — reuse would shadow rather than replace.
	s.counter++
	//: the reserved savepoint belongs to the caller until the transaction ends.
	return s.counter, savepointName(s.counter)
}

// poison marks the transaction unusable and records why. The FIRST poison
// wins: a later failure caused by the first must not overwrite the diagnosis
// that explains it.
func (s *txState) poison(cause error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	//: keep the first cause — it is the one that names the real event.
	if s.poisoned == nil {
		//: every later operation on this transaction now refuses.
		s.poisoned = cause
	}
}

// poisonedErr returns the recorded poison cause, or nil.
func (s *txState) poisonedErr() error {
	s.mu.RLock()
	defer s.mu.RUnlock()
	//: nil means the transaction is still trustworthy.
	return s.poisoned
}

// hold keeps fn until the commit, tagged with scope, and reports whether the
// transaction is still open to take it.
func (s *txState) hold(scope uint64, fn func()) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	//: a settled transaction has no commit left to wait for.
	if s.ended {
		//: the caller keeps fn.
		return false
	}
	//: a nil fn holds nothing, and the answer stays the same.
	if fn != nil {
		s.held = append(s.held, heldFn{fn: fn, scope: scope})
	}
	//: held until the commit, or dropped by a rollback.
	return true
}

// holdAbort keeps fn until the work of scope is undone, with ctx detached,
// and reports whether the transaction is still open to take it.
func (s *txState) holdAbort(ctx context.Context, scope uint64, fn func(ctx context.Context)) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	//: a settled transaction has nothing left to undo.
	if s.ended {
		//: the caller keeps fn.
		return false
	}
	//: a nil fn holds nothing, and the answer stays the same.
	if fn != nil {
		s.aborted = append(s.aborted, abortFn{ctx: context.WithoutCancel(ctx), fn: fn, scope: scope})
	}
	//: held until the work is undone, or dropped by a commit.
	return true
}

// dropFrom drops every function held for the commit in the scope a failed
// savepoint opened, or in a scope opened inside it, and hands back the
// functions those scopes held for an abort, to run once the savepoint has
// rolled back.
//
// Scopes are a stack and their ids grow with every savepoint, so while the
// scope numbered from is unwinding, everything held with an id at or above it
// was held inside it. A savepoint that is released drops nothing: what it held
// stays, and now belongs to the scope around it.
func (s *txState) dropFrom(from uint64) []abortFn {
	s.mu.Lock()
	defer s.mu.Unlock()
	inside := func(scope uint64) bool { return scope >= from }
	//: what the failed scope held goes with its work.
	s.held = slices.DeleteFunc(s.held, func(h heldFn) bool { return inside(h.scope) })
	var undone []abortFn
	kept := s.aborted[:0]
	//: what it held for an abort is due now, and leaves the transaction.
	for _, held := range s.aborted {
		//: held inside the failed scope.
		if inside(held.scope) {
			undone = append(undone, held)
			continue
		}
		kept = append(kept, held)
	}
	clear(s.aborted[len(kept):])
	s.aborted = kept
	//: in the order they were held; runAborted reverses it.
	return undone
}

// drain closes the transaction to Defer and OnAbort and hands back what is
// due: the functions held for the commit when committed is true, the ones
// held for an abort otherwise. The other list is dropped.
func (s *txState) drain(committed bool) (held []heldFn, aborted []abortFn) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ended = true
	held, aborted = s.held, s.aborted
	s.held, s.aborted = nil, nil
	//: a rollback drops every function held for the commit.
	if !committed {
		//: what was held for an abort is due.
		return nil, aborted
	}
	//: in the order they were held; a commit drops what waited for an abort.
	return held, nil
}

// runAborted runs the functions held for an abort, newest first — the order
// in which work is undone —, each on its own detached context.
func runAborted(aborted []abortFn) {
	//: newest first.
	for _, held := range slices.Backward(aborted) {
		held.fn(held.ctx)
	}
}
