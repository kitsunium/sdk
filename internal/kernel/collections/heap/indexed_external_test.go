package heap_test

import (
	"cmp"
	"math/rand/v2"
	"slices"
	"testing"

	"github.com/kitsunium/sdk/internal/kernel/collections/heap"
)

// TestIndexedAgreesWithASortedSliceUnderRandomOperations is the property test:
// random Push, Update, Remove and Pop against a model that is a plain multiset
// kept sorted. Every Pop must return the model's minimum, and every Remove the
// element its handle named.
func TestIndexedAgreesWithASortedSliceUnderRandomOperations(t *testing.T) {
	t.Parallel()
	for seed := range uint64(20) {
		random := rand.New(rand.NewPCG(seed, 7))
		h := heap.NewIndexed(cmp.Compare[int])
		//: the model: the value each live handle names.
		live := map[heap.Handle]int{}
		handles := []heap.Handle{}
		for step := range 2000 {
			switch op := random.IntN(10); {
			case op < 4 || len(handles) == 0:
				v := random.IntN(500)
				handle := h.Push(v)
				live[handle] = v
				handles = append(handles, handle)
			case op < 6:
				at := random.IntN(len(handles))
				v := random.IntN(500)
				if !h.Update(handles[at], v) {
					t.Fatalf("seed %d step %d: Update of a live handle reported false", seed, step)
				}
				live[handles[at]] = v
			case op < 8:
				at := random.IntN(len(handles))
				got, ok := h.Remove(handles[at])
				if !ok || got != live[handles[at]] {
					t.Fatalf("seed %d step %d: Remove = (%d, %v), want (%d, true)", seed, step, got, ok, live[handles[at]])
				}
				delete(live, handles[at])
				handles = slices.Delete(handles, at, at+1)
			default:
				got, handle, ok := h.Pop()
				want := slices.Min(valuesOf(live))
				if !ok || got != want || live[handle] != got {
					t.Fatalf("seed %d step %d: Pop = (%d, %v), want the minimum %d", seed, step, got, ok, want)
				}
				delete(live, handle)
				handles = slices.DeleteFunc(handles, func(x heap.Handle) bool { return x == handle })
			}
			if h.Len() != len(live) {
				t.Fatalf("seed %d step %d: Len = %d, want %d", seed, step, h.Len(), len(live))
			}
		}
		//: drain: the rest comes out sorted.
		want := slices.Sorted(func(yield func(int) bool) {
			for _, v := range live {
				if !yield(v) {
					return
				}
			}
		})
		for i, w := range want {
			got, _, ok := h.Pop()
			if !ok || got != w {
				t.Fatalf("seed %d drain %d: Pop = (%d, %v), want %d", seed, i, got, ok, w)
			}
		}
	}
}

// valuesOf lists a model's values.
func valuesOf(live map[heap.Handle]int) []int {
	out := make([]int, 0, len(live))
	for _, v := range live {
		out = append(out, v)
	}
	return out
}

// TestIndexedStaleHandleNamesNothing pins the generation: once an element left,
// its handle neither updates nor removes anything — not even the element a
// later Push placed in the recycled slot.
func TestIndexedStaleHandleNamesNothing(t *testing.T) {
	t.Parallel()
	h := heap.NewIndexed(cmp.Compare[int])
	first := h.Push(1)
	if got, ok := h.Remove(first); !ok || got != 1 {
		t.Fatalf("Remove(first) = (%d, %v), want (1, true)", got, ok)
	}
	second := h.Push(2)
	if h.Update(first, 0) {
		t.Fatal("Update with a stale handle reported true")
	}
	if _, ok := h.Remove(first); ok {
		t.Fatal("Remove with a stale handle removed the element that reused its slot")
	}
	if got, handle, ok := h.Peek(); !ok || got != 2 || handle != second {
		t.Fatalf("Peek = (%d, %v, %v), want (2, second, true)", got, handle, ok)
	}
	if h.Update(heap.Handle{}, 9) {
		t.Fatal("the zero Handle updated an element")
	}
}

// TestIndexedEmptyAnswersFalse mirrors Heap: emptiness is a state.
func TestIndexedEmptyAnswersFalse(t *testing.T) {
	t.Parallel()
	h := heap.NewIndexed(cmp.Compare[int])
	if _, _, ok := h.Peek(); ok {
		t.Fatal("Peek on an empty heap reported a value")
	}
	if _, _, ok := h.Pop(); ok {
		t.Fatal("Pop on an empty heap reported a value")
	}
	if _, ok := h.Remove(heap.Handle{}); ok {
		t.Fatal("Remove on an empty heap reported a value")
	}
}

// TestIndexedUpdateMovesBothWays checks that an Update re-orders in both
// directions: a key made smaller rises to the top, one made larger sinks.
func TestIndexedUpdateMovesBothWays(t *testing.T) {
	t.Parallel()
	h := heap.NewIndexed(cmp.Compare[int])
	handles := make([]heap.Handle, 0, 10)
	for v := range 10 {
		handles = append(handles, h.Push(v*10))
	}
	h.Update(handles[9], -1)
	if got, handle, _ := h.Peek(); got != -1 || handle != handles[9] {
		t.Fatalf("after lowering the last, Peek = (%d, %v), want (-1, its handle)", got, handle)
	}
	h.Update(handles[9], 1000)
	h.Update(handles[0], 999)
	if got, _, _ := h.Peek(); got != 10 {
		t.Fatalf("after raising the two smallest, Peek = %d, want 10", got)
	}
}

// TestNewIndexedPanicsOnNilComparison mirrors New's refusal.
func TestNewIndexedPanicsOnNilComparison(t *testing.T) {
	t.Parallel()
	defer func() {
		if recover() == nil {
			t.Fatal("NewIndexed(nil) did not panic")
		}
	}()
	heap.NewIndexed[int](nil)
}
