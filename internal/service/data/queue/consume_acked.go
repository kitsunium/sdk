package queue

import "sync"

// ackedWindow is the bounded memory of the messages one Consume handled in
// this process (ConsumerConfig.AckedWindow): the last n identifiers, oldest
// forgotten first. It is the mail spool's DeliveredMemory lifted into the
// engine, for every broker (issue #1046).
//
// Every worker of the Consume shares it, so it takes a mutex: a ring alone is
// one producer and one consumer, and here every worker is both. A nil window
// remembers nothing and holds nothing, so a Consume without one pays one nil
// check per message.
type ackedWindow struct {
	// held maps each remembered identifier to the slot of ring it sits in.
	held map[string]int
	// ring holds the identifiers in the order they were remembered; next is
	// the slot the next one takes — the oldest, once the ring is full.
	ring []string
	next int
	mu   sync.Mutex
}

// newAckedWindow returns a window remembering the last size identifiers;
// size is positive.
func newAckedWindow(size int) *ackedWindow {
	//: the map and the ring at their full size, once.
	return &ackedWindow{held: make(map[string]int, size), ring: make([]string, 0, size)}
}

// holds reports whether id is among the identifiers the window remembers.
func (w *ackedWindow) holds(id string) bool {
	//: no window remembers nothing.
	if w == nil {
		return false
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	_, held := w.held[id]
	//: remembered, or forgotten or never seen.
	return held
}

// remember adds id to the window, forgetting the oldest identifier when the
// window is full. An identifier already held is not added twice.
func (w *ackedWindow) remember(id string) {
	//: no window remembers nothing.
	if w == nil {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	//: held already: its place in the order stays.
	if _, held := w.held[id]; held {
		return
	}
	//: room left: the ring grows to its capacity once.
	if len(w.ring) < cap(w.ring) {
		w.held[id] = len(w.ring)
		w.ring = append(w.ring, id)
		return
	}
	//: full: the oldest makes room.
	delete(w.held, w.ring[w.next])
	w.ring[w.next], w.held[id] = id, w.next
	w.next = (w.next + 1) % len(w.ring)
}
