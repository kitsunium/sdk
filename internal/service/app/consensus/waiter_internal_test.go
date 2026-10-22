package consensus

import (
	"testing"
	"time"

	coreconsensus "github.com/kitsunium/sdk/internal/core/app/consensus"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// TestAppendedNeverOrphansAWaiterAtTheSameIndex is #517: after a leader
// change the new leader hands out first the indexes the old one's
// unreplicated tail held, so a node can learn a second position at an index
// it already waits on. The waiter of the earlier term was overwritten and
// answered by nothing, its local pending bytes counted for good. It is
// answered OUTCOME_UNKNOWN at once, its bytes released, and the waiter of
// the later term is the one kept, whichever arrives first.
func TestAppendedNeverOrphansAWaiterAtTheSameIndex(t *testing.T) {
	c := newCluster(t, 1, 0)
	n := c.leader(5 * time.Second).node
	for _, order := range []string{"earlier first", "later first"} {
		for _, local := range []bool{false, true} {
			start := n.pendingBytes.Load()
			early := &proposal{entry: coreconsensus.EntryValue{Data: []byte("early=1")}, done: make(chan propResult, 1)}
			late := &proposal{entry: coreconsensus.EntryValue{Data: []byte("late=12")}, done: make(chan propResult, 1)}
			var idx uint64
			n.exec(func() {
				n.am.Lock()
				idx = n.applied + 1000
				n.am.Unlock()
				first, second := early, late
				fp, sp := coreconsensus.PositionValue{Term: 5, Index: idx}, coreconsensus.PositionValue{Term: 6, Index: idx}
				if order == "later first" {
					first, second, fp, sp = late, early, sp, fp
				}
				n.appended(first, fp, local)
				n.appended(second, sp, local)
			})
			select {
			case r := <-early.done:
				if !errs.HasCode(r.err, coreconsensus.CodeOutcomeUnknown) {
					t.Fatalf("%s, local %v: the displaced waiter was answered %v, want OUTCOME_UNKNOWN", order, local, r.err)
				}
			case <-time.After(time.Second):
				t.Fatalf("%s, local %v: the waiter of the earlier term was never answered", order, local)
			}
			n.am.Lock()
			kept := n.waiters[idx]
			delete(n.waiters, idx)
			n.am.Unlock()
			if kept != late {
				t.Fatalf("%s, local %v: the waiter kept is not the later term's", order, local)
			}
			if local {
				// What applied1 would release for the waiter kept.
				n.pendingBytes.Add(-int64(len(late.entry.Data)))
			}
			if got := n.pendingBytes.Load(); got != start {
				t.Fatalf("%s, local %v: pending bytes %d once both are answered, want %d", order, local, got, start)
			}
		}
	}
}
