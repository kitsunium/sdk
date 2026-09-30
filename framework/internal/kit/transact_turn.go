// Package kit — the data's writer turn, where there are no transactions.
package kit

import (
	"context"
	"sync"
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
