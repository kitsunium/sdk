// Package singleflight deduplicates concurrent work that names the same key:
// of N goroutines asking for the same thing at the same time, exactly one does
// the work and all N receive its result. It is a kernel primitive — stdlib
// only, fully generic, and free of domain vocabulary (Group, Do, Forget; no
// Cache, no Request, no Key that means something).
//
// # The shared call outlives any single caller
//
// The obvious implementation runs fn on the first caller's goroutine with the
// first caller's context. It has a defect that only shows up under load: the
// caller who arrived first is also the one most likely to give up first — it
// has been waiting longest — and when it does, every other caller inherits its
// cancellation. One abandoned request fails ten that were still willing to
// wait.
//
// So fn runs on a goroutine of its own, under a context derived from the first
// caller's with cancellation removed (context.WithoutCancel), and that context
// is cancelled only when the LAST caller has left. A caller that abandons
// stops waiting and gets its own ctx.Err(); the call keeps running for
// everyone else. When nobody is left, the work stops — nothing is computed for
// an audience of zero.
//
// Two consequences are worth stating rather than discovering:
//
//   - The shared call inherits the FIRST caller's context VALUES, and no
//     caller's deadline. A follower that expected its own request-scoped
//     values (a trace id, a tenant) will not see them; it sees the leader's.
//     Deduplication means one execution, and one execution can carry only one
//     set of values.
//   - Because fn runs on its own goroutine, a Do that has returned has not
//     necessarily stopped fn. Abandonment is a departure, not a kill.
//
// # A panic is delivered, never swallowed
//
// If fn panics, the recovered value and its stack are captured and re-raised
// in EVERY waiter as a [PanicValue]. The alternative — recover and report an
// error — turns a programming fault into a runtime condition and loses the
// original stack; the other alternative — let it escape the goroutine — takes
// the process down while the waiters are still blocked on a channel that will
// never close. One panic becoming N is the honest arithmetic: N callers asked
// for a result, and none of them can have one.
//
// # What this is NOT
//
// It is not a cache: nothing is remembered once the call completes, so two
// SEQUENTIAL Do calls run fn twice. It is not a lock: two DIFFERENT keys never
// wait for each other. And it is per-PROCESS — N replicas of a service each
// running this primitive still send N concurrent calls to whatever is behind
// them.
package singleflight

import (
	"context"
	"sync"
)

// initialCallsCapacity pre-sizes the in-flight map on first use. It is a hint,
// not a bound: a Group is a deduplication window, and the number of DISTINCT
// keys in flight at one instant is small even when the call rate is not —
// that is the whole premise of the primitive.
const initialCallsCapacity int = 8

// Group deduplicates concurrent [Group.Do] calls that name the same key. The
// zero value is ready to use; a Group must not be copied after first use.
//
// Safe for concurrent use by any number of goroutines.
type Group[K comparable, V any] struct {
	//: RWMutex rather than Mutex only for InFlight, the one read-only path;
	//: every other method mutates the map or a refcount and takes Lock.
	mu    sync.RWMutex
	calls map[K]*call[V]
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
	//: join the in-flight call for this key, or become the one that runs it.
	entry, joined := g.join(ctx, key, fn)
	//: wait for the result, for this caller's cancellation, or for a panic.
	return g.await(ctx, key, entry, joined)
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
	g.mu.Lock()
	defer g.mu.Unlock()
	//: a key that names nothing is already forgotten.
	delete(g.calls, key)
}

// InFlight reports how many distinct keys are currently executing. It is a
// point-in-time reading, useful for a gauge or a test — never for a decision,
// since the number can change between the read and the next statement.
func (g *Group[K, V]) InFlight() int {
	g.mu.RLock()
	defer g.mu.RUnlock()
	//: the map holds exactly the keys that have a call in flight.
	return len(g.calls)
}

// join registers this goroutine as a waiter on key's call, starting one if
// none is in flight. joined reports whether the call was already running.
func (g *Group[K, V]) join(ctx context.Context, key K, fn func(ctx context.Context) (V, error)) (entry *call[V], joined bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	//: an in-flight call is joined rather than duplicated — the whole point.
	if existing, found := g.calls[key]; found {
		//: one more waiter; the count is what keeps the call alive.
		existing.refs++
		//: the caller learns its result was shared.
		return existing, true
	}
	//: the shared call keeps this caller's VALUES and none of its
	//: cancellation; the cancel below is the Group's own, fired when the last
	//: waiter leaves rather than when this one does.
	callCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	//: one waiter (this goroutine) and one goroutine about to run fn.
	fresh := &call[V]{done: make(chan struct{}), cancel: cancel, refs: 1}
	//: the map is built lazily so the zero Group is usable.
	if g.calls == nil {
		//: first call on this Group — see initialCallsCapacity for the hint.
		g.calls = make(map[K]*call[V], initialCallsCapacity)
	}
	g.calls[key] = fresh
	//: fn runs on its own goroutine so no caller's departure can stop it.
	go g.run(callCtx, key, fresh, fn)
	//: this goroutine started the call, so its result is not shared.
	return fresh, false
}

// run executes fn, publishes its outcome on entry, and retires key. It runs on
// its own goroutine: that is what makes a caller's departure a departure
// rather than a kill.
func (g *Group[K, V]) run(ctx context.Context, key K, entry *call[V], fn func(ctx context.Context) (V, error)) {
	//: releases the context regardless of how fn ends; cancelling an already
	//: cancelled context is a no-op, so the abandonment path may beat us here.
	defer entry.cancel()
	//: publish exactly once, whether fn returned or panicked — a waiter
	//: blocked on a channel that never closes is the worst outcome available.
	defer g.publish(key, entry)
	//: a panic is captured here and re-raised in every waiter by await.
	defer entry.capturePanic()
	//: the single execution every caller of this key is sharing.
	entry.val, entry.err = fn(ctx)
}

// publish retires key (so the next Do starts fresh) and wakes every waiter.
// The map entry is dropped BEFORE done is closed so a goroutine arriving in
// between starts a new call instead of joining a finished one.
func (g *Group[K, V]) publish(key K, entry *call[V]) {
	g.mu.Lock()
	//: only retire the entry still under this key — Forget, or an abandoned
	//: predecessor, may already have replaced it.
	if g.calls[key] == entry {
		//: the call is over; the key is free again.
		delete(g.calls, key)
	}
	entry.retired = true
	g.mu.Unlock()
	//: every waiter wakes here, and the close is the happens-before edge that
	//: makes entry.val / entry.err / entry.panicked safe to read. The Once
	//: makes "published exactly once" a property of the TYPE rather than of
	//: run's defer discipline — a second close would panic, and a panicking
	//: publish is the one failure that would leave every waiter parked forever.
	entry.publishOnce.Do(func() { close(entry.done) })
}

// await blocks until the result lands or ctx is done.
//
// A served caller does NOT touch the group mutex: publish already retired the
// entry under it, and refs is read only to decide whether an ABANDONING caller
// was the last one — a decision that is moot once the call is retired. Skipping
// the bookkeeping keeps the deduplicated path at one mutex acquisition per
// caller (the join) instead of two.
func (g *Group[K, V]) await(ctx context.Context, key K, entry *call[V], shared bool) (value V, isShared bool, err error) {
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
		g.abandon(key, entry)
		var zero V
		//: the caller's own cancellation, never another caller's.
		return zero, shared, ctx.Err()
	}
}

// abandon drops the claim of a caller that gave up. When the LAST such caller
// leaves, the key is retired and the shared context is cancelled: a call whose
// every caller has gone is computing a result nobody will read.
func (g *Group[K, V]) abandon(key K, entry *call[V]) {
	g.mu.Lock()
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
		if g.calls[key] == entry {
			//: the next Do on this key starts a new call.
			delete(g.calls, key)
		}
	}
	g.mu.Unlock()
	//: cancel outside the lock; fn may observe it and return immediately.
	if orphaned {
		//: the audience is empty — stop paying for the answer.
		entry.cancel()
	}
}
