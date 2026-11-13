package cache

// heapArity is the expiry heap's fan-out: the four cells of a parent's children share
// one cache line, and the tree is half as deep as a binary one.
const heapArity int = 4

// deadlineSlot is one cell of the expiry heap: the deadline, held inline so a
// comparison reads no entry, and the entry it belongs to.
type deadlineSlot[K comparable, V any] struct {
	at  int64
	ent *entry[K, V]
}

// deadlineHeap orders the entries that carry a deadline, earliest first: a
// 4-ary min-heap over int64 deadlines (#1038). Each entry knows its cell — its
// slot is the index plus one — so a re-stamp or a removal from anywhere costs
// O(log n) without a search, and no comparison goes through a function value.
// It replaced kernel/collections/heap's IndexedHeap here, whose comparison is
// an indirect call that dereferences two entries; that heap stays for its
// other users. A slot is 32 bits: four billion deadlines is past what the
// memory of the entries holding them allows. Caller holds the cache's lock.
type deadlineHeap[K comparable, V any] struct {
	// slots is the heap array; nil until the first deadline.
	slots []deadlineSlot[K, V]
}

// size reports how many entries carry a deadline.
func (h *deadlineHeap[K, V]) size() int {
	//: the array IS the heap.
	return len(h.slots)
}

// top returns the entry with the earliest deadline and that deadline, or nil
// when no entry carries one.
func (h *deadlineHeap[K, V]) top() (*entry[K, V], int64) {
	//: an empty heap has no top.
	if len(h.slots) == 0 {
		//: none.
		return nil, 0
	}
	//: the root is the earliest.
	return h.slots[0].ent, h.slots[0].at
}

// push adds ent, which carries no deadline yet, at deadline at.
func (h *deadlineHeap[K, V]) push(ent *entry[K, V], at int64) {
	h.slots = append(h.slots, deadlineSlot[K, V]{at: at, ent: ent})
	h.up(len(h.slots) - 1)
}

// fix moves ent, already in the heap, to its new deadline at.
func (h *deadlineHeap[K, V]) fix(ent *entry[K, V], at int64) {
	cell := int(ent.slot) - 1
	h.slots[cell].at = at
	//: a deadline brought forward climbs; one pushed back sinks.
	if !h.up(cell) {
		h.down(cell)
	}
}

// remove takes ent, which is in the heap, out of it.
func (h *deadlineHeap[K, V]) remove(ent *entry[K, V]) {
	cell := int(ent.slot) - 1
	last := len(h.slots) - 1
	ent.slot = 0
	//: the last cell fills the hole, unless it was the hole.
	if cell != last {
		h.slots[cell] = h.slots[last]
	}
	//: the vacated cell drops its pointer, so a removed entry is collectable.
	h.slots[last] = deadlineSlot[K, V]{}
	h.slots = h.slots[:last]
	//: the moved cell settles, whichever way its deadline points.
	if cell != last && !h.up(cell) {
		h.down(cell)
	}
}

// up moves the cell at index toward the root while its deadline is earlier
// than its parent's, keeping every entry's slot in step, and reports whether
// it moved.
func (h *deadlineHeap[K, V]) up(index int) bool {
	moving := h.slots[index]
	start := index
	//: the hole climbs while the parent is later.
	for index > 0 {
		parent := (index - 1) / heapArity
		//: the parent is no later: the hole is the cell's place.
		if h.slots[parent].at <= moving.at {
			break
		}
		h.place(index, h.slots[parent])
		index = parent
	}
	h.place(index, moving)
	//: whether it climbed at all.
	return index != start
}

// down moves the cell at index toward the leaves while a child's deadline is
// earlier, keeping every entry's slot in step.
func (h *deadlineHeap[K, V]) down(index int) {
	moving := h.slots[index]
	count := len(h.slots)
	//: the hole sinks while its earliest child is earlier than the cell.
	for {
		first := index*heapArity + 1
		//: a leaf: the hole is the cell's place.
		if first >= count {
			break
		}
		least := first
		//: the earliest of up to four children.
		for child := first + 1; child < min(first+heapArity, count); child++ {
			//: an earlier child.
			if h.slots[child].at < h.slots[least].at {
				least = child
			}
		}
		//: no child is earlier: the hole is the cell's place.
		if h.slots[least].at >= moving.at {
			break
		}
		h.place(index, h.slots[least])
		index = least
	}
	h.place(index, moving)
}

// place writes cell at index and tells its entry where it is.
func (h *deadlineHeap[K, V]) place(index int, cell deadlineSlot[K, V]) {
	h.slots[index] = cell
	cell.ent.slot = uint32(index) + 1
}
