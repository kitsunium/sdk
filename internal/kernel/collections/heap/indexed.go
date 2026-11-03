package heap

// noSlot marks a handle id that names no element: its element was popped or
// removed, and the id waits on the free list for the next Push.
const noSlot int = -1

// Handle names one element of an IndexedHeap from the Push that returned it to
// the Pop or Remove that takes it out.
//
// The zero Handle names nothing. A Handle outlives its element harmlessly:
// once the element left, Update and Remove report false for it, even after
// its slot was handed to a later Push — the generation it carries no longer
// matches.
type Handle struct {
	// id indexes the position table; ids are recycled through a free list.
	id uint32
	// gen is the id's generation at the Push; it starts at 1, so the zero
	// Handle never matches a live element.
	gen uint32
}

// indexedItem is one array cell: the element, and the id of the handle that
// names it, so a sift can keep the position table in step.
type indexedItem[T any] struct {
	value T
	id    uint32
}

// indexedSlot is one entry of the position table: where the handle's element
// sits in the array, and the generation the handle must carry to name it.
type indexedSlot struct {
	at  int
	gen uint32
}

// IndexedHeap is a binary heap ordered by the comparison given to [NewIndexed],
// whose every element is named by the [Handle] its Push returned: Update
// re-orders one element after its key changed, and Remove takes one out from
// anywhere, both in O(log n).
//
// The zero value is NOT usable — construct with [NewIndexed]. Not safe for
// concurrent use.
type IndexedHeap[T any] struct {
	// items is the heap array, as in Heap, each cell carrying its handle id.
	items []indexedItem[T]
	// slots is the position table, indexed by handle id.
	slots []indexedSlot
	// free holds the ids of the elements that left, reused by Push.
	free []uint32
	// cmp is the caller's order. Non-nil by construction; see NewIndexed.
	cmp func(a, b T) int
}

// NewIndexed builds an empty IndexedHeap ordered by cmp. A nil cmp panics,
// for the reason New gives.
func NewIndexed[T any](cmp func(a, b T) int) *IndexedHeap[T] {
	//: refuse here, where the bug is, rather than on the first Push.
	if cmp == nil {
		//: no default exists: T is arbitrary and has no intrinsic order.
		panic("kernel/collections/heap: NewIndexed requires a comparison function — there is no order a heap can invent for an arbitrary T")
	}
	//: the arrays stay nil until the first Push; append allocates them.
	return &IndexedHeap[T]{cmp: cmp}
}

// Len reports how many elements the heap holds.
func (h *IndexedHeap[T]) Len() int {
	//: the array IS the heap, so its length is the count.
	return len(h.items)
}

// Push adds v, in O(log n), and returns the Handle that names it.
func (h *IndexedHeap[T]) Push(v T) Handle {
	var id uint32
	//: reuse a freed id first, so a heap in steady state allocates nothing.
	if last := len(h.free) - 1; last >= 0 {
		id = h.free[last]
		h.free = h.free[:last]
	} else {
		//: a fresh id, its generation starting at 0 and bumped below.
		id = uint32(len(h.slots))
		h.slots = append(h.slots, indexedSlot{at: noSlot})
	}
	//: a new generation: every Handle the id carried before stops matching.
	slot := &h.slots[id]
	slot.gen++
	//: generation 0 is the zero Handle's; skip it when the counter wraps.
	if slot.gen == 0 {
		slot.gen = 1
	}
	slot.at = len(h.items)
	h.items = append(h.items, indexedItem[T]{value: v, id: id})
	h.up(slot.at)
	//: the name of the element just pushed.
	return Handle{id: id, gen: slot.gen}
}

// Peek returns the top element and its Handle without removing it, and
// reports whether there was one.
func (h *IndexedHeap[T]) Peek() (T, Handle, bool) {
	//: an empty heap has no top; a zero value with false, never a panic.
	if len(h.items) == 0 {
		var zero T
		//: the caller distinguishes "empty" from "the zero value was on top".
		return zero, Handle{}, false
	}
	top := h.items[0]
	//: the root is the element that sorts first.
	return top.value, Handle{id: top.id, gen: h.slots[top.id].gen}, true
}

// Pop removes and returns the top element and the Handle that named it, in
// O(log n), and reports whether there was one. The Handle names nothing
// afterwards.
func (h *IndexedHeap[T]) Pop() (T, Handle, bool) {
	//: empty is a state, not a fault.
	if len(h.items) == 0 {
		var zero T
		//: nothing to hand back.
		return zero, Handle{}, false
	}
	top := h.items[0]
	named := Handle{id: top.id, gen: h.slots[top.id].gen}
	h.removeAt(0)
	//: the element that sorted first.
	return top.value, named, true
}

// Update replaces the element handle names with v and restores the order, in
// O(log n). It reports false, and changes nothing, when handle names no
// element of this heap.
func (h *IndexedHeap[T]) Update(handle Handle, v T) bool {
	at, ok := h.position(handle)
	//: a stale or foreign handle changes nothing.
	if !ok {
		//: nothing named.
		return false
	}
	h.items[at].value = v
	h.fix(at)
	//: re-ordered.
	return true
}

// Remove takes the element handle names out of the heap, from wherever it
// sits, in O(log n), and returns it. It reports (zero, false) when handle
// names no element of this heap.
func (h *IndexedHeap[T]) Remove(handle Handle) (T, bool) {
	at, ok := h.position(handle)
	//: a stale or foreign handle removes nothing.
	if !ok {
		var zero T
		//: nothing named.
		return zero, false
	}
	removed := h.items[at].value
	h.removeAt(at)
	//: the element the handle named.
	return removed, true
}

// position resolves handle to its array index, refusing a stale one.
func (h *IndexedHeap[T]) position(handle Handle) (int, bool) {
	//: an id this heap never issued names nothing.
	if int(handle.id) >= len(h.slots) {
		//: foreign or zero on an empty table.
		return 0, false
	}
	slot := h.slots[handle.id]
	//: a freed id, or one reissued since this handle was made.
	if slot.at == noSlot || slot.gen != handle.gen {
		//: stale.
		return 0, false
	}
	//: the live element's index.
	return slot.at, true
}

// removeAt takes the element at index at out: the last leaf takes its place
// and is sifted whichever way the order asks, the vacated cell is cleared and
// the element's id goes back to the free list.
func (h *IndexedHeap[T]) removeAt(at int) {
	last := len(h.items) - 1
	gone := h.items[at].id
	//: the last leaf fills the hole, unless the hole IS the last leaf.
	if at != last {
		h.items[at] = h.items[last]
		h.slots[h.items[at].id].at = at
	}
	//: clear the vacated cell: a heap of pointers must not keep a removed
	//: element reachable for as long as the backing array lives.
	h.items[last] = indexedItem[T]{}
	h.items = h.items[:last]
	h.slots[gone].at = noSlot
	h.free = append(h.free, gone)
	//: the moved leaf may sort before its new parent or after its children.
	if at < last {
		h.fix(at)
	}
}

// fix restores the order around index at, whose element changed: up when it
// now sorts before its parent, down otherwise.
func (h *IndexedHeap[T]) fix(at int) {
	//: a move up leaves nothing to sift down; only one direction can apply.
	if !h.up(at) {
		h.down(at)
	}
}

// place writes item into cell at and records the position. Every sift moves
// elements through it: one copy and one table write per level, where a swap
// would make two of each.
func (h *IndexedHeap[T]) place(at int, item indexedItem[T]) {
	h.items[at] = item
	h.slots[item.id].at = at
}

// up restores the invariant along the path from child to the root, moving
// the element up through a hole, and reports whether it moved.
func (h *IndexedHeap[T]) up(child int) bool {
	item := h.items[child]
	start := child
	//: walk towards the root, shifting each parent that sorts after the
	//: element down into the hole — everything above the first one that does
	//: not already sorts before it.
	for child > 0 {
		parent := (child - 1) / branching
		//: >= 0 keeps equal elements where they are, as Heap does.
		if h.cmp(item.value, h.items[parent].value) >= 0 {
			//: the invariant holds from here up.
			break
		}
		h.place(child, h.items[parent])
		child = parent
	}
	//: the element lands in the hole the walk left.
	if child != start {
		h.place(child, item)
	}
	//: whether the element left its index.
	return child != start
}

// down restores the invariant along the path from parent to a leaf, moving
// the element down through a hole.
func (h *IndexedHeap[T]) down(parent int) {
	count := len(h.items)
	item := h.items[parent]
	start := parent
	//: walk towards the leaves, shifting the child that sorts first up into
	//: the hole while it sorts before the element.
	for {
		left := branching*parent + 1
		//: no children — the hole is a leaf.
		if left >= count {
			break
		}
		first := left
		//: pick the child that sorts first; only that one can displace parent.
		if right := left + 1; right < count && h.cmp(h.items[right].value, h.items[left].value) < 0 {
			first = right
		}
		//: the element already sorts before both children.
		if h.cmp(h.items[first].value, item.value) >= 0 {
			break
		}
		h.place(parent, h.items[first])
		parent = first
	}
	//: the element lands in the hole the walk left.
	if parent != start {
		h.place(parent, item)
	}
}
