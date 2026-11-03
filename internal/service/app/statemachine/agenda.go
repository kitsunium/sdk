package statemachine

import (
	"cmp"
	"slices"
	"sync"
	"time"

	"github.com/kitsunium/sdk/internal/kernel/collections/heap"
)

// slot is one heap entry: key is due at due. seq orders equal instants in the
// order they were scheduled.
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
	// entry names the key's heap entry; the zero Handle when the key is not
	// scheduled.
	entry heap.Handle
	// failures counts the consecutive failed transitions.
	failures int
}

// scheduled reports whether p holds a heap entry.
func (p *plan) scheduled() bool {
	//: the zero Handle names nothing.
	return p.entry != heap.Handle{}
}

// agenda is a min-heap of due instants with exactly ONE entry per scheduled
// entity — its earliest. A reschedule re-keys that entry in place and an
// unschedule removes it (kernel/collections/heap's IndexedHeap, #1036), so the
// heap never holds a stale entry and never needs rebuilding. Finding the next
// transition due is O(log N), where a sweep that re-reads every entity is
// O(N). mu guards every field.
//
// dirty holds the entities written since the loop last looked: their next due
// instant is computed on the loop's goroutine, never on the writer's, because
// computing it runs the caller's guards and instant functions.
type agenda struct {
	heap  *heap.IndexedHeap[slot]
	plans map[string]*plan
	dirty map[string]struct{}
	// wake holds one token when the loop should look again: a write arrived.
	wake chan struct{}
	// seq numbers the schedules, so equal instants keep their order.
	seq uint64
	mu  sync.Mutex
}

// newAgenda returns an empty agenda.
func newAgenda() *agenda {
	//: earliest first; equal instants in the order they were scheduled.
	byDue := func(a, b slot) int { return cmp.Or(a.due.Compare(b.due), cmp.Compare(a.seq, b.seq)) }
	//: the wake channel holds one token: a burst of writes is one wake.
	return &agenda{heap: heap.NewIndexed(byDue), plans: make(map[string]*plan), dirty: make(map[string]struct{}), wake: make(chan struct{}, 1)}
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
	a.seq++
	next := slot{due: due, key: key, seq: a.seq}
	//: a scheduled key's entry is re-keyed in place: one entry per key.
	if p.scheduled() {
		a.heap.Update(p.entry, next)
		//: rescheduled.
		return
	}
	p.entry = a.heap.Push(next)
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
	//: its entry leaves the heap now, not when it would have surfaced.
	if p.scheduled() {
		a.heap.Remove(p.entry)
		p.entry = heap.Handle{}
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
	//: its entry, if any, leaves the heap with it.
	if p := a.plans[key]; p != nil && p.scheduled() {
		a.heap.Remove(p.entry)
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
// the backoff for that many failures. delay is the curve — the machine's
// configured kernel/backoff.Value, handed over as its Delay method so the
// agenda depends on "the wait after n failures" and on no curve type.
func (a *agenda) failed(key string, now time.Time, delay func(failures int) time.Duration) {
	a.mu.Lock()
	defer a.mu.Unlock()
	p := a.plans[key]
	//: the first failure of a key with no plan yet.
	if p == nil {
		p = &plan{}
		a.plans[key] = p
	}
	p.failures++
	p.notBefore = now.Add(delay(p.failures))
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
	//: pop while the top is due; every entry is a live one.
	for {
		top, _, ok := a.heap.Peek()
		//: nothing is left, or nothing left is due.
		if !ok || top.due.After(now) {
			//: done popping.
			break
		}
		a.heap.Pop()
		a.plans[top.key].entry = heap.Handle{}
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
	top, _, ok := a.heap.Peek()
	//: nothing scheduled at all.
	if !ok {
		//: only a write can wake the loop.
		return time.Time{}
	}
	//: the earliest entry; the loop sleeps until then.
	return top.due
}
