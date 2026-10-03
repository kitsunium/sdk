// Package statemachine — hosts the per-entity locks that serialise the
// transitions of one entity, and the context mark that tells a hook's own
// machine apart.
package statemachine

import (
	"context"
	"errors"
	"sync"

	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// keyLock serialises the transitions of one entity. It is a one-token
// channel rather than a mutex so a caller whose context ends stops waiting.
type keyLock struct {
	// gate holds a token while the lock is held.
	gate chan struct{}
	// refs counts the holders and waiters; guarded by keyLocks.mu.
	refs int
}

// keyLocks hands out one lock per key and forgets a key nobody holds or waits
// for, so the table stays as large as the entities in flight.
type keyLocks struct {
	byKey map[string]*keyLock
	mu    sync.Mutex
}

// newKeyLocks returns an empty table.
func newKeyLocks() *keyLocks {
	//: filled as entities are held.
	return &keyLocks{byKey: make(map[string]*keyLock)}
}

// acquire takes key's lock, waiting until it is free or ctx ends, and returns
// the function that releases it. A context that ends first is [WaitAbandoned].
//
// The release is idempotent — a second call does nothing — so a caller can
// defer it for the panicking path and still hand it over to be called early:
// a transition releases the lock before its OnTransition hooks run.
func (l *keyLocks) acquire(ctx context.Context, key string) (func(), error) {
	//: an ended context does not take a lock it would have to give back.
	if err := ctx.Err(); err != nil {
		//: nothing was taken.
		return nil, abandoned(err)
	}
	l.mu.Lock()
	lock := l.byKey[key]
	//: the first holder or waiter of this key.
	if lock == nil {
		lock = &keyLock{gate: make(chan struct{}, 1)}
		l.byKey[key] = lock
	}
	lock.refs++
	l.mu.Unlock()
	//: the token is the lock; the context is the way out of the wait.
	select {
	//: taken.
	case lock.gate <- struct{}{}:
		released := false
		//: the release gives the token back and drops the reference, once;
		//: it is only ever called on the goroutine that took the lock.
		return func() {
			//: a second call — the deferred one after an early release.
			if released {
				//: already given back.
				return
			}
			released = true
			<-lock.gate
			l.drop(key, lock)
		}, nil
	//: the caller stopped waiting.
	case <-ctx.Done():
		l.drop(key, lock)
		//: nothing was taken.
		return nil, abandoned(ctx.Err())
	}
}

// drop removes one reference to key's lock, forgetting it with the last.
func (l *keyLocks) drop(key string, lock *keyLock) {
	l.mu.Lock()
	defer l.mu.Unlock()
	lock.refs--
	//: nobody holds it or waits for it any more.
	if lock.refs == 0 {
		delete(l.byKey, key)
	}
}

// abandoned is [WaitAbandoned] joined with the context's error, so the
// verdict keeps its 503 and errors.Is still finds context.Canceled or
// context.DeadlineExceeded.
func abandoned(cause error) error {
	//: verdict first, cause second — the order errors.Join renders them in.
	return errors.Join(errs.Wrap(WaitAbandoned, errs.WrapParams{}), cause)
}

// hookScope marks a context handed to an OnEnter hook with the lock table of
// the machine running it — the table a nested transition would wait on;
// parent links the machines running hooks further up the call.
type hookScope struct {
	locks  *keyLocks
	parent *hookScope
}

// hookScopeKey is the context key of the mark.
type hookScopeKey struct{}

// within returns ctx marked as running an OnEnter hook of the machine whose
// lock table is locks.
func within(ctx context.Context, locks *keyLocks) context.Context {
	parent, _ := ctx.Value(hookScopeKey{}).(*hookScope)
	//: one link more; the chain says every machine up the call.
	return context.WithValue(ctx, hookScopeKey{}, &hookScope{locks: locks, parent: parent})
}

// inside reports whether ctx was handed to an OnEnter hook of the machine
// whose lock table is locks, however far up the call.
func inside(ctx context.Context, locks *keyLocks) bool {
	scope, _ := ctx.Value(hookScopeKey{}).(*hookScope)
	//: walk the chain of machines running hooks.
	for ; scope != nil; scope = scope.parent {
		//: this machine holds an entity's lock further up the call.
		if scope.locks == locks {
			//: a transition would wait for itself.
			return true
		}
	}
	//: no hook of this machine is running up the call.
	return false
}
