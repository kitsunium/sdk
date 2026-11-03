package singleflight

import (
	"context"
	"hash/maphash"
	"sync"
)

// initialCallsCapacity pre-sizes a shard's in-flight map on first use. It is a
// hint, not a bound: a Group is a deduplication window, and the number of
// DISTINCT keys in flight at one instant is small even when the call rate is
// not — that is the whole premise of the primitive.
const initialCallsCapacity int = 8

// shardCount is how many independent maps a Group spreads its keys over (a
// power of two, so a mask picks one): distinct keys contend only when they
// hash to the same shard, where one map under one lock serialised them all.
const shardCount int = 16

// cacheLine is the 64 bytes a shard is padded to, so two shards never share a
// cache line and one shard's lock traffic does not slow its neighbour's.
const cacheLine int = 64

// shardHeld is what a shard holds before its padding: a mutex and a map
// pointer, 16 bytes.
const shardHeld int = 16

// shardPad fills a shard to cacheLine, derived from cacheLine and shardHeld.
const shardPad int = cacheLine - shardHeld

// shardSeed keys the hash that picks a key's shard. One seed per process:
// drawn at start, so no input can be crafted to land every key in one shard.
var shardSeed = maphash.MakeSeed()

// shard is one of a Group's maps and the mutex guarding it, with every call's
// refs and retired.
type shard[K comparable, V any] struct {
	mu    sync.Mutex
	calls map[K]*call[V]
	_     [shardPad]byte
}

// Group deduplicates concurrent [Group.Do] calls that name the same key. The
// zero value is ready to use; a Group must not be copied after first use.
//
// Safe for concurrent use by any number of goroutines.
type Group[K comparable, V any] struct {
	//: a fixed array so the zero Group needs no constructor; each shard builds
	//: its map on first use.
	shards [shardCount]shard[K, V]
}

// shardOf returns the shard key lives in.
func (g *Group[K, V]) shardOf(key K) *shard[K, V] {
	//: the low bits of a seeded hash; shardCount is a power of two.
	return &g.shards[maphash.Comparable(shardSeed, key)&uint64(shardCount-1)]
}

// Do runs fn for key unless an identical key is already in flight, in which
// case it waits for that call instead of starting a second one. shared reports
// whether the result came from a call this goroutine did not start.
//
// fn does NOT receive ctx. It receives the shared call's context: the first
// caller's values with every deadline and cancellation removed, cancelled only
// once every caller has stopped waiting. See the package documentation.
//
// A ctx that is done while this goroutine is still waiting returns
// (zero, shared, ctx.Err()) and leaves the call running for whoever else is
// waiting. A result that is already available is delivered even if ctx is also
// done — cancellation preempts waiting, not delivery.
//
// If fn panics, Do panics with a [PanicValue] carrying the original value and
// fn's stack.
func (g *Group[K, V]) Do(ctx context.Context, key K, fn func(ctx context.Context) (V, error)) (value V, shared bool, err error) {
	held := g.shardOf(key)
	//: join the in-flight call for this key, or become the one that runs it.
	entry, joined := held.join(ctx, key, fn)
	//: wait for the result, for this caller's cancellation, or for a panic.
	return held.await(ctx, key, entry, joined)
}

// Forget retires key so the NEXT Do starts a fresh call. Callers already
// waiting on the in-flight call keep waiting and still receive its result —
// forgetting a key drops the dedup entry, it does not abandon the work.
//
// It exists for the case where the in-flight call is known to be answering a
// question that has since changed: without it, a caller arriving one
// microsecond later would be handed a result computed from stale inputs and
// would have no way to tell.
func (g *Group[K, V]) Forget(key K) {
	held := g.shardOf(key)
	held.mu.Lock()
	defer held.mu.Unlock()
	//: a key that names nothing is already forgotten.
	delete(held.calls, key)
}

// InFlight reports how many keys a [Group.Do] arriving now would JOIN rather
// than start — the deduplication entries currently registered. It is a
// point-in-time reading, useful for a gauge or a test — never for a decision,
// since the number can change between the read and the next statement.
//
// It is NOT a count of running calls. A key that was Forgotten, or whose every
// caller abandoned it, stops being counted while its fn may still be running —
// Forget drops the entry and not the work, and abandonment is a departure and
// not a kill — so a forgotten key that a later Do re-issued counts ONCE while
// two calls for it run.
func (g *Group[K, V]) InFlight() int {
	total := 0
	//: one shard at a time; each map holds exactly the keys a new Do would
	//: join there, and a retired call that is still running is in none.
	for at := range g.shards {
		held := &g.shards[at]
		held.mu.Lock()
		total += len(held.calls)
		held.mu.Unlock()
	}
	//: the sum, no single instant's under concurrent calls.
	return total
}

// join registers this goroutine as a waiter on key's call, starting one if
// none is in flight. joined reports whether the call was already running.
//
// The critical section holds the map and nothing else (#1035): a follower
// finds the call and counts itself in one acquisition; a leader looks, builds
// its call record and context with the lock released, and takes it again to
// insert — and a candidate that lost the key to another leader in between
// cancels the context it built and joins the winner instead.
func (s *shard[K, V]) join(ctx context.Context, key K, fn func(ctx context.Context) (V, error)) (entry *call[V], joined bool) {
	//: the follower's path, and the leader's first look.
	if existing := s.joinExisting(key); existing != nil {
		//: the caller learns its result was shared.
		return existing, true
	}
	//: the shared call keeps this caller's VALUES and none of its
	//: cancellation; the cancel below is the Group's own, fired when the last
	//: waiter leaves rather than when this one does. Built outside the lock.
	callCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	//: one waiter (this goroutine) and one goroutine about to run fn.
	candidate := &call[V]{done: make(chan struct{}), cancel: cancel, refs: 1}
	//: insert, unless another leader got there since the first look.
	if winner, inserted := s.insertOrJoin(key, candidate); !inserted {
		//: the candidate never runs: its context goes, the winner is joined.
		cancel()
		//: the caller learns its result was shared.
		return winner, true
	}
	//: fn runs on its own goroutine so no caller's departure can stop it; the
	//: goroutine starts with no lock held.
	go s.run(callCtx, key, candidate, fn)
	//: this goroutine started the call, so its result is not shared.
	return candidate, false
}

// joinExisting counts the caller in key's in-flight call and returns it, or
// returns nil when none is in flight.
func (s *shard[K, V]) joinExisting(key K) *call[V] {
	s.mu.Lock()
	defer s.mu.Unlock()
	existing, found := s.calls[key]
	//: nothing in flight: the caller leads.
	if !found {
		//: no call to join.
		return nil
	}
	//: one more waiter; the count is what keeps the call alive.
	existing.refs++
	//: the call joined.
	return existing
}

// insertOrJoin registers candidate under key and reports true, or — when a
// call for key is already in flight — counts the caller in that call and
// returns it with false. One acquisition decides both.
func (s *shard[K, V]) insertOrJoin(key K, candidate *call[V]) (winner *call[V], inserted bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	//: another leader inserted since this caller's first look: join it.
	if existing, found := s.calls[key]; found {
		existing.refs++
		//: the winner, joined.
		return existing, false
	}
	//: the map is built lazily so the zero Group is usable.
	if s.calls == nil {
		//: first call on this shard — see initialCallsCapacity for the hint.
		s.calls = make(map[K]*call[V], initialCallsCapacity)
	}
	s.calls[key] = candidate
	//: this caller leads.
	return candidate, true
}

// run executes fn, publishes its outcome on entry, and retires key. It runs on
// its own goroutine: that is what makes a caller's departure a departure
// rather than a kill.
func (s *shard[K, V]) run(ctx context.Context, key K, entry *call[V], fn func(ctx context.Context) (V, error)) {
	//: releases the context regardless of how fn ends; cancelling an already
	//: cancelled context is a no-op, so the abandonment path may beat us here.
	defer entry.cancel()
	//: publish exactly once, whether fn returned or panicked — a waiter
	//: blocked on a channel that never closes is the worst outcome available.
	defer s.publish(key, entry)
	//: a panic is captured here and re-raised in every waiter by await.
	defer entry.capturePanic()
	//: the single execution every caller of this key is sharing.
	entry.val, entry.err = fn(ctx)
}

// publish retires key (so the next Do starts fresh) and wakes every waiter.
// The map entry is dropped BEFORE done is closed so a goroutine arriving in
// between starts a new call instead of joining a finished one.
func (s *shard[K, V]) publish(key K, entry *call[V]) {
	s.mu.Lock()
	//: only retire the entry still under this key — Forget, or an abandoned
	//: predecessor, may already have replaced it.
	if s.calls[key] == entry {
		//: the call is over; the key is free again.
		delete(s.calls, key)
	}
	entry.retired = true
	s.mu.Unlock()
	//: every waiter wakes here, and the close is the happens-before edge that
	//: makes entry.val / entry.err / entry.panicked safe to read. The Once
	//: makes "published exactly once" a property of the TYPE rather than of
	//: run's defer discipline — a second close would panic, and a panicking
	//: publish is the one failure that would leave every waiter parked forever.
	entry.publishOnce.Do(func() { close(entry.done) })
}

// await blocks until the result lands or ctx is done.
//
// A served caller does NOT touch the shard's mutex: publish already retired the
// entry under it, and refs is read only to decide whether an ABANDONING caller
// was the last one — a decision that is moot once the call is retired. Skipping
// the bookkeeping keeps the deduplicated path at one mutex acquisition per
// caller (the join) instead of two.
func (s *shard[K, V]) await(ctx context.Context, key K, entry *call[V], shared bool) (value V, isShared bool, err error) {
	select {
	case <-entry.done:
		//: a panic in fn is re-raised here so no waiter returns a zero value
		//: that looks like a legitimate answer.
		entry.repanic()
		//: the single shared outcome.
		return entry.val, shared, entry.err
	case <-ctx.Done():
		//: select picks at random among ready cases, so a result that landed
		//: in the same instant would be discarded on a coin flip. Re-check it:
		//: cancellation preempts WAITING, not DELIVERY.
		select {
		case <-entry.done:
			entry.repanic()
			//: the single shared outcome, delivered despite the cancellation.
			return entry.val, shared, entry.err
		default:
		}
		//: this caller gives up; the call survives unless it was the last one.
		s.abandon(key, entry)
		var zero V
		//: the caller's own cancellation, never another caller's.
		return zero, shared, ctx.Err()
	}
}

// abandon drops the claim of a caller that gave up. When the LAST such caller
// leaves, the key is retired and the shared context is cancelled: a call whose
// every caller has gone is computing a result nobody will read.
func (s *shard[K, V]) abandon(key K, entry *call[V]) {
	s.mu.Lock()
	entry.refs--
	//: retire under the lock so a newcomer cannot join a call that is about to
	//: be cancelled — it would receive a cancellation it never asked for.
	orphaned := entry.refs == 0 && !entry.retired
	//: only the LAST caller out retires the key; every earlier departure just
	//: decrements, which is what keeps the call alive for whoever is left.
	if orphaned {
		//: nobody is waiting; the key is free for the next caller.
		entry.retired = true
		//: only remove the entry still under this key — Forget may have
		//: replaced it already.
		if s.calls[key] == entry {
			//: the next Do on this key starts a new call.
			delete(s.calls, key)
		}
	}
	s.mu.Unlock()
	//: cancel outside the lock; fn may observe it and return immediately.
	if orphaned {
		//: the audience is empty — stop paying for the answer.
		entry.cancel()
	}
}
