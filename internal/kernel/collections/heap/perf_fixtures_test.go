//go:build !race

// The fixtures of the indexed heap's performance contracts (design/sdk.yaml,
// budgets): each sets one call up on a heap already in steady state and
// returns it, and the gen_perf_test.go kit gen writes beside this file counts
// its allocations against its budget — zero, since every array the heap grows
// keeps its capacity and a freed id is reused before a new one is made. Race
// off: the allocation lane runs it.
package heap_test

import (
	"cmp"
	"testing"

	"github.com/kitsunium/sdk/internal/kernel/collections/heap"
)

// perfPopulation is the steady population every fixture holds.
const perfPopulation int = 1024

// perfIndexed returns a heap of perfPopulation elements and their handles.
func perfIndexed() (*heap.IndexedHeap[int], []heap.Handle) {
	h := heap.NewIndexed(cmp.Compare[int])
	handles := make([]heap.Handle, perfPopulation)
	for i := range handles {
		handles[i] = h.Push(i * 3 % perfPopulation)
	}
	return h, handles
}

// perfIndexedPushPop is IndexedHeap.Push's fixture: a push and the pop that
// keeps the population steady.
func perfIndexedPushPop(testing.TB) func() {
	h, _ := perfIndexed()
	n := 0
	return func() {
		n++
		h.Push(n % perfPopulation)
		h.Pop()
	}
}

// perfIndexedUpdate is IndexedHeap.Update's fixture: one element re-keyed.
func perfIndexedUpdate(testing.TB) func() {
	h, handles := perfIndexed()
	n := 0
	return func() {
		n++
		h.Update(handles[n%perfPopulation], n%perfPopulation)
	}
}

// perfIndexedRemove is IndexedHeap.Remove's fixture: one element removed and
// pushed back, its new handle kept.
func perfIndexedRemove(testing.TB) func() {
	h, handles := perfIndexed()
	n := 0
	return func() {
		n++
		at := n % perfPopulation
		h.Remove(handles[at])
		handles[at] = h.Push(at)
	}
}
