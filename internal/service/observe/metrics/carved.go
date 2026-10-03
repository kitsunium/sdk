// Package metrics — one instrument name's slot in a collection arena.
package metrics

// carved is one instrument name's slot in a collection arena: the kind its
// name is bound to, and an empty, exactly-sized window to append its series
// into.
type carved[V any] struct {
	kind   instrumentKind
	window []V
}
