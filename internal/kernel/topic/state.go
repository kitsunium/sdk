// Package topic — one published version of a Topic's membership.
package topic

// state is the value a Topic publishes through its copy-on-write snapshot: who
// is subscribed, and whether the topic still accepts anybody.
//
// It is replaced, never mutated. A publisher may be ranging over subs at any
// moment with no lock held, which is precisely what makes an unsubscribe during
// a live fan-out harmless.
type state[T any] struct {
	// closed records that Close has run: no further subscriber is accepted.
	closed bool
	// subs is the fan-out target list.
	subs []*Listener[T]
}

// cloneStateWith returns a copy of current with joining appended. A nil current
// is the never-published state and yields a fresh open membership.
func cloneStateWith[T any](current *state[T], joining *Listener[T]) *state[T] {
	//: first subscriber on this Topic: nothing to copy.
	if current == nil {
		//: an open membership of exactly one.
		return &state[T]{subs: []*Listener[T]{joining}}
	}
	//: sized for the copy plus the newcomer, so the append below never grows.
	subs := make([]*Listener[T], 0, len(current.subs)+1)
	subs = append(subs, current.subs...)
	subs = append(subs, joining)
	//: a fresh version; current stays valid for whoever is reading it.
	return &state[T]{closed: current.closed, subs: subs}
}

// cloneStateWithout returns a copy of current with leaving removed.
func cloneStateWithout[T any](current *state[T], leaving *Listener[T]) *state[T] {
	//: at most the current membership, and usually one fewer.
	kept := make([]*Listener[T], 0, len(current.subs))
	//: identity comparison: two subscribers are the same only if they are the
	//: same handle, which is what makes Unsubscribe unambiguous.
	for _, sub := range current.subs {
		//: everyone but the departing handle survives the rewrite.
		if sub != leaving {
			kept = append(kept, sub)
		}
	}
	//: a fresh version without the departing subscriber.
	return &state[T]{closed: current.closed, subs: kept}
}
