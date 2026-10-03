//go:generate gomarkdoc --output README.md --repository.url https://github.com/kitsunium/sdk --repository.default-branch main --repository.path /pkg/v1/collections/heap .

// Package heap is a binary heap of any element type, ordered by a comparison
// you give it: Push in O(log n), Pop the element that sorts first in
// O(log n), Peek at it in O(1).
//
//	due := heap.New(func(a, b Timer) int { return a.At.Compare(b.At) })
//	due.Push(Timer{At: later})
//	due.Push(Timer{At: sooner})
//	next, ok := due.Pop() // the sooner one
//
// It is the priority queue the SDK's state machine keeps its agenda in, and
// its in-memory queue broker its lease expiries, published as an alias of that
// kernel package (ADR 0159 §4).
//
// # When to use it
//
// Whenever the next element to handle is the smallest by some order — the
// timer due soonest, the job with the highest priority, the k largest values
// of a stream. container/heap provides the algorithm, over five methods a
// caller writes on a named slice type, with Push taking any and Pop returning
// any: the same boilerplate at every use, every element boxed on the way in
// and type-asserted on the way out. Here the cost is one comparison function
// at construction, and nothing at the call site.
//
// # Order
//
// The comparison follows the cmp.Compare and slices.SortFunc convention:
// negative when a sorts before b, zero when they tie, positive otherwise. The
// element that sorts FIRST is at the top, so cmp.Compare[int] gives a min-heap
// and the same function with its arguments swapped a max-heap. Elements that
// tie pop in no particular order — a binary heap is not stable.
//
// # Empty is an answer
//
// Pop and Peek on an empty heap return the zero value and false: emptiness is
// an ordinary state of a queue. A nil comparison panics at [New] — there is no
// order to invent for an arbitrary type.
//
// # Concurrency
//
// A Heap is NOT safe for concurrent use. That is deliberate: a lock inside
// would be paid by every caller, including the many that keep a heap inside a
// structure they already lock. The caller picks the lock, or needs none.
package heap

import (
	kheap "github.com/kitsunium/sdk/internal/kernel/collections/heap"
)

// Heap is a binary heap ordered by the comparison given to [New].
//
// [Heap].Push adds an element in O(log n); [Heap].Pop removes and returns the
// top one in O(log n), and [Heap].Peek returns it without removing it, both
// reporting false on an empty heap; [Heap].Len counts the elements.
//
// The zero value is NOT usable — a heap with no comparison has no order — and
// a Heap is not safe for concurrent use.
type Heap[T any] = kheap.Heap[T]

// New builds an empty [Heap] ordered by cmp, which returns a negative number
// when a sorts before b, zero on a tie and a positive one otherwise. A nil cmp
// panics here, at the call that made the mistake.
func New[T any](cmp func(a, b T) int) *Heap[T] {
	//: the kernel owns the heap; this facade only forwards.
	return kheap.New(cmp)
}
