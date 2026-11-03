package heap_test

import (
	"cmp"
	stdheap "container/heap"
	"math/rand/v2"
	"testing"

	"github.com/kitsunium/sdk/internal/kernel/collections/heap"
)

// benchSize is the population every benchmark holds. It is large enough that a
// push or a pop really walks log2(4096) = 12 levels, and small enough to stay
// close to the CPU so the numbers measure the algorithm rather than the memory
// system.
const benchSize int = 4096

// intSlice is the container/heap adapter the generic version replaces: five
// methods, an `any` on the way in, a type assertion on the way out. It is here
// so the comparison in BENCH.md is an executed measurement rather than a claim.
type intSlice []int

func (s *intSlice) Len() int           { return len(*s) }
func (s *intSlice) Less(i, j int) bool { return (*s)[i] < (*s)[j] }
func (s *intSlice) Swap(i, j int)      { (*s)[i], (*s)[j] = (*s)[j], (*s)[i] }

func (s *intSlice) Push(v any) {
	//: the boxing the generic version does not do.
	*s = append(*s, v.(int))
}

func (s *intSlice) Pop() any {
	old := *s
	last := len(old) - 1
	popped := old[last]
	*s = old[:last]
	//: and the assertion the caller has to write on the way back out.
	return popped
}

// values is a fixed pseudo-random sequence shared by every benchmark, so every
// comparison below is over identical input.
func values() []int {
	random := rand.New(rand.NewPCG(1, 2))
	out := make([]int, benchSize)
	for i := range out {
		out[i] = random.IntN(1 << 20)
	}
	return out
}

// BenchmarkFill builds a heap of benchSize elements from empty. One reported
// operation is one whole fill, so it includes the amortised cost of the backing
// array doubling — which is most of the allocation this primitive ever does.
func BenchmarkFill(b *testing.B) {
	input := values()
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		h := heap.New(cmp.Compare[int])
		for _, v := range input {
			h.Push(v)
		}
	}
}

// BenchmarkFillAndDrain is the batch shape: fill, then empty. Subtracting
// BenchmarkFill gives the cost of the benchSize sift-downs.
func BenchmarkFillAndDrain(b *testing.B) {
	input := values()
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		h := heap.New(cmp.Compare[int])
		for _, v := range input {
			h.Push(v)
		}
		for h.Len() > 0 {
			h.Pop()
		}
	}
}

// BenchmarkPushPop_SteadyState is the number that matters for a priority queue
// in service: a population held at benchSize, one insert and one extraction per
// operation, both walking the full depth.
func BenchmarkPushPop_SteadyState(b *testing.B) {
	input := values()
	h := heap.New(cmp.Compare[int])
	for _, v := range input {
		h.Push(v)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := range b.N {
		h.Push(input[i%benchSize])
		if _, ok := h.Pop(); !ok {
			b.Fatal("Pop() on a populated heap reported empty")
		}
	}
}

// BenchmarkBaseline_ContainerHeap_PushPop is the same steady state through
// container/heap and the adapter above. The delta is what the pre-generics
// interface costs: a boxed `any` per push, a type assertion per pop, and an
// interface method call per comparison.
func BenchmarkBaseline_ContainerHeap_PushPop(b *testing.B) {
	input := values()
	queue := new(make(intSlice, 0, benchSize))
	//: the same starting population as the generic benchmark above.
	for _, v := range input {
		stdheap.Push(queue, v)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := range b.N {
		stdheap.Push(queue, input[i%benchSize])
		if popped := stdheap.Pop(queue); popped == nil {
			b.Fatal("Pop() on a populated heap returned nil")
		}
	}
}

// BenchmarkPeek is the pure read. It is here so Pop's sift-down cost can be
// read by difference rather than guessed at.
func BenchmarkPeek(b *testing.B) {
	h := heap.New(cmp.Compare[int])
	for _, v := range values() {
		h.Push(v)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		if _, ok := h.Peek(); !ok {
			b.Fatal("Peek() on a populated heap reported empty")
		}
	}
}

// indexedFilled is an IndexedHeap holding the shared input, with the handle of
// every element, for the steady-state benchmarks below.
func indexedFilled(input []int) (*heap.IndexedHeap[int], []heap.Handle) {
	h := heap.NewIndexed(cmp.Compare[int])
	handles := make([]heap.Handle, len(input))
	for i, v := range input {
		handles[i] = h.Push(v)
	}
	return h, handles
}

// BenchmarkIndexed_PushPop_SteadyState is PushPop_SteadyState on the indexed
// sibling: the delta is what keeping the position table in step costs a sift.
func BenchmarkIndexed_PushPop_SteadyState(b *testing.B) {
	input := values()
	h, _ := indexedFilled(input)
	b.ReportAllocs()
	b.ResetTimer()
	for i := range b.N {
		h.Push(input[i%benchSize])
		if _, _, ok := h.Pop(); !ok {
			b.Fatal("Pop() on a populated heap reported empty")
		}
	}
}

// BenchmarkIndexed_Update_SteadyState re-keys one element in place — what a
// reschedule costs, against the Push of a fresh entry plus the later Pop of
// the stale one that lazy deletion pays.
func BenchmarkIndexed_Update_SteadyState(b *testing.B) {
	input := values()
	h, handles := indexedFilled(input)
	b.ReportAllocs()
	b.ResetTimer()
	for i := range b.N {
		if !h.Update(handles[i%benchSize], input[(i*7)%benchSize]) {
			b.Fatal("Update of a live handle reported false")
		}
	}
}

// BenchmarkIndexed_Remove_SteadyState removes one element from anywhere and
// pushes it back, so the population stays at benchSize: a cancellation, which
// lazy deletion cannot do at all.
func BenchmarkIndexed_Remove_SteadyState(b *testing.B) {
	input := values()
	h, handles := indexedFilled(input)
	b.ReportAllocs()
	b.ResetTimer()
	for i := range b.N {
		at := i % benchSize
		if _, ok := h.Remove(handles[at]); !ok {
			b.Fatal("Remove of a live handle reported false")
		}
		handles[at] = h.Push(input[at])
	}
}

// BenchmarkDrain measures Drain over benchSize elements (#816): one reported
// operation is one whole drain, the refill done with the timer stopped. The
// refill reuses the backing array, so the timed window holds the drain alone
// and its allocations — none.
func BenchmarkDrain(b *testing.B) {
	input := values()
	h := heap.New(cmp.Compare[int])
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		b.StopTimer()
		for _, v := range input {
			h.Push(v)
		}
		b.StartTimer()
		drained := 0
		for range h.Drain() {
			drained++
		}
		if drained != benchSize {
			b.Fatalf("Drain() yielded %d elements, want %d", drained, benchSize)
		}
	}
}
