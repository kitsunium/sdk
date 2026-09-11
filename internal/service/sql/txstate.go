// Package sql — hosts the per-transaction state every scope of one
// transaction shares, and the context chain that finds it.
package sql

import (
	"sync"

	stdsql "database/sql"

	coresql "github.com/kitsunium/sdk/internal/core/sql"
)

// txState is the state ONE transaction shares across its root scope and every
// savepoint nested inside it.
//
// It is reachable only through the context chain, and only by the transactor
// that created it, so two managers over two different databases never see each
// other's transactions.
type txState struct {
	// mu serialises the savepoint counter and the poison flag. The
	// underlying *sql.Tx is itself safe for concurrent use, but the counter
	// must not hand two goroutines the same savepoint name.
	//
	// RWMutex rather than Mutex because the poison flag is READ on every
	// statement (executor.usable) and written at most once per transaction:
	// the read side is the hot one, and it is the one that must not serialise
	// two goroutines sharing a transaction.
	mu sync.RWMutex
	// tx is the single database transaction every scope runs on.
	tx *stdsql.Tx
	// dialect selects the savepoint grammar.
	dialect coresql.Dialect
	// counter numbers savepoints and NEVER resets inside one transaction, so
	// a name is never reused — see savepointName for why reuse is a trap.
	counter uint64
	// poisoned is non-nil once the transaction can no longer be trusted:
	// a savepoint rollback failed, so what the engine has kept is unknown.
	poisoned error
}

// nextSavepoint reserves the next savepoint name for this transaction.
func (s *txState) nextSavepoint() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	//: monotonic and never reset — reuse would shadow rather than replace.
	s.counter++
	//: the reserved name belongs to the caller until the transaction ends.
	return savepointName(s.counter)
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
