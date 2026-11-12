package http1

import (
	"sync"
	"sync/atomic"
)

// laneTable finds a link's state of a lane, built the first time the lane
// comes, with no lock on any later call: a lane touches only its own entry, and
// the table of pointers is replaced, never written in place, under mu.
type laneTable[T any] struct {
	// states holds each lane's state at its index.
	states atomic.Pointer[[]*T]
	// mu serialises the first sight of a lane.
	mu sync.Mutex
}

// get returns lane i's state, built on the lane's first call.
func (t *laneTable[T]) get(i int) *T {
	//: the common case: the lane has been seen.
	if p := t.states.Load(); p != nil && i < len(*p) && (*p)[i] != nil {
		//: no lock taken.
		return (*p)[i]
	}
	//: once per lane.
	return t.grow(i)
}

// grow builds lane i's state and publishes a table that holds it.
func (t *laneTable[T]) grow(i int) *T {
	t.mu.Lock()
	defer t.mu.Unlock()
	var cur []*T
	//: the table as the other lanes left it.
	if p := t.states.Load(); p != nil {
		cur = *p
	}
	//: raced with nobody, since only lane i asks for i; checked all the same.
	if i < len(cur) && cur[i] != nil {
		//: already there.
		return cur[i]
	}
	next := make([]*T, max(len(cur), i+1))
	copy(next, cur)
	next[i] = new(T)
	t.states.Store(&next)
	//: published.
	return next[i]
}
