// Package statemachine — hosts the agenda: when each entity's next automatic
// transition falls due, the entities written since the loop last looked, and
// the backoff of those whose transition failed.
package statemachine

import (
	"cmp"
	"slices"
	"sync"
	"time"

	"github.com/kitsunium/sdk/internal/kernel/heap"
	"github.com/kitsunium/sdk/internal/service/resilience"
)

// compactSlack is how many stale entries the heap may hold beyond its live
// ones before it is rebuilt: below it, a rebuild costs more than it frees.
const compactSlack int = 64

// staleFactor is how many times the live entries the heap may hold before it
// is rebuilt. With it, a rebuild is paid for by at least as many updates as
// it drops, so an update stays O(log N) amortised.
const staleFactor int = 2

// slot is one heap entry: key is due at due. seq identifies the entry, so an
// entry replaced by a later schedule is recognised as stale when it surfaces.
type slot struct {
	due time.Time
	key string
	seq uint64
}

// plan is what the agenda holds for one key.
type plan struct {
	// notBefore holds back an entity whose transition failed: no automatic
	// transition of it before this instant.
	notBefore time.Time
	// seq is the live heap entry's; zero when the key is not scheduled.
	seq uint64
	// failures counts the consecutive failed transitions.
	failures int
}

// agenda is a min-heap of due instants with ONE live entry per entity — its
// earliest — and lazy deletion: rescheduling pushes a fresh entry and leaves
// the old one to be recognised as stale and dropped when it reaches the top.
// Finding the next transition due is therefore O(log N), where a sweep that
// re-reads every entity is O(N). mu guards every field.
//
// dirty holds the entities written since the loop last looked: their next due
// instant is computed on the loop's goroutine, never on the writer's, because
// computing it runs the caller's guards and instant functions.
type agenda struct {
	heap  *heap.Heap[slot]
	plans map[string]*plan
	dirty map[string]struct{}
	// wake holds one token when the loop should look again: a write arrived.
	wake chan struct{}
	// seq numbers the entries; it never repeats, so a forgotten key's stale
	// entries can never match a plan made later.
	seq uint64
	// live counts the entries that are not stale.
	live int
	mu   sync.Mutex
}

// newAgenda returns an empty agenda.
func newAgenda() *agenda {
	//: earliest first; equal instants in the order they were scheduled.
	byDue := func(a, b slot) int { return cmp.Or(a.due.Compare(b.due), cmp.Compare(a.seq, b.seq)) }
	//: the wake channel holds one token: a burst of writes is one wake.
	return &agenda{heap: heap.New(byDue), plans: make(map[string]*plan), dirty: make(map[string]struct{}), wake: make(chan struct{}, 1)}
}

// signal wakes the loop, without blocking when a wake is already pending.
func (a *agenda) signal() {
	//: one pending token is enough; a second write adds nothing.
	select {
	//: the loop will look again.
	case a.wake <- struct{}{}:
	//: a wake is already pending.
	default:
	}
}

// markDirty asks the loop to compute key's next due instant again, and wakes
// it.
func (a *agenda) markDirty(key string) {
	a.mu.Lock()
	a.dirty[key] = struct{}{}
	a.mu.Unlock()
	a.signal()
}

// pending reports whether written entities wait to be looked at.
func (a *agenda) pending() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	//: the set, not the wake token, says whether there is work.
	return len(a.dirty) > 0
}

// requeue puts keys back among the dirty ones without waking the loop: a run
// that stopped half-way leaves them for the next one.
func (a *agenda) requeue(keys []string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	//: every key the stopped run did not reach.
	for _, key := range keys {
		a.dirty[key] = struct{}{}
	}
}

// schedule makes due key's next instant, never earlier than its backoff.
func (a *agenda) schedule(key string, due time.Time) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.scheduleLocked(key, due)
}

// scheduleLocked is schedule under mu.
func (a *agenda) scheduleLocked(key string, due time.Time) {
	p := a.plans[key]
	//: the first plan for this key.
	if p == nil {
		p = &plan{}
		a.plans[key] = p
	}
	//: a failing entity waits out its backoff, whatever falls due.
	if due.Before(p.notBefore) {
		due = p.notBefore
	}
	//: the previous entry, if any, becomes stale rather than removed.
	if p.seq == 0 {
		a.live++
	}
	a.seq++
	p.seq = a.seq
	a.heap.Push(slot{due: due, key: key, seq: p.seq})
	a.compact()
}

// unschedule takes key off the agenda: nothing it could do is due by itself.
func (a *agenda) unschedule(key string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	p := a.plans[key]
	//: never scheduled.
	if p == nil {
		//: nothing to take off.
		return
	}
	//: its live entry becomes stale.
	if p.seq != 0 {
		p.seq = 0
		a.live--
	}
	//: a plan with no entry and no failure to remember is dropped.
	if p.failures == 0 {
		delete(a.plans, key)
	}
}

// forget drops everything the agenda holds for key: the entity is gone.
func (a *agenda) forget(key string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	delete(a.dirty, key)
	//: its live entry, if any, becomes stale.
	if p := a.plans[key]; p != nil && p.seq != 0 {
		a.live--
	}
	delete(a.plans, key)
}

// restart clears key's failures: it entered a new state, and what failed was a
// transition out of the old one.
func (a *agenda) restart(key string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	//: nothing to clear for a key that never failed.
	if p := a.plans[key]; p != nil {
		p.failures, p.notBefore = 0, time.Time{}
	}
}

// failed counts one more failure of key and schedules its retry at now plus
// the backoff for that many failures.
func (a *agenda) failed(key string, now time.Time, backoff resilience.BackoffValue) {
	a.mu.Lock()
	defer a.mu.Unlock()
	p := a.plans[key]
	//: the first failure of a key with no plan yet.
	if p == nil {
		p = &plan{}
		a.plans[key] = p
	}
	p.failures++
	p.notBefore = now.Add(backoff.Delay(p.failures))
	a.scheduleLocked(key, p.notBefore)
}

// heldBack reports whether key's backoff still forbids an automatic
// transition at now.
func (a *agenda) heldBack(key string, now time.Time) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	p := a.plans[key]
	//: a key that never failed, or whose backoff has run out, may fire.
	return p != nil && p.notBefore.After(now)
}

// succeeded clears key's failures after a transition of it was stored.
func (a *agenda) succeeded(key string) {
	//: same effect as a new state: the failing transition is behind it.
	a.restart(key)
}

// take returns the keys the loop must look at, at now: every dirty key, and
// every scheduled key due at or before now — each once, due keys first in
// due order, then the dirty ones by key. Both are consumed: a due key leaves
// the heap and a dirty key the dirty set.
func (a *agenda) take(now time.Time) []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	var keys []string
	seen := make(map[string]struct{}, len(a.dirty))
	//: pop while the top is due; stale tops are dropped on the way.
	for {
		top, ok := a.heap.Peek()
		//: nothing is left, or nothing left is due.
		if !ok || top.due.After(now) && a.isLive(top) {
			//: done popping.
			break
		}
		a.heap.Pop()
		//: a replaced or cancelled entry is just dropped.
		if !a.isLive(top) {
			//: the next top.
			continue
		}
		a.plans[top.key].seq = 0
		a.live--
		keys = append(keys, top.key)
		seen[top.key] = struct{}{}
	}
	dirty := make([]string, 0, len(a.dirty))
	//: the dirty keys not already taken as due.
	for key := range a.dirty {
		//: a key both due and dirty is looked at once.
		if _, dup := seen[key]; !dup {
			dirty = append(dirty, key)
		}
	}
	clear(a.dirty)
	slices.Sort(dirty)
	//: due keys first, in due order, then the dirty ones.
	return append(keys, dirty...)
}

// next says when the loop should look again by itself: now when keys wait to
// be looked at, the earliest due instant otherwise, zero when nothing is
// scheduled.
func (a *agenda) next(now time.Time) time.Time {
	a.mu.Lock()
	defer a.mu.Unlock()
	//: written entities wait: look again as soon as allowed.
	if len(a.dirty) > 0 {
		//: at once, subject to the loop's pace.
		return now
	}
	//: drop stale tops so the answer is a live entry's.
	for {
		top, ok := a.heap.Peek()
		//: nothing scheduled at all.
		if !ok {
			//: only a write can wake the loop.
			return time.Time{}
		}
		//: the earliest live entry.
		if a.isLive(top) {
			//: the loop sleeps until then.
			return top.due
		}
		a.heap.Pop()
	}
}

// isLive reports whether s is its key's current entry. The caller holds mu.
func (a *agenda) isLive(s slot) bool {
	p := a.plans[s.key]
	//: the key's plan names this very entry.
	return p != nil && p.seq == s.seq
}

// compact rebuilds the heap once stale entries outnumber live ones by
// staleFactor, so memory stays proportional to the entities scheduled. The
// caller holds mu.
func (a *agenda) compact() {
	//: below the threshold a rebuild would cost more than it frees.
	if a.heap.Len() <= staleFactor*a.live+compactSlack {
		//: keep the stale entries until they surface.
		return
	}
	kept := make([]slot, 0, a.live)
	//: drain, keeping only the live entries.
	for {
		s, ok := a.heap.Pop()
		//: drained.
		if !ok {
			//: rebuild from what was kept.
			break
		}
		//: stale entries are dropped here once and for all.
		if a.isLive(s) {
			kept = append(kept, s)
		}
	}
	//: push back in order; a sorted push never sifts.
	for _, s := range kept {
		a.heap.Push(s)
	}
}
