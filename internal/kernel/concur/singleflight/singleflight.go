package singleflight

import (
	"context"
	"hash/maphash"
	"sync"
	"sync/atomic"
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

// shardHeld is what a shard's first cache line holds before its padding: a
// mutex, a map pointer and the head of its queue of retired calls, 24 bytes.
const shardHeld int = 24

// shardPad fills a shard's first cache line, derived from cacheLine and
// shardHeld.
const shardPad int = cacheLine - shardHeld

// hintHeld is what a shard's second cache line holds: the hot key's hash, 8
// bytes.
const hintHeld int = 8

// hintPad fills a shard's second cache line, derived from cacheLine and
// hintHeld.
const hintPad int = cacheLine - hintHeld

// shardSeed keys the hash that picks a key's shard. One seed per process:
// drawn at start, so no input can be crafted to land every key in one shard.
var shardSeed = maphash.MakeSeed()

// shard is one of a Group's maps and the mutex guarding it, with every call's
// refs, and the queue of retired calls whose entry is still in the map.
type shard[K comparable, V any] struct {
	mu    sync.Mutex
	calls map[K]*call[K, V]
	//: the calls publish retired while another goroutine held mu, linked by
	//: their next field: whoever holds mu removes their entries before
	//: releasing it (see unlock), so publish never waits for the lock.
	retired atomic.Pointer[call[K, V]]
	_       [shardPad]byte
	//: the hash of the latest key whose call gained a follower: read without
	//: the lock, it tells a caller whether a first look may find its key in
	//: flight. A number, so it keeps no call alive; on a line of its own,
	//: written only when the hot key changes, so the callers of keys nobody
	//: shares read it from their own cache rather than from the line the
	//: mutex moves between them.
	hotHash atomic.Uint64
	_       [hintPad]byte
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

// hashOf returns key's seeded hash: its low bits pick its shard, and the
// whole of it is compared with the shard's hot key.
func hashOf[K comparable](key K) uint64 {
	//: one seed per process, see shardSeed.
	return maphash.Comparable(shardSeed, key)
}

// shardAt returns the shard a key of hash lives in.
func (g *Group[K, V]) shardAt(hash uint64) *shard[K, V] {
	//: the low bits of the hash; shardCount is a power of two.
	return &g.shards[hash&uint64(shardCount-1)]
}

// shardOf returns the shard key lives in.
func (g *Group[K, V]) shardOf(key K) *shard[K, V] {
	//: the shard of the key's hash.
	return g.shardAt(hashOf(key))
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
	//: hashed here rather than through hashOf, which does not inline.
	hash := maphash.Comparable(shardSeed, key)
	held := g.shardAt(hash)
	//: join the in-flight call for this key, or become the one that runs it.
	entry, joined := held.join(ctx, key, hash, fn)
	//: wait for the result, for this caller's cancellation, or for a panic.
	return held.await(ctx, entry, joined)
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
	defer held.unlock()
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
	//: one shard at a time; a retired call whose entry is still in the map
	//: is one a new Do would not join, so it is not counted.
	for at := range g.shards {
		total += g.shards[at].live()
	}
	//: the sum, no single instant's under concurrent calls.
	return total
}

// live counts the shard's calls a new Do would join: its entries, less the
// retired ones still waiting for their removal.
func (s *shard[K, V]) live() int {
	s.mu.Lock()
	defer s.unlock()
	count := 0
	//: a retired call is absent to a newcomer, so it is not counted.
	for _, entry := range s.calls {
		//: only a call still answering newcomers counts.
		if !entry.retired.Load() {
			count++
		}
	}
	//: the shard's share of InFlight.
	return count
}

// join registers this goroutine as a waiter on key's call, starting one if
// none is in flight. joined reports whether the call was already running.
//
// The critical section holds the map and nothing else (#1035). A caller whose
// key hashes as the shard's hot key — the latest whose call gained a follower
// — looks first and joins the call in flight in that one acquisition,
// building nothing. Every other caller builds a candidate, the call record
// and its context, with the lock released, and one acquisition either
// inserts it or counts the caller in the call that got there first. Only the
// leader of a hot key, whose first look found nothing, takes the lock twice.
// hash is key's hashOf.
func (s *shard[K, V]) join(ctx context.Context, key K, hash uint64, fn func(ctx context.Context) (V, error)) (entry *call[K, V], joined bool) {
	//: the follower's path: a hot key, likely in flight.
	if s.hotHash.Load() == hash {
		//: found under the lock: joined, with nothing built.
		if existing := s.joinExisting(key, hash); existing != nil {
			//: the caller learns its result was shared.
			return existing, true
		}
	}
	candidate, callCtx := newCandidate[K, V](ctx, key)
	//: lead with the candidate, or join the call already in flight.
	return s.settle(callCtx, candidate, hash, fn)
}

// joinExisting counts the caller in key's in-flight call and returns it, or
// returns nil when none is in flight.
func (s *shard[K, V]) joinExisting(key K, hash uint64) *call[K, V] {
	s.mu.Lock()
	defer s.unlock()
	existing, found := s.calls[key]
	//: nothing in flight: the caller leads.
	if !found || existing.retired.Load() {
		//: no call to join.
		return nil
	}
	s.follow(existing, hash)
	//: the call joined.
	return existing
}

// follow counts one more waiter in entry, with s.mu held, and makes its key,
// of hash hash, the shard's hot key: a key whose call gained a follower is
// likely to gain more, and the next ones then look before building anything.
func (s *shard[K, V]) follow(entry *call[K, V], hash uint64) {
	//: one more waiter; the count is what keeps the call alive.
	entry.refs++
	//: written when the hot key changes, not at every follower.
	if s.hotHash.Load() != hash {
		s.hotHash.Store(hash)
	}
}

// newCandidate builds, with no lock held, the call a caller of key leads if
// no call for key is in flight, and the context its fn would run under.
func newCandidate[K comparable, V any](ctx context.Context, key K) (candidate *call[K, V], callCtx context.Context) {
	//: a nil context is refused with the very panic context.WithoutCancel
	//: raised here before #813.
	if ctx == nil {
		panic("cannot create context from nil parent")
	}
	//: one waiter (this goroutine) and one goroutine about to run fn; the
	//: record carries the root of the shared context — this caller's VALUES
	//: and none of its cancellation — one allocation fewer than
	//: context.WithoutCancel (#813).
	candidate = &call[K, V]{base: detached{parent: ctx}, done: make(chan struct{}), refs: 1, key: key}
	//: the cancel is the Group's own, fired when the last waiter leaves rather
	//: than when this one does.
	callCtx, candidate.cancel = context.WithCancel(&candidate.base)
	//: the candidate and the context its fn would run under.
	return candidate, callCtx
}

// settle inserts candidate under its key and starts its fn under callCtx, or
// joins the call already in flight for that key — one acquisition decides. A
// candidate that lost the key cancels the context it built and joins the
// winner: its fn never runs. hash is the key's hashOf.
//
// Goroutine lifecycle: a winning candidate's fn runs on a goroutine started
// here, with no lock held, and that goroutine ends when fn returns or panics,
// once run has published the outcome and cancelled callCtx.
func (s *shard[K, V]) settle(callCtx context.Context, candidate *call[K, V], hash uint64, fn func(ctx context.Context) (V, error)) (entry *call[K, V], joined bool) {
	//: insert, or join the call already in flight.
	if winner, inserted := s.insertOrJoin(candidate, hash); !inserted {
		//: the candidate never runs: its context goes, the winner is joined.
		candidate.cancel()
		//: the caller learns its result was shared.
		return winner, true
	}
	//: fn runs on its own goroutine so no caller's departure can stop it; the
	//: goroutine starts with no lock held.
	go s.run(callCtx, candidate, fn)
	//: this goroutine started the call, so its result is not shared.
	return candidate, false
}

// insertOrJoin registers candidate under key and reports true, or — when a
// call for key is already in flight — counts the caller in that call and
// returns it with false. One acquisition decides both. A retired call still
// in the map is not in flight: the candidate takes its place, so a newcomer
// starts a new call rather than joining a finished one.
func (s *shard[K, V]) insertOrJoin(candidate *call[K, V], hash uint64) (winner *call[K, V], inserted bool) {
	key := candidate.key
	s.mu.Lock()
	defer s.unlock()
	//: a call in flight for key: join it.
	if existing, found := s.calls[key]; found && !existing.retired.Load() {
		s.follow(existing, hash)
		//: the winner, joined.
		return existing, false
	}
	//: the map is built lazily so the zero Group is usable.
	if s.calls == nil {
		//: first call on this shard — see initialCallsCapacity for the hint.
		s.calls = make(map[K]*call[K, V], initialCallsCapacity)
	}
	s.calls[key] = candidate
	//: this caller leads.
	return candidate, true
}

// unlock releases s.mu, first removing the entries of the calls publish
// queued while it was held. A publish that queued its call after the drain
// found the lock taken, so the call is drained by whoever releases the lock
// after it — this goroutine, which looks once more once it has released it,
// or the goroutine that took the lock in between.
func (s *shard[K, V]) unlock() {
	//: drain and release until no call is left queued, or another goroutine
	//: holds the lock and will drain it itself.
	for {
		s.drainRetired()
		s.mu.Unlock()
		//: nothing queued since the drain, or a holder that will see it.
		if s.retired.Load() == nil || !s.mu.TryLock() {
			//: every queued call is some holder's to drain.
			return
		}
	}
}

// drainRetired removes, with s.mu held, the map entry of every call queued
// on s.retired whose key it still holds.
func (s *shard[K, V]) drainRetired() {
	//: the common case: nothing queued, and no atomic write.
	if s.retired.Load() == nil {
		//: nothing to remove.
		return
	}
	//: take the whole queue at once; a call queued from now on is the next
	//: holder's.
	for queued := s.retired.Swap(nil); queued != nil; {
		next := queued.next
		//: unlinked, so a drained call keeps no other alive.
		queued.next = nil
		s.remove(queued)
		queued = next
	}
}

// remove deletes entry's map entry, with s.mu held, unless its key now
// belongs to another call — Forget, an abandoned predecessor's successor or a
// newcomer that found it retired may have replaced it.
func (s *shard[K, V]) remove(entry *call[K, V]) {
	//: only the entry still under its key is this call's.
	if s.calls[entry.key] == entry {
		//: the next Do on this key starts a new call.
		delete(s.calls, entry.key)
	}
}

// run executes fn, publishes its outcome on entry, and retires its key. It
// runs on its own goroutine: that is what makes a caller's departure a
// departure rather than a kill.
func (s *shard[K, V]) run(ctx context.Context, entry *call[K, V], fn func(ctx context.Context) (V, error)) {
	//: releases the context regardless of how fn ends; cancelling an already
	//: cancelled context is a no-op, so the abandonment path may beat us here.
	defer entry.cancel()
	//: publish exactly once, whether fn returned or panicked — a waiter
	//: blocked on a channel that never closes is the worst outcome available.
	defer s.publish(entry)
	//: a panic is captured here and re-raised in every waiter by await.
	defer entry.capturePanic()
	//: the single execution every caller of this key is sharing.
	entry.val, entry.err = fn(ctx)
}

// publish retires the call (so the next Do on its key starts fresh) and wakes
// every waiter. The call is retired BEFORE done is closed so a goroutine
// arriving in between starts a new call instead of joining a finished one.
//
// It never waits for the shard's mutex (#1035): retired is an atomic, which a
// newcomer reads as the key's absence, and the map entry is removed now when
// the lock is free, or by the goroutine holding it otherwise.
func (s *shard[K, V]) publish(entry *call[K, V]) {
	//: the last abandonment may have retired the call, and removed its entry,
	//: already.
	if entry.retired.CompareAndSwap(false, true) {
		s.release(entry)
	}
	//: every waiter wakes here, and the close is the happens-before edge that
	//: makes entry.val / entry.err / entry.panicked safe to read. The Once
	//: makes "published exactly once" a property of the TYPE rather than of
	//: run's defer discipline — a second close would panic, and a panicking
	//: publish is the one failure that would leave every waiter parked forever.
	entry.publishOnce.Do(func() { close(entry.done) })
}

// release removes a retired call's map entry without waiting for s.mu: at
// once when the lock is free, else by queueing the call for the goroutine
// holding it.
func (s *shard[K, V]) release(entry *call[K, V]) {
	//: the lock is free: remove the entry now.
	if s.mu.TryLock() {
		s.remove(entry)
		s.unlock()
		//: removed.
		return
	}
	//: the lock is held: its holder removes the entry on its way out.
	for {
		head := s.retired.Load()
		entry.next = head
		//: queued, unless another call was queued in between.
		if s.retired.CompareAndSwap(head, entry) {
			break
		}
	}
	//: the holder that refused the TryLock may have left before the call was
	//: queued, having looked at the queue already: if the lock is free now,
	//: take it and drain the queue here rather than leave the call waiting
	//: for the shard's next caller.
	if s.mu.TryLock() {
		s.unlock()
	}
}

// await blocks until the result lands or ctx is done.
//
// A served caller does NOT touch the shard's mutex: publish already retired the
// entry under it, and refs is read only to decide whether an ABANDONING caller
// was the last one — a decision that is moot once the call is retired. Skipping
// the bookkeeping keeps the deduplicated path at one mutex acquisition per
// caller (the join) instead of two.
func (s *shard[K, V]) await(ctx context.Context, entry *call[K, V], shared bool) (value V, isShared bool, err error) {
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
		s.abandon(entry)
		var zero V
		//: the caller's own cancellation, never another caller's.
		return zero, shared, ctx.Err()
	}
}

// abandon drops the claim of a caller that gave up. When the LAST such caller
// leaves, the key is retired and the shared context is cancelled: a call whose
// every caller has gone is computing a result nobody will read.
func (s *shard[K, V]) abandon(entry *call[K, V]) {
	s.mu.Lock()
	entry.refs--
	//: retire under the lock so a newcomer cannot join a call that is about to
	//: be cancelled — it would receive a cancellation it never asked for. Only
	//: the LAST caller out retires the key, and only if publish has not:
	//: every earlier departure just decrements, which is what keeps the call
	//: alive for whoever is left.
	orphaned := entry.refs == 0 && entry.retired.CompareAndSwap(false, true)
	//: nobody is waiting; the key is free for the next caller.
	if orphaned {
		s.remove(entry)
	}
	s.unlock()
	//: cancel outside the lock; fn may observe it and return immediately.
	if orphaned {
		//: the audience is empty — stop paying for the answer.
		entry.cancel()
	}
}
