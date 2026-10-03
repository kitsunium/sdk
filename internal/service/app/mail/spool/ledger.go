// Package spool — the memory of what was delivered, which is what drops a
// redelivery instead of sending a mail twice.
package spool

import "sync"

// ledger remembers the last size identifiers it was given, in a ring.
type ledger struct {
	// seen indexes the ring.
	seen map[string]struct{}
	// ring holds the identifiers, oldest overwritten first.
	ring []string
	// mu guards everything.
	mu sync.Mutex
	// next is the ring slot the next identifier takes.
	next int
}

// newLedger returns a ledger remembering size identifiers.
func newLedger(size int) *ledger {
	//: empty, with room for size.
	return &ledger{seen: make(map[string]struct{}, size), ring: make([]string, size)}
}

// add remembers id, forgetting the oldest identifier when the ring is full.
func (l *ledger) add(id string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	//: remembered already: nothing moves.
	if _, known := l.seen[id]; known {
		return
	}
	//: the slot's previous occupant, forgotten.
	if old := l.ring[l.next]; old != "" {
		delete(l.seen, old)
	}
	l.ring[l.next] = id
	l.seen[id] = struct{}{}
	l.next = (l.next + 1) % len(l.ring)
}

// has reports whether id is remembered.
func (l *ledger) has(id string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	_, known := l.seen[id]
	//: delivered, as far as this process knows.
	return known
}
