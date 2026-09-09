// Package topic — one subscriber's handle, and the delivery paths behind it.
package topic

import (
	"context"
	"sync"
	"sync/atomic"
)

// Listener is one subscriber's handle on a [Topic].
//
// Obtain one from [Topic.Subscribe]; the zero value is not usable and cannot be
// made so. Safe for concurrent use.
type Listener[T any] struct {
	// owner is the Topic this handle belongs to, so Unsubscribe needs no
	// argument and cannot be pointed at the wrong topic.
	owner *Topic[T]
	// values carries the delivered payloads. NEVER closed — see the package
	// documentation for why that is a guarantee rather than an omission.
	values chan T
	// done is closed exactly once, by leave, when this subscriber departs or
	// the topic closes. It is what releases a publisher parked on a Block
	// delivery to this subscriber.
	done chan struct{}
	// leaveOnce guards close(done): Unsubscribe may be deferred by the
	// subscriber AND reached by Topic.Close, and a second close panics.
	leaveOnce sync.Once
	// evict serialises the receive-then-send pair of the DropOldest policy
	// against other publishers. Untaken by every other policy.
	evict sync.Mutex
	// delivery is the decision made at Subscribe. Immutable afterwards.
	delivery DeliveryConfig
	// dropped counts values this subscriber never saw. It is the reason a drop
	// policy is not a silent policy.
	dropped atomic.Uint64
}

// Values is the channel this subscriber reads.
//
// It is never closed, so ranging over it never ends. Select on it together with
// [Listener.Done] (or with a context), which is the shape this primitive is
// built for.
func (s *Listener[T]) Values() <-chan T {
	//: receive-only: a subscriber that could publish into its own inbox would
	//: be indistinguishable, downstream, from the topic.
	return s.values
}

// Done closes when this subscriber leaves — by its own Unsubscribe, or because
// the [Topic] closed. It is the termination signal the value channel
// deliberately does not carry.
func (s *Listener[T]) Done() <-chan struct{} {
	//: same shape as context.Context.Done, for the same reason.
	return s.done
}

// Dropped reports how many values never reached this subscriber: discarded by a
// drop policy, or abandoned because Publish's context expired while a Block
// delivery waited.
//
// It does NOT count values a departed subscriber missed — leaving is not
// falling behind, and conflating the two would make a clean shutdown look like
// a capacity problem.
//
// Monotonic, and a point-in-time reading.
func (s *Listener[T]) Dropped() uint64 {
	//: the counter is the whole answer to "was anything lost".
	return s.dropped.Load()
}

// Unsubscribe removes this subscriber from its topic. It is idempotent, safe to
// defer, and safe to call from inside the goroutine reading [Listener.Values]
// — including while a publisher is mid-delivery to it.
//
// Values already delivered remain readable afterwards; the channel is not
// closed and not drained.
func (s *Listener[T]) Unsubscribe() {
	//: close done FIRST: it releases a publisher parked on a Block delivery to
	//: this subscriber, so the membership rewrite below cannot wait on one.
	s.leave()
	//: drop out of the fan-out list.
	s.owner.unregister(s)
}

// leave closes done exactly once.
func (s *Listener[T]) leave() {
	//: a subscriber can be released twice — its own Unsubscribe and the topic's
	//: Close — and a second close of a channel panics.
	s.leaveOnce.Do(func() { close(s.done) })
}

// deliver hands v to this subscriber under its chosen policy and reports
// whether it landed.
func (s *Listener[T]) deliver(ctx context.Context, v T) bool {
	select {
	//: already gone: a departure is not a drop, and is not counted as one.
	case <-s.done:
		//: nothing landed, and nobody was waiting for it.
		return false
	default:
	}
	//: exhaustive on purpose — a new policy must fail to compile here rather
	//: than fall through to a default that silently blocks or silently drops.
	switch s.delivery.mode {
	//: the caller asked for backpressure and gets exactly that.
	case modeBlock:
		//: waits, bounded by ctx and by this subscriber leaving.
		return s.deliverBlocking(ctx, v)
	//: keep the freshest window; costs a mutex, see deliverEvicting.
	case modeDropOldest:
		//: evicts the stalest buffered value to make room for this one.
		return s.deliverEvicting(v)
	//: keep the earliest window; the cheapest path there is.
	case modeDropNewest:
		//: one non-blocking send; refuses this value when the buffer is full.
		return s.deliverRefusing(v)
	//: unreachable: Subscribe refuses an unset policy at construction.
	case modeUnset:
		//: deliver nothing rather than pick a policy nobody asked for.
		return false
	}
	//: unreachable for the same reason; the compiler cannot see it.
	return false
}

// deliverBlocking implements Block: wait for room, bounded by the publisher's
// context and by this subscriber leaving.
func (s *Listener[T]) deliverBlocking(ctx context.Context, v T) bool {
	select {
	case s.values <- v:
		//: the producer waited and the value landed.
		return true
	case <-s.done:
		//: it left while we waited; a departure, so not counted as a drop.
		return false
	case <-ctx.Done():
		//: the producer gave up on this subscriber. That IS a loss, so it is
		//: counted — Block is a coupling, never a hang.
		s.dropped.Add(1)
		//: nothing landed.
		return false
	}
}

// deliverRefusing implements DropNewest: discard the arriving value when the
// buffer is full.
func (s *Listener[T]) deliverRefusing(v T) bool {
	select {
	case s.values <- v:
		//: room was available.
		return true
	default:
		//: the loss is counted, which is what stops it being silent.
		s.dropped.Add(1)
		//: this subscriber is behind.
		return false
	}
}

// deliverEvicting implements DropOldest: discard the stalest buffered value to
// make room for v.
func (s *Listener[T]) deliverEvicting(v T) bool {
	//: the receive-then-send below is two operations, and two publishers
	//: interleaving them could each evict one value and land only one — a loss
	//: neither of them would be able to attribute.
	s.evict.Lock()
	defer s.evict.Unlock()
	select {
	case s.values <- v:
		//: room was already available; nothing had to be discarded.
		return true
	default:
	}
	select {
	//: discard the OLDEST buffered value — the one whose information is most
	//: stale — and count it.
	case <-s.values:
		s.dropped.Add(1)
	default:
	}
	select {
	case s.values <- v:
		//: the slot freed above cannot have been taken: other publishers are
		//: behind evict, and a receiver only frees more.
		return true
	default:
	}
	//: unreachable while the reasoning above holds. Counted rather than
	//: asserted so that a future change to the locking turns a lost value into
	//: a visible number instead of a silent one.
	s.dropped.Add(1)
	//: nothing landed.
	return false
}
