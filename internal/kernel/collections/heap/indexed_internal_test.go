package heap

import (
	"cmp"
	"math/rand/v2"
	"testing"
)

// TestIndexedRemoveFromTheMiddleKeepsTheInvariant removes elements from the
// middle of a 1 000-element heap and walks the whole array after each: a hole
// filled by the last leaf may need to move up as well as down, and a sift that
// only goes one way leaves a child above its parent somewhere below the root.
// It also reads the array beyond len, as TestPopClearsTheVacatedSlot does.
func TestIndexedRemoveFromTheMiddleKeepsTheInvariant(t *testing.T) {
	t.Parallel()
	type payload struct{ n int }
	random := rand.New(rand.NewPCG(3, 9))
	h := NewIndexed(func(a, b *payload) int { return cmp.Compare(a.n, b.n) })
	handles := make([]Handle, 0, 1000)
	for range 1000 {
		handles = append(handles, h.Push(&payload{random.IntN(10_000)}))
	}
	for i := 250; i < 750; i++ {
		if _, ok := h.Remove(handles[i]); !ok {
			t.Fatalf("Remove(handles[%d]) reported false", i)
		}
		assertIndexedInvariant(t, h)
	}
	//: the cells beyond len held removed elements: every one must be cleared.
	tail := h.items[len(h.items):cap(h.items)]
	for i, cell := range tail {
		if cell.value != nil {
			t.Fatalf("vacated cell %d still holds %+v — a removed element must not stay reachable", len(h.items)+i, cell.value)
		}
	}
}

// assertIndexedInvariant checks the order and the position table together.
func assertIndexedInvariant[T any](t *testing.T, h *IndexedHeap[T]) {
	t.Helper()
	for child := 1; child < len(h.items); child++ {
		parent := (child - 1) / branching
		if h.cmp(h.items[child].value, h.items[parent].value) < 0 {
			t.Fatalf("heap invariant broken: child %d sorts before parent %d", child, parent)
		}
	}
	for at, cell := range h.items {
		if h.slots[cell.id].at != at {
			t.Fatalf("position table out of step: id %d says %d, array says %d", cell.id, h.slots[cell.id].at, at)
		}
	}
}
