// Package statemachine — white-box tests of the book's journal gates.
package statemachine

import (
	"testing"
	"time"
)

// TestNoTwoKeysShareAJournalGate pins that a journal write holds back its own
// key and no other, whatever the book: the gates were 256 mutexes picked by a
// seeded hash of the key, so two keys shared one in one book of 256 and the
// second waited for the first's journal call (#304). A thousand books would
// have met that about four times.
func TestNoTwoKeysShareAJournalGate(t *testing.T) {
	t.Parallel()
	for range 1000 {
		b := newBook[int](nil, map[int]int{}, 1)
		releaseA := b.lockGate("a")
		taken := make(chan func(), 1)
		go func() { taken <- b.lockGate("b") }()
		select {
		case releaseB := <-taken:
			releaseB()
		case <-time.After(10 * time.Second):
			t.Fatal("key b waited for the gate key a holds")
		}
		releaseA()
		if len(b.gates.byKey) != 0 {
			t.Fatalf("%d gates kept once nobody holds one", len(b.gates.byKey))
		}
	}
}

// TestAGateOrdersTheWritesOfItsKey pins the other half: a second write of the
// same key waits for the first to release its gate.
func TestAGateOrdersTheWritesOfItsKey(t *testing.T) {
	t.Parallel()
	b := newBook[int](nil, map[int]int{}, 1)
	release := b.lockGate("a")
	taken := make(chan func(), 1)
	go func() { taken <- b.lockGate("a") }()
	select {
	case <-taken:
		t.Fatal("a second write of key a took the gate the first holds")
	case <-time.After(20 * time.Millisecond):
	}
	release()
	select {
	case second := <-taken:
		second()
	case <-time.After(10 * time.Second):
		t.Fatal("the second write of key a never took the released gate")
	}
}
