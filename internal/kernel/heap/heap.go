// Package heap provides Heap[T], a binary heap ordered by a comparison
// function the caller supplies. It is a kernel primitive: stdlib-only, and
// domain-neutral down to the signatures — one type parameter and one
// comparison, with no Job, no Task and no Priority anywhere in the API.
//
// # Why not container/heap
//
// container/heap exists and cannot be used cleanly here. It is an ALGORITHM
// over an interface the caller must implement: five methods (Len, Less, Swap,
// Push, Pop) on a named slice type, where Push takes `any` and Pop returns
// `any`. Every use site therefore writes the same boilerplate, boxes each
// element on the way in, and type-asserts it on the way out — a conversion the
// compiler cannot check and the reader has to trust. That was the right design
// in 2011; it predates type parameters.
//
// The generic version costs one comparison function at construction and
// nothing at all at the call site.
//
// # Order
//
// cmp follows the cmp.Compare / slices.SortFunc convention: negative when a
// sorts before b, zero when they tie, positive otherwise. The element that
// sorts FIRST is the one at the top, so cmp.Compare[int] gives a MIN-heap and
// a max-heap is the same function with its arguments reversed. Ties pop in an
// unspecified order — a binary heap is not stable, and no amount of care in
// the comparison makes it so.
//
// # Concurrency
//
// A Heap is NOT safe for concurrent use. That is deliberate: a lock inside
// would be paid by every caller, including the many that hold a heap inside a
// structure they already serialise. The caller picks the lock, or does not
// need one.
package heap

// branching is the heap's arity: every node has at most two children, which is
// what puts the children of i at 2i+1 and 2i+2 and makes the tree implicit in a
// flat array. It is named rather than written twice as a bare 2 because those
// two occurrences are the SAME decision, not two coincidences.
const branching int = 2

// Heap is a binary heap ordered by the comparison given to [New].
//
// The zero value is NOT usable — a heap with no comparison has no order — so
// construct with [New]. Not safe for concurrent use.
type Heap[T any] struct {
	// items is the heap array: the children of i live at 2i+1 and 2i+2, so the
	// tree needs no pointers and no per-element allocation.
	items []T
	// cmp is the caller's order. Non-nil by construction; see New.
	cmp func(a, b T) int
}

// New builds an empty Heap ordered by cmp.
//
// A nil cmp PANICS, at the call that made the mistake. There is no order the
// SDK can invent for an arbitrary T, so ADR 0031's other branch — clamp to a
// working default — has nothing to clamp to; this is the refusal branch. It is
// delivered as a panic rather than an error because the kernel has no error
// budget for programmer faults (recycler and clock take the same position) and
// because the alternative, a Heap that panics on the first Push, moves the
// crash away from the line that caused it.
func New[T any](cmp func(a, b T) int) *Heap[T] {
	//: refuse here, where the bug is, rather than on the first Push.
	if cmp == nil {
		//: no default exists: T is arbitrary and has no intrinsic order.
		panic("kernel/heap: New requires a comparison function — there is no order a heap can invent for an arbitrary T")
	}
	//: items stays nil until the first Push; append allocates it.
	return &Heap[T]{cmp: cmp}
}

// Len reports how many elements the heap holds.
func (h *Heap[T]) Len() int {
	//: the array IS the heap, so its length is the count.
	return len(h.items)
}

// Push adds v, in O(log n).
func (h *Heap[T]) Push(v T) {
	//: append first, then restore the invariant from the new leaf upwards —
	//: the only path that can be violated by adding at the end.
	h.items = append(h.items, v)
	h.up(len(h.items) - 1)
}

// Peek returns the top element without removing it, and reports whether there
// was one. It does not mutate the heap, so unlike a cache read it really is a
// getter.
func (h *Heap[T]) Peek() (T, bool) {
	//: an empty heap has no top; a zero value with false, never a panic.
	if len(h.items) == 0 {
		var zero T
		//: the caller distinguishes "empty" from "the zero value was on top".
		return zero, false
	}
	//: the root is the element that sorts first.
	return h.items[0], true
}

// Pop removes and returns the top element, in O(log n), and reports whether
// there was one.
//
// It returns (zero, false) on an empty heap rather than panicking: emptiness is
// an ordinary state of a queue, not a programmer error, and the (T, bool) shape
// is the same one kernel/cache uses for a miss.
func (h *Heap[T]) Pop() (T, bool) {
	var zero T
	last := len(h.items) - 1
	//: empty is a state, not a fault.
	if last < 0 {
		//: nothing to hand back.
		return zero, false
	}
	top := h.items[0]
	//: move the last leaf to the root and sift it down — the standard
	//: removal, and the reason a heap needs no hole to be filled.
	h.items[0] = h.items[last]
	//: clear the vacated slot: without this a heap of pointers keeps the
	//: popped element alive for as long as the backing array does.
	h.items[last] = zero
	h.items = h.items[:last]
	//: last == 0 means the heap is now empty; there is nothing to sift.
	if last > 0 {
		h.down(0)
	}
	//: the element that sorted first.
	return top, true
}

// up restores the heap invariant along the path from child to the root.
func (h *Heap[T]) up(child int) {
	//: walk towards the root, stopping at the first parent that already sorts
	//: before this element — everything above it does too.
	for child > 0 {
		parent := (child - 1) / branching
		//: >= 0 keeps equal elements where they are, which costs one swap less
		//: per tie and is why the heap is explicitly not stable.
		if h.cmp(h.items[child], h.items[parent]) >= 0 {
			//: the invariant holds from here up.
			return
		}
		h.items[child], h.items[parent] = h.items[parent], h.items[child]
		child = parent
	}
}

// down restores the heap invariant along the path from parent to a leaf.
func (h *Heap[T]) down(parent int) {
	count := len(h.items)
	//: walk towards the leaves, stopping at the first level where the invariant
	//: already holds.
	for {
		left := branching*parent + 1
		//: no children — parent is a leaf and the invariant holds.
		if left >= count {
			//: done sifting.
			return
		}
		first := left
		//: pick the child that sorts first; only that one can displace parent.
		if right := left + 1; right < count && h.cmp(h.items[right], h.items[left]) < 0 {
			first = right
		}
		//: parent already sorts before both children.
		if h.cmp(h.items[first], h.items[parent]) >= 0 {
			//: the invariant holds from here down.
			return
		}
		h.items[parent], h.items[first] = h.items[first], h.items[parent]
		parent = first
	}
}
