// Package statemachine — white-box tests of the agenda: one heap entry per
// scheduled key, the order take hands keys out in, and the backoff.
package statemachine

import (
	"slices"
	"testing"
	"time"

	kbackoff "github.com/kitsunium/sdk/internal/kernel/backoff"
)

// t0 is the instant the agenda tests schedule from.
var t0 = time.Date(2026, 9, 24, 9, 0, 0, 0, time.UTC)

// TestARescheduleReKeysTheOneEntryOfItsKey pins the indexed heap (#1036):
// moving an entity's instant re-keys its entry in place rather than leaving a
// stale one behind, and unscheduling removes it at once.
func TestARescheduleReKeysTheOneEntryOfItsKey(t *testing.T) {
	t.Parallel()
	a := newAgenda()
	a.schedule("k", t0.Add(time.Minute))
	a.schedule("k", t0.Add(time.Hour))
	if a.heap.Len() != 1 {
		t.Fatalf("heap %d entries; want the one entry of the key, re-keyed", a.heap.Len())
	}
	if keys := a.take(t0.Add(30 * time.Minute)); len(keys) != 0 {
		t.Fatalf("take() = %v: the replaced instant was taken", keys)
	}
	if next := a.next(t0); !next.Equal(t0.Add(time.Hour)) {
		t.Errorf("next() = %v; want the rescheduled instant", next)
	}
	a.unschedule("k")
	if next := a.next(t0); !next.IsZero() || a.heap.Len() != 0 {
		t.Errorf("after unschedule, next() = %v with %d entries", next, a.heap.Len())
	}
}

// TestTheHeapHoldsOneEntryPerScheduledKey pins the memory bound the rebuild
// used to approximate: rescheduling one entity many times never grows the
// heap beyond one entry per scheduled key.
func TestTheHeapHoldsOneEntryPerScheduledKey(t *testing.T) {
	t.Parallel()
	a := newAgenda()
	a.schedule("other", t0.Add(24*time.Hour))
	for i := range 10_000 {
		a.schedule("k", t0.Add(time.Duration(i)*time.Second))
		if a.heap.Len() != 2 {
			t.Fatalf("after %d reschedules the heap holds %d entries, want 2", i+1, a.heap.Len())
		}
	}
	if keys := a.take(t0.Add(48 * time.Hour)); !slices.Equal(keys, []string{"k", "other"}) {
		t.Errorf("take() = %v; each entry exactly once, in due order", keys)
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
	backoff := kbackoff.Value{BaseDelay: time.Second, MaxDelay: time.Minute}
	a.failed("k", t0, backoff.Delay)
	a.failed("k", t0, backoff.Delay)
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
	if _, ok := a.plans["k"]; ok || a.heap.Len() != 0 {
		t.Error("forget left a plan behind")
	}
}
