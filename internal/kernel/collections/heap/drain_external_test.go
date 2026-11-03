package heap_test

import (
	"cmp"
	"slices"
	"testing"

	"github.com/kitsunium/sdk/internal/kernel/collections/heap"
)

// drainHeap returns a min-heap of the given values.
func drainHeap(values ...int) *heap.Heap[int] {
	h := heap.New(cmp.Compare[int])
	for _, v := range values {
		h.Push(v)
	}
	return h
}

// TestDrainYieldsEveryElementInOrder pins #816's Drain: every element, in the
// heap's order, and an empty heap afterwards.
func TestDrainYieldsEveryElementInOrder(t *testing.T) {
	t.Parallel()
	h := drainHeap(5, 1, 4, 2, 3, 1)
	if got, want := slices.Collect(h.Drain()), []int{1, 1, 2, 3, 4, 5}; !slices.Equal(got, want) {
		t.Fatalf("Drain() yielded %v, want %v", got, want)
	}
	if n := h.Len(); n != 0 {
		t.Fatalf("Len() after a whole drain = %d, want 0", n)
	}
	if got := slices.Collect(h.Drain()); len(got) != 0 {
		t.Fatalf("Drain() of an empty heap yielded %v, want nothing", got)
	}
}

// TestDrainStoppedEarlyLeavesTheRest pins the early stop: what was yielded is
// gone, what was not stays in the heap in order.
func TestDrainStoppedEarlyLeavesTheRest(t *testing.T) {
	t.Parallel()
	h := drainHeap(5, 1, 4, 2, 3)
	var seen []int
	for v := range h.Drain() {
		seen = append(seen, v)
		if len(seen) == 2 {
			break
		}
	}
	if want := []int{1, 2}; !slices.Equal(seen, want) {
		t.Fatalf("the first two yielded %v, want %v", seen, want)
	}
	if got, want := slices.Collect(h.Drain()), []int{3, 4, 5}; !slices.Equal(got, want) {
		t.Fatalf("the rest drained as %v, want %v", got, want)
	}
}

// TestDrainYieldsWhatTheBodyPushes pins that each step pops the heap as it is
// then: an element the loop's body pushes is yielded in its turn.
func TestDrainYieldsWhatTheBodyPushes(t *testing.T) {
	t.Parallel()
	h := drainHeap(1, 3, 5)
	var seen []int
	for v := range h.Drain() {
		seen = append(seen, v)
		if v == 1 {
			h.Push(2)
		}
	}
	if want := []int{1, 2, 3, 5}; !slices.Equal(seen, want) {
		t.Fatalf("Drain() with a push in its body yielded %v, want %v", seen, want)
	}
}
