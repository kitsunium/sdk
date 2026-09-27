// Package sql — hosts the per-transaction state every scope of one
// transaction shares, and the context chain that finds it.
package sql

import (
	"slices"
	"sync"

	stdsql "database/sql"

	coresql "github.com/kitsunium/sdk/internal/core/sql"
)

// heldFn is one function held until the commit, tagged with the scope it was
// held in.
type heldFn struct {
	// fn runs after the commit.
	fn func()
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

// dropFrom drops every function held in the scope a failed savepoint opened,
// or in a scope opened inside it.
//
// Scopes are a stack and their ids grow with every savepoint, so while the
// scope numbered from is unwinding, everything held with an id at or above it
// was held inside it. A savepoint that is released drops nothing: what it held
// stays, and now belongs to the scope around it.
func (s *txState) dropFrom(from uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	//: what the failed scope held goes with its work.
	s.held = slices.DeleteFunc(s.held, func(h heldFn) bool { return h.scope >= from })
}

// drain closes the transaction to Defer and hands back what was held: to run
// when committed is true, and dropped otherwise.
func (s *txState) drain(committed bool) []heldFn {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ended = true
	held := s.held
	s.held = nil
	//: a rollback drops every held function.
	if !committed {
		//: nothing to run.
		return nil
	}
	//: in the order they were held.
	return held
}
