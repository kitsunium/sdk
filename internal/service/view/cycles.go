// Package view — the cycle detection the trust-type scan needs, kept in its own
// file so scan.go holds the walk and nothing else.
package view

import "reflect"

// initialSeenCapacity sizes the cycle-detection map on the rare occasion it is
// allocated at all. A scan that has descended past the threshold is already in
// a shape nobody writes, so the hint is small on purpose.
const initialSeenCapacity int = 16

// visitedKey identifies a pointer already followed during one scan. The length
// is carried for slices, mirroring encoding/json, so two slices sharing a
// backing array are not mistaken for one.
type visitedKey struct {
	// typ is the static type at the visit site.
	typ reflect.Type
	// addr is the value's pointer.
	addr uintptr
	// extra is the slice length, or zero for every other kind.
	extra int
}

// repeat reports whether value's pointer has already been followed in this
// scan, and records it when it has not.
//
// Nothing is recorded below [startDetectingCyclesAfter]: a model shallower
// than that cannot loop without recursing through it first, so the map is
// never allocated on a normal render.
func (s *scanner) repeat(value reflect.Value, depth, extra int) bool {
	//: below the threshold, recursion alone is the (json-identical) policy.
	if depth < startDetectingCyclesAfter {
		//: no bookkeeping, no allocation.
		return false
	}
	//: allocate only once the threshold is actually crossed.
	if s.seen == nil {
		s.seen = make(map[visitedKey]struct{}, initialSeenCapacity)
	}
	key := visitedKey{typ: value.Type(), addr: value.Pointer(), extra: extra}
	//: a repeat means this subtree was already scanned in this walk.
	if _, duplicate := s.seen[key]; duplicate {
		//: stop descending; nothing new lies below.
		return true
	}
	s.seen[key] = struct{}{}
	//: first visit — descend.
	return false
}
