// Package events — one published version of the bus membership, and the two
// rewrites that produce the next one.
package events

import (
	"maps"
	"slices"

	corev "github.com/kitsunium/sdk/internal/core/events"
)

// state is the value a bus publishes through its copy-on-write snapshot: the
// subscriptions held for each event type, each list ALREADY ORDERED.
//
// It is replaced, never mutated. A publisher may be ranging over one of these
// slices at any moment with no lock held, which is what makes a Subscribe or
// an Unsubscribe during a live dispatch harmless — and what makes the
// dispatch itself a single atomic load.
//
// The lists are kept sorted at REGISTRATION rather than at publication. A bus
// is written once at wiring time and read once per event for the life of the
// process, so sorting on the read path would pay the cost on exactly the side
// that cannot afford it — and would allocate a scratch slice per published
// event, on the hottest path there is.
type state struct {
	// byType maps a concrete event type to its ordered subscriptions.
	byType map[corev.EventType][]corev.SubscriptionValue
}

// cloneStateWith returns a copy of current with sub inserted in its ordered
// position among the subscriptions for eventType. A nil current is the
// never-published state and yields a fresh membership of exactly one.
func cloneStateWith(current *state, eventType corev.EventType, sub corev.SubscriptionValue) *state {
	//: first subscription on this bus: nothing to copy.
	if current == nil {
		//: a membership holding exactly one listener, for one type.
		return &state{byType: map[corev.EventType][]corev.SubscriptionValue{
			eventType: {sub},
		}}
	}
	next := cloneByType(current.byType)
	next[eventType] = insertOrdered(current.byType[eventType], sub)
	//: a fresh version; current stays valid for whoever is dispatching from it.
	return &state{byType: next}
}

// cloneStateWithout returns a copy of current with the listener named name
// removed from eventType's list, and reports whether it was there.
func cloneStateWithout(current *state, eventType corev.EventType, name string) (next *state, found bool) {
	//: nothing was ever registered, so nothing can be removed.
	if current == nil {
		//: the caller turns this into UnknownListener.
		return nil, false
	}
	subs := current.byType[eventType]
	index := slices.IndexFunc(subs, func(s corev.SubscriptionValue) bool {
		//: identity is the registered name, which Subscribe keeps unique per type.
		return s.Name == name
	})
	//: no listener under that name for that type.
	if index < 0 {
		//: leave the membership exactly as it was.
		return current, false
	}
	rebuilt := cloneByType(current.byType)
	//: rebuild rather than delete in place: the current slice is live for any
	//: publisher that has already loaded it.
	rebuilt[eventType] = slices.Delete(slices.Clone(subs), index, index+1)
	//: the last listener for a type takes the empty slice with it, so the map
	//: does not accumulate one entry per type ever subscribed to.
	if len(rebuilt[eventType]) == 0 {
		delete(rebuilt, eventType)
	}
	//: a fresh version without the departing listener.
	return &state{byType: rebuilt}, true
}

// cloneByType copies the type index. The copy is SHALLOW by design: a
// subscription list is only ever replaced whole, so sharing a slice header
// with the previous version is safe, and it is what keeps a rewrite
// proportional to the number of TYPES rather than to the number of listeners.
func cloneByType(
	current map[corev.EventType][]corev.SubscriptionValue,
) map[corev.EventType][]corev.SubscriptionValue {
	next := maps.Clone(current)
	//: maps.Clone(nil) is nil, and the caller is about to write into this.
	if next == nil {
		//: an empty index the caller can populate.
		return map[corev.EventType][]corev.SubscriptionValue{}
	}
	//: the copy the caller is about to mutate.
	return next
}

// insertOrdered returns a NEW slice with sub placed after every subscription
// whose Priority is less than or equal to its own.
//
// "Less than or equal" is the whole tie rule: an equal priority means the
// newcomer goes AFTER the incumbents, so subscriptions registered at the same
// priority run in registration order. Sorting the list instead would need a
// stable sort AND a sequence number to stay stable across the rewrites, and
// would re-derive on every Subscribe an order the insert already knows.
func insertOrdered(current []corev.SubscriptionValue, sub corev.SubscriptionValue) []corev.SubscriptionValue {
	at := len(current)
	//: walk from the end: the common case is an append, and a bus is usually
	//: wired in the order it runs.
	for at > 0 && current[at-1].Priority > sub.Priority {
		at--
	}
	//: sized for the copy plus the newcomer, so the inserts below never grow.
	next := make([]corev.SubscriptionValue, 0, len(current)+1)
	next = append(next, current[:at]...)
	next = append(next, sub)
	next = append(next, current[at:]...)
	//: the ordered list this event type will be dispatched through.
	return next
}
