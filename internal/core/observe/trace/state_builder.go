package trace

import "slices"

// NewStateBuilder returns an empty builder with room for size members. A size
// above MaxTraceStateMembers is clamped to it — no list holds more — and a
// negative one is read as zero.
func NewStateBuilder(size int) StateBuilder {
	//: the room is a hint; the grammar's own cap bounds it.
	return StateBuilder{entries: make([]traceStateEntry, 0, min(max(size, 0), MaxTraceStateMembers))}
}

// Add appends key=value at the right end of the list, reporting whether it
// did. It adds nothing and reports false when the key or the value is not one
// the grammar can spell, when the key is already in the list, or when the list
// already holds MaxTraceStateMembers members. Nothing of either argument is
// echoed anywhere: a tracestate is written by a stranger.
func (b *StateBuilder) Add(key, value string) bool {
	//: a member the grammar cannot spell, a second entry for one key (the
	//: list would be ambiguous) or a 33rd member: each refused, nothing added.
	if !isValidTraceStateKey(key) || !isValidMemberValue(value) ||
		slices.ContainsFunc(b.entries, keyMatcher(key)) || len(b.entries) == MaxTraceStateMembers {
		//: refused.
		return false
	}
	b.entries = append(b.entries, traceStateEntry{key: key, value: value})
	//: appended, in the order it arrived.
	return true
}

// State returns the list built so far, leftmost first, and leaves the builder
// empty. The builder hands its storage to the value rather than copying it, so
// it lets go of that storage too: a later Add starts a new list instead of
// writing into one that is meant to be immutable.
func (b *StateBuilder) State() StateValue {
	state := StateValue{entries: b.entries}
	b.entries = nil
	//: an empty builder yields the empty list.
	return state
}
