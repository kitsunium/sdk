package heap

import (
	"cmp"
	"math/rand/v2"
	"testing"
)

// TestPopClearsTheVacatedSlot pins a leak that no black-box test can see: the
// backing array outlives the slice header, so a popped pointer that is not
// cleared stays reachable — and keeps whatever it points at alive — until the
// array is reallocated or the heap is collected.
func TestPopClearsTheVacatedSlot(t *testing.T) {
	t.Parallel()
	type payload struct{ n int }
	h := New(func(a, b *payload) int { return cmp.Compare(a.n, b.n) })
	h.Push(&payload{1})
	h.Push(&payload{2})
	h.Push(&payload{3})
	for range 3 {
		if _, ok := h.Pop(); !ok {
			t.Fatal("Pop() reported empty before the heap drained")
		}
	}
	//: the slice is empty, but the array behind it still has three slots.
	tail := h.items[:cap(h.items)]
	for i, slot := range tail {
		if slot != nil {
			t.Fatalf("backing array slot %d still holds %+v — a popped element must not stay reachable", i, slot)
		}
	}
}

// TestTheInvariantHoldsAfterEveryOperation walks the whole array rather than
// only observing the top, so a sift bug that happens to leave the root correct
// still fails here.
func TestTheInvariantHoldsAfterEveryOperation(t *testing.T) {
	t.Parallel()
	random := rand.New(rand.NewPCG(42, 1024))
	h := New(cmp.Compare[int])
	for step := range 500 {
		if step%3 == 2 && h.Len() > 0 {
			h.Pop()
		} else {
			h.Push(random.IntN(200))
		}
		assertInvariant(t, h, step)
	}
	for h.Len() > 0 {
		h.Pop()
		assertInvariant(t, h, -1)
	}
}

// assertInvariant checks that no child sorts before its parent.
func assertInvariant(t *testing.T, h *Heap[int], step int) {
	t.Helper()
	for child := 1; child < len(h.items); child++ {
		parent := (child - 1) / 2
		if h.cmp(h.items[child], h.items[parent]) < 0 {
			t.Fatalf("step %d: items[%d]=%d sorts before its parent items[%d]=%d", step, child, h.items[child], parent, h.items[parent])
		}
	}
}

// TestNewLeavesTheArrayNil keeps construction free: a heap that is built and
// never used must not have paid for a backing array.
func TestNewLeavesTheArrayNil(t *testing.T) {
	t.Parallel()
	h := New(cmp.Compare[int])
	if h.items != nil {
		t.Fatalf("items = %v, want nil until the first Push", h.items)
	}
}

// TestDownPicksTheChildThatSortsFirst covers the branch that chooses between
// two children — the one a heap with a single child per level never reaches.
func TestDownPicksTheChildThatSortsFirst(t *testing.T) {
	t.Parallel()
	h := New(cmp.Compare[int])
	//: a hand-built array whose root is out of place and whose RIGHT child is
	//: the smaller one, so a sift-down that always takes the left child fails.
	h.items = []int{100, 50, 10, 60, 70, 20, 30}
	h.down(0)
	if h.items[0] != 10 {
		t.Fatalf("after down(0) the top is %d, want 10 — the right child was the smaller one", h.items[0])
	}
	assertInvariant(t, h, 0)
}
