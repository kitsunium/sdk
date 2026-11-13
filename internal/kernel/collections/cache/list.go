package cache

// entryList is an intrusive doubly-linked list of entries with its count and
// summed weight: head = most recently inserted or promoted, tail = the next
// victim. Caller holds the cache's lock for every method.
type entryList[K comparable, V any] struct {
	head   *entry[K, V]
	tail   *entry[K, V]
	count  int
	weight int64
}

// pushFront inserts ent at the head. ent MUST be detached.
func (l *entryList[K, V]) pushFront(ent *entry[K, V]) {
	//: the new node's next is the old head.
	ent.prev = nil
	ent.next = l.head
	//: the old head (if any) links back to the new node.
	if l.head != nil {
		//: maintain the back-link.
		l.head.prev = ent
	}
	//: the new node becomes the head.
	l.head = ent
	//: an empty list also gains its tail.
	if l.tail == nil {
		//: first node is both head and tail.
		l.tail = ent
	}
	l.count++
	l.weight += int64(ent.weight)
}

// unlink detaches ent from the list, fixing head and tail.
func (l *entryList[K, V]) unlink(ent *entry[K, V]) {
	//: bridge the previous node (or the head) across ent.
	if ent.prev != nil {
		//: previous node skips ent.
		ent.prev.next = ent.next
	} else {
		//: ent was the head — the next node becomes head.
		l.head = ent.next
	}
	//: bridge the next node (or the tail) across ent.
	if ent.next != nil {
		//: next node skips ent.
		ent.next.prev = ent.prev
	} else {
		//: ent was the tail — the previous node becomes tail.
		l.tail = ent.prev
	}
	//: clear ent's links so a stale node can't corrupt the list.
	ent.prev = nil
	ent.next = nil
	l.count--
	l.weight -= int64(ent.weight)
}

// moveFront promotes ent, which sits in the list, to the head. It relinks
// pointers only: the entry never leaves the list, so its count and summed
// weight stay as they are, and a hit writes neither (#1038).
func (l *entryList[K, V]) moveFront(ent *entry[K, V]) {
	//: already the head — nothing to move.
	if l.head == ent {
		//: fast-path no-op.
		return
	}
	//: not the head, so ent has a previous node and the list a head.
	prev, next := ent.prev, ent.next
	prev.next = next
	//: bridge the next node, or make the previous one the tail.
	if next != nil {
		//: next node skips ent.
		next.prev = prev
	} else {
		//: ent was the tail — the previous node becomes tail.
		l.tail = prev
	}
	ent.prev = nil
	ent.next = l.head
	l.head.prev = ent
	l.head = ent
}
