package app

import (
	"context"
	"fmt"
	"sync"
)

// loopReads are what the engine's loop read of a workflow's local store
// outside any transaction: per key, the count of the store's writes back
// that entity's read compares (undoneCounts) as the read began. A read is
// forgotten by the replacement that follows it, by the entity's deletion
// (forget) and when the workflow stops: what it holds is bounded by the
// entities the store holds. On the data directory and in memory a
// transaction's writes land before its commit: the loop could check a guard
// on one, and its transition, waiting for the writer turn, wrote the entity
// back after a rollback had undone that write — the rolled back write stood.
// The replacement that follows the read compares the count once it holds the
// turn. Start and Fire take the turn before the engine reads, and their reads
// are not remembered.
type loopReads struct {
	mu sync.Mutex
	// byKey maps an entity's key to the count its read saw.
	byKey map[string]uint64
}

// remember records a read of key the loop makes outside any transaction, and
// forgets the one a transaction's read supersedes. A store on a database, or
// one apart, has no local transaction to read into.
func (r *loopReads) remember(ctx context.Context, s *storeCore, key string) {
	a := s.app()
	if a == nil || !s.local() || s.apart() {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if unitOf(ctx) != nil {
		delete(r.byKey, key)
		return
	}
	if r.byKey == nil {
		r.byKey = map[string]uint64{}
	}
	r.byKey[key] = a.undone.seen(s.id, key)
}

// clear forgets every read: the workflow stopped, and its next run reads
// anew.
func (r *loopReads) clear() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.byKey = nil
}

// take returns, and forgets, the count the loop's read of key saw; false when
// no read of the loop's is remembered.
func (r *loopReads) take(key string) (uint64, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	seen, ok := r.byKey[key]
	delete(r.byKey, key)
	return seen, ok
}

// check refuses the replacement of key when the loop decided it on a read a
// rollback of another transaction has since undone: once the caller's
// transaction holds the writer turn, no write it could have read is pending,
// and a count past the read's says one was written back. The refusal is
// Unavailable: the loop tries the entity again after its backoff, on what
// the store holds.
func (r *loopReads) check(ctx context.Context, s *storeCore, key string) error {
	seen, ok := r.take(key)
	a := s.app()
	if !ok || a == nil {
		return nil
	}
	ctx, release, err := s.turn(ctx)
	defer release()
	if err != nil {
		return err
	}
	undone := a.undone.seen(s.id, key) - seen
	if u := unitOf(ctx); u != nil {
		undone -= u.tx.undidOf(s.id, key)
	}
	if undone > 0 {
		return Unavailable(fmt.Sprintf("%s: the entity %q was read while a transaction that rolled back wrote it: its transition is decided again",
			s.name, clip(key)))
	}
	return nil
}
