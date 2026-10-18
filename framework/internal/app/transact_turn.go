package app

import (
	"context"
	"sync"
	"sync/atomic"
	"time"
)

// The data's writer turn (ADR 0004). The data directory and memory have no
// transactions: kit keeps its own, and a transaction there takes the turn
// alone, at its first write, until it ends. A write outside any transaction
// shares the turn with the others like it — they run at once, as they
// always did — and waits while a transaction holds it, so that a rollback
// never writes back over a write that came after it.
//
// The turn is the outermost lock of a write: taken before kit's own — a
// store's hold lock, the document store's writers' —, so that a transaction
// that waits for one of them never waits for a write that waits for it. A
// wait is bounded: past turnWait the write is refused as Unavailable rather
// than left waiting for a transaction that waits for it — a lock of the
// product's, a workflow's entity its own loop is transitioning.

// turnWait bounds how long a write waits for the writer turn, on the wall
// clock: a test's manual clock would hold it forever.
const turnWait time.Duration = 10 * time.Second

const (
	// undoneSlots is how many slots undoneCounts spreads a store's entities
	// over: a write back counts in its entity's slot, so a read of another
	// entity is told of it only when the two keys share one.
	undoneSlots int = 64
	// fnvOffset is 32-bit FNV-1a's offset basis, which slotOf hashes a key
	// from.
	fnvOffset uint32 = 2166136261
	// fnvPrime is 32-bit FNV-1a's prime.
	fnvPrime uint32 = 16777619
)

// writerTurn is the data's writer turn in one app: held by one transaction
// alone, or shared by the writes of none.
type writerTurn struct {
	mu sync.Mutex
	// shared counts the writes of no transaction that hold it; alone is set
	// while a transaction does. waiting counts the transactions that wait
	// for it: a new shared hold waits behind them, so a steady stream of
	// writes never keeps a transaction out.
	shared  int
	alone   bool
	waiting int
	// changed is fired, and replaced, whenever the turn is given back.
	changed *turnSignal
}

// turnSignal tells the waiters of a writer turn that it changed: its
// channel is closed once, however many give the turn back.
type turnSignal struct {
	ch   chan struct{}
	once sync.Once
}

// fire closes the signal's channel, once.
func (s *turnSignal) fire() { s.once.Do(func() { close(s.ch) }) }

// take waits for the turn — alone for a transaction, shared for a write of
// none — within ctx and turnWait, and returns what gives it back.
func (t *writerTurn) take(ctx context.Context, alone bool) (release func(), err error) {
	// The timer is made at the first wait: a write the turn lets through at
	// once pays one lock.
	var timeout <-chan time.Time
	t.mu.Lock()
	if alone {
		t.waiting++
	}
	for {
		if release, ok := t.tryTake(alone); ok {
			t.mu.Unlock()
			return release, nil
		}
		ch := t.signal()
		t.mu.Unlock()
		if timeout == nil {
			timer := time.NewTimer(turnWait)
			defer timer.Stop()
			timeout = timer.C
		}
		if err := t.await(ctx, ch, timeout, alone); err != nil {
			return nil, err
		}
		t.mu.Lock()
	}
}

// tryTake takes the turn when it is free for the caller — alone when no
// write holds it, shared when no transaction holds it or waits for it — and
// returns what gives it back. The caller holds t.mu.
func (t *writerTurn) tryTake(alone bool) (func(), bool) {
	switch {
	case alone && !t.alone && t.shared == 0:
		t.waiting--
		t.alone = true
		return t.giveBack(true), true
	case !alone && !t.alone && t.waiting == 0:
		t.shared++
		return t.giveBack(false), true
	}
	return nil, false
}

// signal is the channel the turn's next change closes. The caller holds
// t.mu.
func (t *writerTurn) signal() <-chan struct{} {
	if t.changed == nil {
		t.changed = &turnSignal{ch: make(chan struct{})}
	}
	return t.changed.ch
}

// await waits for ch, the turn's next change: false with why when ctx ends
// or the wait lasts past timeout, the caller no longer waiting.
func (t *writerTurn) await(ctx context.Context, ch <-chan struct{}, timeout <-chan time.Time, alone bool) error {
	select {
	case <-ch:
		return nil
	case <-ctx.Done():
		t.abandon(alone)
		return ctx.Err()
	case <-timeout:
		t.abandon(alone)
		return Unavailable("the data's writer turn is held by a transaction: a write waited for it for 10s")
	}
}

// abandon stops waiting: a transaction no longer holds the shared writes
// back.
func (t *writerTurn) abandon(alone bool) {
	if !alone {
		return
	}
	t.mu.Lock()
	t.waiting--
	t.wake()
	t.mu.Unlock()
}

// giveBack is what gives the turn back, once.
func (t *writerTurn) giveBack(alone bool) func() {
	var once sync.Once
	return func() {
		once.Do(func() {
			t.mu.Lock()
			if alone {
				t.alone = false
			} else {
				t.shared--
			}
			t.wake()
			t.mu.Unlock()
		})
	}
}

// wake tells every waiter the turn changed. The caller holds t.mu.
func (t *writerTurn) wake() {
	if t.changed != nil {
		t.changed.fire()
		t.changed = nil
	}
}

// sharesTurn marks a context whose write already shares the turn: the
// writes it makes on the way — its history, its journal — do not take it
// again, which would wait behind a transaction waiting for this very write.
type sharesTurn struct{}

// undoneSlot is one count of undoneCounts: a store, and the slot of an
// entity's key, 0 for a write back whose entity the funnel did not name.
type undoneSlot struct {
	store string
	slot  int
}

// slotOf is the slot of key: FNV-1a over its bytes, never 0, which counts
// for every entity.
func slotOf(key string) int {
	h := fnvOffset
	for i := range len(key) {
		h ^= uint32(key[i])
		h *= fnvPrime
	}
	return 1 + int(h%uint32(undoneSlots))
}

// slotFor is the slot a write back of key counts in: 0 when key is "".
func slotFor(key string) int {
	if key == "" {
		return 0
	}
	return slotOf(key)
}

// undoneCounts counts, per store and slot of an entity's key, the writes the
// data's rollbacks wrote back. A transaction's writes land before its
// commit, so a read outside it may see one a rollback then undoes: a reader
// that writes what it read compares the count of its entity across its read
// and its write (workflow_reads.go). The slots bound what it keeps, whatever
// the number of entities.
type undoneCounts struct {
	mu sync.Mutex
	// byStore maps a store's id to its counts, made at its first write back.
	byStore map[string]*[undoneSlots + 1]atomic.Uint64
}

// counts are the counts of the store id; nil before its first write back,
// unless create makes them.
func (u *undoneCounts) counts(store string, create bool) *[undoneSlots + 1]atomic.Uint64 {
	u.mu.Lock()
	defer u.mu.Unlock()
	c := u.byStore[store]
	if c == nil && create {
		if u.byStore == nil {
			u.byStore = map[string]*[undoneSlots + 1]atomic.Uint64{}
		}
		c = new([undoneSlots + 1]atomic.Uint64)
		u.byStore[store] = c
	}
	return c
}

// seen is the count a read of key in store compares: the writes back of its
// slot, and those whose entity was not named.
func (u *undoneCounts) seen(store, key string) uint64 {
	c := u.counts(store, false)
	if c == nil {
		return 0
	}
	return c[0].Load() + c[slotOf(key)].Load()
}

// counted records that the transaction's rollback wrote a write of key in
// store back: in a's counts, after the write back, so that a read that saw
// the count first may have seen the write, and one that saw it after did
// not; and in the transaction's own.
func (t *txn) counted(a *App, store, key string) {
	if a == nil {
		return
	}
	slot := slotFor(key)
	a.undone.counts(store, true)[slot].Add(1)
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.undid == nil {
		t.undid = map[undoneSlot]uint64{}
	}
	t.undid[undoneSlot{store: store, slot: slot}]++
}

// undidOf is how many of the writes back a read of key in store compares the
// transaction's own rollbacks made.
func (t *txn) undidOf(store, key string) uint64 {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.undid[undoneSlot{store: store}] + t.undid[undoneSlot{store: store, slot: slotOf(key)}]
}

// undoKeyOf carries, to the engine's call of plug.Transaction.Undo, the
// entity a funnel writes.
type undoKeyOf struct{}

// undoKeyed is ctx for a funnel's write of key: in a local transaction,
// marked with the key, which the write's undo step keeps (frameworkTransaction);
// unchanged elsewhere, where nothing is undone.
func undoKeyed(ctx context.Context, key string) context.Context {
	if !inLocal(ctx) {
		return ctx
	}
	return context.WithValue(ctx, undoKeyOf{}, key)
}

// writeTurn is what a local write of the store needs before it runs: in a
// transaction, the turn for the transaction — at its first write there —;
// outside any, the turn shared for the write's length. A store on a
// database, and a store apart — a cache (InMemory), kit's data keys — need
// none. It returns the context the write runs with, and what ends its share.
func (a *App) writeTurn(ctx context.Context, local bool, store string) (context.Context, func(), error) {
	if !local {
		return ctx, func() {}, nil
	}
	if u := unitOf(ctx); u != nil {
		return ctx, func() {}, u.claimLocal(ctx, a, store)
	}
	if ctx.Value(sharesTurn{}) != nil {
		return ctx, func() {}, nil
	}
	release, err := a.turn.take(ctx, false)
	if err != nil {
		return ctx, func() {}, err
	}
	return context.WithValue(ctx, sharesTurn{}, true), release, nil
}
