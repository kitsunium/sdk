// Package statemachine — white-box tests of the book's journal gates.
package statemachine

import (
	"testing"
	"time"
)

// TestAJournalGateHoldsBackItsOwnKeyOnly pins that a journal write holds back
// the writes of its own key and no other, whatever the book: the gates were
// 256 mutexes picked by a seeded hash of the key, so two keys shared one in
// one book of 256 and the second waited for the first's journal call (#304).
// A thousand books would have met that about four times.
func TestAJournalGateHoldsBackItsOwnKeyOnly(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		// second is the key a second write takes while "a" is held.
		second string
		// books is how many fresh books the case is run on.
		books int
		// wantWait says the second write must wait for the first.
		wantWait bool
	}
	cases := []tc{
		{name: "another key never waits", second: "b", books: 1000, wantWait: false},
		{name: "the same key waits for the gate it holds", second: "a", books: 1, wantWait: true},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		//: a fresh book each time: its seed, when it had one, picked the gates.
		for range c.books {
			b := newBook[int](nil, map[int]int{}, 1)
			releaseFirst := b.lockGate("a")
			taken := make(chan func(), 1)
			go func() { taken <- b.lockGate(c.second) }()
			//: the same key: not taken while the first holds it.
			if c.wantWait {
				select {
				//: taken anyway: the gate orders nothing.
				case <-taken:
					t.Fatalf("a second write of %q took the gate the first holds", c.second)
				//: still waiting, as it must.
				case <-time.After(20 * time.Millisecond):
				}
				releaseFirst()
			}
			select {
			//: taken: give it back.
			case releaseSecond := <-taken:
				releaseSecond()
			//: never taken: it waited for a gate another key holds.
			case <-time.After(10 * time.Second):
				t.Fatalf("the write of %q never took its gate", c.second)
			}
			//: another key: the first is still held, and given back now.
			if !c.wantWait {
				releaseFirst()
			}
			//: nobody holds or waits for a gate any more: none is kept.
			if n := len(b.gates.byKey); n != 0 {
				t.Fatalf("%d gates kept once nobody holds one", n)
			}
		}
	}
	//: each case on its own.
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}
