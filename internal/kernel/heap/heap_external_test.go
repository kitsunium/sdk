package heap_test

import (
	"cmp"
	"math/rand/v2"
	"slices"
	"strings"
	"testing"

	"github.com/kitsunium/sdk/internal/kernel/heap"
)

// TestPopReturnsElementsInComparatorOrder is the primitive's whole contract:
// whatever order values went in, they come out ordered by cmp.
func TestPopReturnsElementsInComparatorOrder(t *testing.T) {
	t.Parallel()
	h := heap.New(cmp.Compare[int])
	for _, v := range []int{5, 3, 9, 1, 7, 0, 8, 2, 6, 4} {
		h.Push(v)
	}
	for want := range 10 {
		got, ok := h.Pop()
		if !ok {
			t.Fatalf("Pop() reported empty at %d, want %d more", want, 10-want)
		}
		if got != want {
			t.Fatalf("Pop() = %d, want %d", got, want)
		}
	}
	if _, ok := h.Pop(); ok {
		t.Fatal("Pop() on a drained heap reported a value")
	}
}

// TestReversingTheComparatorGivesAMaxHeap documents the only knob there is:
// the element that sorts FIRST is on top, so a max-heap is the same function
// with its arguments swapped — no second type, no flag.
func TestReversingTheComparatorGivesAMaxHeap(t *testing.T) {
	t.Parallel()
	h := heap.New(func(a, b int) int { return cmp.Compare(b, a) })
	for _, v := range []int{5, 3, 9, 1, 7} {
		h.Push(v)
	}
	want := []int{9, 7, 5, 3, 1}
	for _, expected := range want {
		got, ok := h.Pop()
		if !ok || got != expected {
			t.Fatalf("Pop() = %d, %v; want %d, true", got, ok, expected)
		}
	}
}

// TestPopOnAnEmptyHeapIsAStateNotAPanic pins the (T, bool) shape. Emptiness is
// an ordinary state of a queue, and the SDK already spells "no value here" as a
// second return (kernel/cache.Fetch), not as a panic and not as a sentinel.
func TestPopOnAnEmptyHeapIsAStateNotAPanic(t *testing.T) {
	t.Parallel()
	h := heap.New(cmp.Compare[string])
	got, ok := h.Pop()
	if ok {
		t.Fatalf("Pop() on an empty heap = %q, true; want the zero value and false", got)
	}
	if got != "" {
		t.Fatalf("Pop() value = %q, want the zero value", got)
	}
	if _, ok := h.Peek(); ok {
		t.Fatal("Peek() on an empty heap reported a value")
	}
	if h.Len() != 0 {
		t.Fatalf("Len() = %d, want 0", h.Len())
	}
}

// TestPeekDoesNotMutate is the reason Peek keeps the name Get would have
// deserved and Fetch had to give up in kernel/cache: this really is a pure
// read.
func TestPeekDoesNotMutate(t *testing.T) {
	t.Parallel()
	h := heap.New(cmp.Compare[int])
	for _, v := range []int{4, 2, 8} {
		h.Push(v)
	}
	for range 3 {
		got, ok := h.Peek()
		if !ok || got != 2 {
			t.Fatalf("Peek() = %d, %v; want 2, true", got, ok)
		}
		if h.Len() != 3 {
			t.Fatalf("Len() after Peek = %d, want 3", h.Len())
		}
	}
}

// TestNewWithoutAComparisonRefusesAtTheCallThatMadeTheMistake is ADR 0031's
// refusal branch. There is no order the SDK can invent for an arbitrary T, so
// there is nothing to clamp to; the refusal lands on the line with the bug
// rather than on the first Push, several frames away.
func TestNewWithoutAComparisonRefusesAtTheCallThatMadeTheMistake(t *testing.T) {
	t.Parallel()
	defer func() {
		raised := recover()
		if raised == nil {
			t.Fatal("New(nil) returned a heap, want a refusal")
		}
		message, ok := raised.(string)
		if !ok {
			t.Fatalf("recovered %T, want a string message", raised)
		}
		if !strings.Contains(message, "kernel/heap: New requires a comparison function") {
			t.Fatalf("panic message = %q, want it to name the missing argument", message)
		}
	}()
	heap.New[int](nil)
}

// TestARandomisedSequenceMatchesASortedSlice is the property test: any
// interleaving of pushes and pops must produce the same order a sort would.
// Fixed seeds keep it reproducible while still covering shapes a hand-written
// case would not reach.
func TestARandomisedSequenceMatchesASortedSlice(t *testing.T) {
	t.Parallel()
	for seed := range uint64(16) {
		random := rand.New(rand.NewPCG(seed, seed+1))
		h := heap.New(cmp.Compare[int])
		var mirror []int
		for range 400 {
			//: two pushes for every pop, so the heap grows and still drains.
			if random.IntN(3) == 0 && len(mirror) > 0 {
				slices.Sort(mirror)
				want := mirror[0]
				mirror = mirror[1:]
				got, ok := h.Pop()
				if !ok || got != want {
					t.Fatalf("seed %d: Pop() = %d, %v; want %d, true", seed, got, ok, want)
				}
				continue
			}
			v := random.IntN(1000)
			h.Push(v)
			mirror = append(mirror, v)
		}
		if h.Len() != len(mirror) {
			t.Fatalf("seed %d: Len() = %d, want %d", seed, h.Len(), len(mirror))
		}
		slices.Sort(mirror)
		for _, want := range mirror {
			got, _ := h.Pop()
			if got != want {
				t.Fatalf("seed %d: drain gave %d, want %d", seed, got, want)
			}
		}
	}
}

// TestAHeapOfStructsOrdersByWhateverTheCallerChose is the kernel rule made
// executable: the primitive knows nothing about what T means. The struct here
// carries a deadline and a name, and the heap orders it without either word
// appearing in its API.
func TestAHeapOfStructsOrdersByWhateverTheCallerChose(t *testing.T) {
	t.Parallel()
	type record struct {
		name string
		rank int
	}
	h := heap.New(func(a, b record) int { return cmp.Compare(a.rank, b.rank) })
	for _, r := range []record{{"c", 3}, {"a", 1}, {"b", 2}} {
		h.Push(r)
	}
	for _, want := range []string{"a", "b", "c"} {
		got, ok := h.Pop()
		if !ok || got.name != want {
			t.Fatalf("Pop() = %+v, %v; want name %q", got, ok, want)
		}
	}
}

// TestTiesComeOutInAnUnspecifiedOrderButAllOfThem states the non-guarantee out
// loud. A binary heap is not stable; what IS guaranteed is that no element is
// lost or duplicated, which is the part a caller can rely on.
func TestTiesComeOutInAnUnspecifiedOrderButAllOfThem(t *testing.T) {
	t.Parallel()
	type record struct {
		id   int
		rank int
	}
	h := heap.New(func(a, b record) int { return cmp.Compare(a.rank, b.rank) })
	for id := range 50 {
		h.Push(record{id: id, rank: 0})
	}
	seen := make(map[int]bool, 50)
	for range 50 {
		got, ok := h.Pop()
		if !ok {
			t.Fatal("Pop() reported empty before the heap drained")
		}
		if seen[got.id] {
			t.Fatalf("id %d popped twice", got.id)
		}
		seen[got.id] = true
	}
	if len(seen) != 50 {
		t.Fatalf("saw %d distinct elements, want 50", len(seen))
	}
}

// TestInterleavedPushAndPopKeepsTheTopCorrect covers the sift-down path that a
// pure fill-then-drain sequence never exercises deeply.
func TestInterleavedPushAndPopKeepsTheTopCorrect(t *testing.T) {
	t.Parallel()
	h := heap.New(cmp.Compare[int])
	h.Push(10)
	h.Push(20)
	if got, _ := h.Pop(); got != 10 {
		t.Fatalf("Pop() = %d, want 10", got)
	}
	h.Push(5)
	h.Push(15)
	if got, _ := h.Peek(); got != 5 {
		t.Fatalf("Peek() = %d, want 5", got)
	}
	for _, want := range []int{5, 15, 20} {
		got, _ := h.Pop()
		if got != want {
			t.Fatalf("Pop() = %d, want %d", got, want)
		}
	}
}
