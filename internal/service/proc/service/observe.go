package service

import (
	"sync"

	coresvc "github.com/kitsunium/sdk/internal/core/proc/service"
)

const (
	// defaultQueue is a subscription's capacity when Subscribe is given 0.
	defaultQueue int = 64
	// maxQueue is the largest capacity a subscription takes.
	maxQueue int = 4096
)

// subscription is one consumer's queue: the actor offers it every event and
// never waits on it.
type subscription struct {
	ch chan coresvc.EventValue
	// once closes ch, whoever ends the subscription first.
	once sync.Once
}

// Subscribe returns a channel that receives one EventValue per published
// snapshot, and the function that ends the subscription and closes the
// channel. The channel holds capacity events — 0 is 64, and the capacity is
// held within 1 and 4096 —, and the unit never waits on it: when it is full
// the newest event is dropped and counted in the unit's Dropped, and a
// consumer that sees two events whose generations are not consecutive reads
// State to resynchronise (ADR 0214 §4). The unit's end closes the channel; on
// a closed unit it is closed already. cancel may be called more than once.
func (u *Unit) Subscribe(capacity int) (events <-chan coresvc.EventValue, cancel func()) {
	//: the default capacity.
	if capacity == 0 {
		capacity = defaultQueue
	}
	sub := &subscription{ch: make(chan coresvc.EventValue, min(max(capacity, 1), maxQueue))}
	u.mu.Lock()
	defer u.mu.Unlock()
	//: the unit's end is complete: nothing will come.
	if u.ended {
		sub.close()
		//: a closed channel.
		return sub.ch, func() {}
	}
	u.subs[sub] = struct{}{}
	//: listening, until cancel or the unit's end.
	return sub.ch, func() { u.unsubscribe(sub) }
}

// close closes the subscription's channel, once.
func (s *subscription) close() {
	s.once.Do(func() { close(s.ch) })
}

// unsubscribe ends one subscription: out of the unit's set, under the lock
// publish holds, then closed — so no event is offered to a closed channel.
func (u *Unit) unsubscribe(sub *subscription) {
	u.mu.Lock()
	defer u.mu.Unlock()
	delete(u.subs, sub)
	sub.close()
}

// publish records a snapshot and offers its event to every subscription,
// never waiting: a full queue drops the event and counts it. A subscription
// is offered events only while it is in the set, which unsubscribe leaves
// under the same lock before it closes the channel.
func (u *Unit) publish(snap *coresvc.StateValue, prev coresvc.State, prevSub coresvc.Sub) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.snap = *snap
	view := *snap
	view.Dropped = u.dropped
	ev := coresvc.EventValue{StateValue: view, Previous: prev, PreviousSub: prevSub}
	//: each subscription in the set.
	for sub := range u.subs {
		//: queued, or dropped.
		select {
		//: queued.
		case sub.ch <- ev:
		//: full: the newest is dropped.
		default:
			u.dropped++
		}
	}
}

// closeSubscriptions closes every subscription at the unit's end, the last
// of it: a subscription closed says everything the unit held is released.
func (u *Unit) closeSubscriptions() {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.ended = true
	//: each subscription.
	for sub := range u.subs {
		delete(u.subs, sub)
		sub.close()
	}
}
