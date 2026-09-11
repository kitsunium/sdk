// Package topic provides Topic[T], a typed in-process broadcast: every
// published value reaches every current subscriber. It is a kernel primitive —
// stdlib-only (plus the kernel's own copy-on-write snapshot), and
// domain-neutral: Topic, Publish, Subscribe, and a T, with no Event, no
// Message and no Handler in a signature.
//
// # A slow subscriber never stalls the producer in silence
//
// This is the property that makes or breaks a broadcast primitive, and it is
// why there is no default delivery mode. Every [Topic.Subscribe] names one, at
// the call site, by building a [DeliveryConfig] with [Block], [DropOldest] or
// [DropNewest]:
//
//   - Block couples the producer to this subscriber on purpose. Publish waits,
//     bounded by the caller's own context, and returns a delivered count below
//     [Topic.Subscribers] when the value did not land.
//   - DropOldest keeps the freshest window: the stale value is discarded to
//     make room, and counted in [Listener.Dropped].
//   - DropNewest keeps the earliest window: the arriving value is discarded,
//     and counted.
//
// A [DeliveryConfig] has no usable zero value — ADR 0031, applied by making the
// bad state unspellable rather than merely checked. Whatever a zero meant it
// would be a backpressure decision the caller never made, and the two
// candidates are the exact failures this primitive exists to prevent: a silent
// stall or a silent loss.
//
// Silence is closed off from both ends. With a drop policy the loss is COUNTED
// and readable; with Block the coupling is written at the call site and bounded
// by Publish's context.
//
// # Leaving during a fan-out
//
// [Listener.Unsubscribe] is safe at any moment, including while a Publish is
// delivering to that very subscriber, and it never costs another subscriber its
// copy. Two decisions buy that:
//
//   - Publish loads the subscriber list from a copy-on-write snapshot and
//     delivers OUTSIDE any lock, so an unsubscribe never waits on a delivery —
//     including the delivery that only the unsubscribe could have unblocked.
//   - The value channel is NEVER closed. Unsubscribe closes a separate done
//     channel, which releases a publisher parked on a Block delivery. Closing
//     the value channel instead would put "send on closed channel" one race
//     away, and that panic lands in the PUBLISHER — code that did nothing
//     wrong. Here it is unreachable rather than unlikely.
//
// The cost of that choice is stated rather than hidden: ranging over
// [Listener.Values] never ends. Select on [Listener.Done] alongside it,
// exactly as with a context.
package topic

import (
	"context"

	"github.com/kitsunium/sdk/internal/kernel/snapshot"
)

// Topic broadcasts every published value to every current subscriber.
//
// The zero value is ready to use, as sync.Mutex and sync.Map are; a Topic must
// not be copied after first use. Safe for concurrent use by any number of
// publishers and subscribers.
type Topic[T any] struct {
	// members is the subscriber list plus the closed flag, published as one
	// copy-on-write snapshot. Publish reads it with a single atomic load and no
	// allocation; Subscribe / Unsubscribe / Close rebuild it, which is the
	// right trade for a list that changes at setup and is read per value.
	//
	// The closed flag lives INSIDE the snapshot rather than beside it so that
	// closing and subscribing are serialised by the one writer lock. A separate
	// atomic.Bool would let a Subscribe racing a Close register into a list
	// that has already been abandoned.
	members snapshot.Value[state[T]]
}

// Subscribe registers a new subscriber and returns its handle.
//
// delivery says what happens when this subscriber falls behind and MUST be built
// with [Block], [DropOldest] or [DropNewest]; a zero [DeliveryConfig] panics, at
// the call that made the omission. See the package documentation for why there
// is no default.
//
// Subscribing to a closed Topic is neither an error nor a panic: the returned
// Listener is already done, so a shutdown race reads as a shutdown at the
// call site rather than as a crash.
func (t *Topic[T]) Subscribe(delivery DeliveryConfig) *Listener[T] {
	//: refuse the unspoken policy here, where the omission is.
	if delivery.mode == modeUnset {
		//: no value the SDK could pick would be the caller's; see ADR 0031.
		panic("kernel/topic: Subscribe needs an explicit delivery policy — build one with topic.Block, topic.DropOldest or topic.DropNewest")
	}
	sub := &Listener[T]{
		//: the topic this handle detaches from; fixed for the handle's life.
		owner: t,
		//: the value channel; never closed, see the package doc.
		values: make(chan T, delivery.capacity),
		//: closed on Unsubscribe or Close; releases a blocked publisher.
		done:     make(chan struct{}),
		delivery: delivery,
	}
	registered := false
	t.members.Update(func(current *state[T]) *state[T] {
		//: a closed topic accepts nobody; returning current is Update's
		//: documented way to abort without republishing anything.
		if current != nil && current.closed {
			//: the membership stays exactly as it was.
			return current
		}
		registered = true
		//: copy-on-write: a publisher may be ranging over the current list.
		return cloneStateWith(current, sub)
	})
	//: a subscriber nobody will ever publish to is done from the start, which
	//: is what makes the caller's select on Done fire immediately.
	if !registered {
		sub.leave()
	}
	//: the handle: Values, Done, Dropped, Unsubscribe.
	return sub
}

// Publish delivers v to every current subscriber and returns how many of them
// took it.
//
// A delivered count below [Topic.Subscribers] is the primitive telling the
// caller something did not land: a drop policy discarded the value (counted in
// that subscriber's [Listener.Dropped]), a subscriber left mid-fan-out, or
// ctx expired while a Block delivery was waiting. It is never
// zero-information.
//
// ctx bounds only the Block policy — the drop policies never wait. It is the
// producer's escape hatch from a subscriber that has stopped reading, and the
// reason Block is a coupling rather than a hang.
//
// Values from ONE publisher reach each subscriber in publication order. Values
// from concurrent publishers interleave in an unspecified order, and so does
// the order in which subscribers are visited inside a single Publish.
func (t *Topic[T]) Publish(ctx context.Context, v T) int {
	//: one atomic load, no lock and no allocation — and the copy-on-write
	//: snapshot is what lets a subscriber leave DURING this fan-out without
	//: stopping it and without the others losing the value.
	current := t.members.Load()
	//: no subscriber has ever been registered on this topic.
	if current == nil {
		//: nothing was delivered because there was nobody to deliver to.
		return 0
	}
	delivered := 0
	//: deliver outside every lock; see the package doc.
	for _, sub := range current.subs {
		//: each subscriber applies its OWN policy; one refusing costs the
		//: others nothing.
		if sub.deliver(ctx, v) {
			delivered++
		}
	}
	//: how many copies actually landed.
	return delivered
}

// Subscribers reports how many subscribers are currently registered. It is a
// point-in-time reading — useful next to a Publish result or in a gauge, never
// for a decision, since it can change before the next statement.
func (t *Topic[T]) Subscribers() int {
	current := t.members.Load()
	//: never subscribed to.
	if current == nil {
		//: no membership has been published yet.
		return 0
	}
	//: the list length is the count.
	return len(current.subs)
}

// Close releases every current subscriber — their [Listener.Done] channel
// closes, and a publisher parked on a Block delivery to any of them returns —
// and makes every later [Topic.Subscribe] hand back an already-done handle.
//
// It does NOT close the value channels: values already delivered stay readable,
// and a publisher racing this call cannot crash on a closed channel. Close is
// idempotent.
func (t *Topic[T]) Close() {
	var leaving []*Listener[T]
	t.members.Update(func(current *state[T]) *state[T] {
		//: captured under the writer lock, so no Subscribe can slip a new
		//: member past this snapshot and be left running.
		if current != nil {
			leaving = current.subs
		}
		//: the list is dropped, not emptied in place — a publisher mid-fan-out
		//: keeps ranging over the slice it already loaded, which is exactly
		//: what the copy-on-write contract promised it.
		return &state[T]{closed: true}
	})
	//: closes done only; releases anyone blocked on these subscribers.
	for _, sub := range leaving {
		sub.leave()
	}
}

// unregister drops target from the membership list. Called by
// [Listener.Unsubscribe] AFTER it has closed its done channel, so a publisher
// blocked on target is already on its way out and this rewrite cannot wait on
// it.
func (t *Topic[T]) unregister(target *Listener[T]) {
	t.members.Update(func(current *state[T]) *state[T] {
		//: nothing was ever published; nothing to remove.
		if current == nil {
			//: keep the empty state.
			return nil
		}
		//: rebuild rather than filter in place: the current slice is live for
		//: any publisher that has already loaded it.
		return cloneStateWithout(current, target)
	})
}
