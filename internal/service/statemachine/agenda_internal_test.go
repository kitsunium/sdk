// Package statemachine — white-box tests of the agenda: lazy deletion, the
// rebuild that bounds it, the order take hands keys out in, and the backoff.
package statemachine

import (
	"slices"
	"testing"
	"time"

	"github.com/kitsunium/sdk/internal/service/resilience"
)

// t0 is the instant the agenda tests schedule from.
var t0 = time.Date(2026, 9, 24, 9, 0, 0, 0, time.UTC)

// TestARescheduleLeavesAStaleEntryThatIsDroppedWhenItSurfaces pins lazy
// deletion: moving an entity's instant pushes a fresh entry, and the old one
// is recognised and dropped rather than taken.
func TestARescheduleLeavesAStaleEntryThatIsDroppedWhenItSurfaces(t *testing.T) {
	t.Parallel()
	a := newAgenda()
	a.schedule("k", t0.Add(time.Minute))
	a.schedule("k", t0.Add(time.Hour))
	if a.heap.Len() != 2 || a.live != 1 {
		t.Fatalf("heap %d entries, %d live; want the stale one kept until it surfaces", a.heap.Len(), a.live)
	}
	if keys := a.take(t0.Add(30 * time.Minute)); len(keys) != 0 {
		t.Fatalf("take() = %v: a stale entry was taken", keys)
	}
	if next := a.next(t0); !next.Equal(t0.Add(time.Hour)) {
		t.Errorf("next() = %v; want the live entry's instant", next)
	}
	a.unschedule("k")
	if next := a.next(t0); !next.IsZero() || a.live != 0 {
		t.Errorf("after unschedule, next() = %v with %d live", next, a.live)
	}
}

// TestTheHeapIsRebuiltBeforeStaleEntriesOutgrowItsBound pins the memory bound:
// rescheduling one entity many times never lets the heap hold more than
// staleFactor times its live entries plus the slack.
func TestTheHeapIsRebuiltBeforeStaleEntriesOutgrowItsBound(t *testing.T) {
	t.Parallel()
	a := newAgenda()
	a.schedule("other", t0.Add(24*time.Hour))
	for i := range 10_000 {
		a.schedule("k", t0.Add(time.Duration(i)*time.Second))
		if bound := staleFactor*a.live + compactSlack + 1; a.heap.Len() > bound {
			t.Fatalf("after %d reschedules the heap holds %d entries, over %d", i+1, a.heap.Len(), bound)
		}
	}
	if keys := a.take(t0.Add(48 * time.Hour)); !slices.Equal(keys, []string{"k", "other"}) {
		t.Errorf("take() = %v; each live entry exactly once, in due order", keys)
	}
}

// TestTakeHandsOutDueKeysInOrderThenDirtyOnesEachOnce pins the order and the
// deduplication of a key both due and written.
func TestTakeHandsOutDueKeysInOrderThenDirtyOnesEachOnce(t *testing.T) {
	t.Parallel()
	a := newAgenda()
	a.schedule("late", t0.Add(2*time.Minute))
	a.schedule("early", t0.Add(time.Minute))
	a.schedule("future", t0.Add(time.Hour))
	a.markDirty("zeta")
	a.markDirty("late")
	a.markDirty("alpha")
	if next := a.next(t0); !next.Equal(t0) {
		t.Errorf("next() with dirty keys = %v; want now", next)
	}
	want := []string{"early", "late", "alpha", "zeta"}
	if keys := a.take(t0.Add(5 * time.Minute)); !slices.Equal(keys, want) {
		t.Fatalf("take() = %v; want %v", keys, want)
	}
	if keys := a.take(t0.Add(5 * time.Minute)); len(keys) != 0 {
		t.Errorf("a second take() = %v; both sets were consumed", keys)
	}
	if next := a.next(t0); !next.Equal(t0.Add(time.Hour)) {
		t.Errorf("next() = %v", next)
	}
}

// TestAFailureHoldsItsKeyBackUntilTheBackoffEnds pins the per-key backoff and
// that a success or a new state clears it.
func TestAFailureHoldsItsKeyBackUntilTheBackoffEnds(t *testing.T) {
	t.Parallel()
	a := newAgenda()
	backoff := resilience.BackoffValue{BaseDelay: time.Second, MaxDelay: time.Minute}
	a.failed("k", t0, backoff)
	a.failed("k", t0, backoff)
	if !a.heldBack("k", t0.Add(time.Second)) || a.heldBack("k", t0.Add(2*time.Second)) {
		t.Error("two failures must hold the key back two seconds")
	}
	a.schedule("k", t0)
	if next := a.next(t0); !next.Equal(t0.Add(2 * time.Second)) {
		t.Errorf("a schedule earlier than the backoff = %v; want it clamped", next)
	}
	a.succeeded("k")
	if a.heldBack("k", t0) {
		t.Error("a success must clear the backoff")
	}
	a.forget("k")
	if _, ok := a.plans["k"]; ok || a.live != 0 {
		t.Error("forget left a plan behind")
	}
}
