package heap_test

import (
	"cmp"
	"fmt"
	"slices"
	"testing"

	"github.com/kitsunium/sdk/pkg/v1/collections/heap"
)

// drain pops h until it is empty and returns what came out, in order.
func drain[T any](h *heap.Heap[T]) []T {
	var out []T
	for {
		v, ok := h.Pop()
		if !ok {
			return out
		}
		out = append(out, v)
	}
}

// TestPopReturnsTheOrderTheComparisonDefines asserts the order through the
// public alias: cmp.Compare gives a min-heap, the same comparison with its
// arguments swapped a max-heap, and Peek shows what Pop will return without
// removing it.
func TestPopReturnsTheOrderTheComparisonDefines(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		cmp  func(a, b int) int
		want []int
	}{
		{"cmp.Compare is a min-heap", cmp.Compare[int], []int{1, 2, 3, 5, 8, 9}},
		{"the arguments swapped is a max-heap", func(a, b int) int { return cmp.Compare(b, a) }, []int{9, 8, 5, 3, 2, 1}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h := heap.New(tc.cmp)
			for _, v := range []int{5, 9, 1, 8, 2, 3} {
				h.Push(v)
			}
			if top, ok := h.Peek(); !ok || top != tc.want[0] || h.Len() != len(tc.want) {
				t.Errorf("Peek = %d, %v with Len %d; want %d, true with Len %d", top, ok, h.Len(), tc.want[0], len(tc.want))
			}
			if got := drain(h); !slices.Equal(got, tc.want) {
				t.Errorf("popped %v, want %v", got, tc.want)
			}
		})
	}
}

// TestAnEmptyHeapAnswersFalse asserts that emptiness is an answer, not a
// panic: Pop and Peek on an empty heap return the zero value and false.
func TestAnEmptyHeapAnswersFalse(t *testing.T) {
	t.Parallel()
	h := heap.New(cmp.Compare[string])
	if v, ok := h.Pop(); ok || v != "" {
		t.Errorf("Pop on an empty heap = %q, %v; want \"\", false", v, ok)
	}
	if v, ok := h.Peek(); ok || v != "" {
		t.Errorf("Peek on an empty heap = %q, %v; want \"\", false", v, ok)
	}
}

// TestANilComparisonPanicsAtNew asserts the refusal lands at the call that
// made the mistake.
func TestANilComparisonPanicsAtNew(t *testing.T) {
	t.Parallel()
	defer func() {
		if recover() == nil {
			t.Error("New(nil) did not panic")
		}
	}()
	heap.New[int](nil)
}

// ExampleNew keeps tasks by priority, the most urgent first.
func ExampleNew() {
	type task struct {
		name     string
		priority int
	}
	queue := heap.New(func(a, b task) int { return cmp.Compare(b.priority, a.priority) })
	queue.Push(task{"write report", 2})
	queue.Push(task{"fix outage", 9})
	queue.Push(task{"reply to email", 5})
	for queue.Len() > 0 {
		next, _ := queue.Pop()
		fmt.Println(next.name)
	}
	// Output:
	// fix outage
	// reply to email
	// write report
}
