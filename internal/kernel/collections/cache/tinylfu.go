package cache

import "hash/maphash"

const (
	// windowShare is W-TinyLFU's window against the bound, written as the
	// divisor it is used as: a hundredth.
	windowShare int = 100
	// protectedFifths is the protected segment's share of main, in fifths.
	protectedFifths int = 4
	// fifths divides a share in fifths.
	fifths int = 5
	// admitAtLeast is the estimate a candidate needs to replace a key of
	// main: two uses — the doorkeeper's and one counted —, so a key used once,
	// a scan's or the long tail's, never displaces one.
	admitAtLeast uint64 = 2
)

// sketchSeed keys the hash the frequency sketch counts by. One seed per
// process, drawn at start: no input can be crafted to collide with a key's
// counters, and two caches of one process count a key alike.
var sketchSeed = maphash.MakeSeed()

// initTinyLFU derives the segment bounds and sizes the sketch: by the entry
// bound, else by the live count as it grows; a cache that never evicts keeps
// none. Caller holds the lock.
func (c *UnlockedMap[K, V]) initTinyLFU() {
	//: a count bound: the window a hundredth of it, protected four fifths of
	//: the rest.
	if c.maxEntries > 0 {
		c.shares.window = max(c.maxEntries/windowShare, 1)
		c.shares.protected = int(protectedShare(int64(c.maxEntries - c.shares.window)))
		c.sketch.size(c.maxEntries)
	}
	//: a weight bound: the same shares of the weight.
	if c.maxWeight > 0 {
		c.shares.windowWeight = max(c.maxWeight/int64(windowShare), 1)
		c.shares.protectedWeight = protectedShare(c.maxWeight - c.shares.windowWeight)
		//: without a count bound the sketch grows with the live count.
		if c.maxEntries == 0 {
			c.sketch.size(blockWords)
		}
	}
}

// protectedShare returns the protected segment's share of a main of size
// units: four fifths, rounded down, computed without overflowing for a bound
// near the int64's maximum.
func protectedShare(size int64) int64 {
	//: the whole fifths, then the remainder's share of a fifth.
	return size/int64(fifths)*int64(protectedFifths) + size%int64(fifths)*int64(protectedFifths)/int64(fifths)
}

// admitTinyLFU hashes and counts a new entry, places it at the front of the
// window, and moves the window's overflow into probation while main has room
// for it. Caller holds the lock.
func (c *UnlockedMap[K, V]) admitTinyLFU(ent *entry[K, V]) {
	//: only a cache that may evict counts keys.
	if c.sketch.blocks != nil {
		//: a weight bound alone sizes the sketch by the live count.
		if c.maxEntries == 0 {
			c.sketch.fit(len(c.items))
		}
		ent.hash = uint32(maphash.Comparable(sketchSeed, ent.key))
		c.sketch.record(ent.hash, &ent.period)
	}
	ent.queue = queueSmall
	c.small.pushFront(ent)
	//: the window's oldest go to probation while main can take them.
	for c.windowIsLarge() {
		candidate := c.small.tail
		//: the entry just written stays, and a full main means a contest,
		//: which the eviction loop holds once the bounds are exceeded.
		if candidate == ent || !c.mainHasRoom(candidate) {
			//: the window keeps its overflow.
			return
		}
		c.probate(candidate)
	}
}

// touchTinyLFU records a hit: the sketch counts it, and the entry moves to
// the front of its segment — out of probation into protected. Caller holds
// the lock.
func (c *UnlockedMap[K, V]) touchTinyLFU(ent *entry[K, V]) {
	c.sketch.touch(ent.hash, &ent.period)
	//: protected, where the hits land, is an LRU: the hot path.
	if ent.queue == queueMain {
		c.main.moveFront(ent)
		//: relinked.
		return
	}
	c.reorderTinyLFU(ent)
}

// reorderTinyLFU moves a window entry to the window's front, and a
// probation entry to protected's, whose oldest go back to probation while
// it holds more than its share. Caller holds the lock.
func (c *UnlockedMap[K, V]) reorderTinyLFU(ent *entry[K, V]) {
	//: the window is an LRU.
	if ent.queue == queueSmall {
		c.small.moveFront(ent)
		//: relinked.
		return
	}
	//: a second use in main earns protection.
	c.probation.unlink(ent)
	ent.queue = queueMain
	c.main.pushFront(ent)
	//: protected over its share: its oldest go back to probation.
	for c.protectedIsLarge() {
		demoted := c.main.tail
		c.main.unlink(demoted)
		demoted.queue = queueProbation
		c.probation.pushFront(demoted)
	}
}

// evictTinyLFU evicts by W-TinyLFU's admission: while the window holds more
// than its share, its oldest entry is a candidate for main and contests
// main's victim; otherwise main's victim goes — probation's oldest, then
// protected's —, or the window's oldest when main has none. The entry the
// current Set wrote is stepped over, never stopped at, and never evicted.
// Caller holds the lock.
func (c *UnlockedMap[K, V]) evictTinyLFU(fresh *entry[K, V], pending *[]evictionValue[K, V]) bool {
	//: the window over its share: its oldest is a candidate for main.
	if c.windowIsLarge() {
		//: a window of the entry just written alone has no candidate.
		if candidate := olderThan(&c.small, fresh); candidate != nil {
			c.contest(candidate, fresh, pending)
			//: one entry moved or evicted.
			return true
		}
	}
	victim := c.mainVictim(fresh)
	//: main holds nothing but the entry just written: the window's oldest.
	if victim == nil {
		victim = olderThan(&c.small, fresh)
	}
	//: nothing but the entry just written is left to evict.
	if victim == nil {
		//: no victim.
		return false
	}
	c.removeEvict(victim, pending)
	//: evicted.
	return true
}

// contest settles a candidate from the window: it moves into probation when
// main has room for it; otherwise the sketch decides between it and main's
// victim, and the one used less goes — the candidate when they tie, so a key
// must have been used more than the one it replaces, and twice at least.
// Caller holds the lock.
func (c *UnlockedMap[K, V]) contest(candidate, fresh *entry[K, V], pending *[]evictionValue[K, V]) {
	//: room in main: no contest.
	if c.mainHasRoom(candidate) {
		c.probate(candidate)
		//: moved.
		return
	}
	victim := c.mainVictim(fresh)
	used := c.sketch.estimateHeld(candidate.hash, candidate.period)
	//: no victim but the entry just written, a candidate used once, or a
	//: victim used at least as often.
	if victim == nil || used < admitAtLeast || used <= c.sketch.estimateHeld(victim.hash, victim.period) {
		c.removeEvict(candidate, pending)
		//: the candidate goes.
		return
	}
	c.removeEvict(victim, pending)
	c.probate(candidate)
}

// probate moves a window entry to the front of probation. Caller holds the
// lock.
func (c *UnlockedMap[K, V]) probate(ent *entry[K, V]) {
	c.small.unlink(ent)
	ent.queue = queueProbation
	c.probation.pushFront(ent)
}

// mainVictim returns main's next victim, never fresh: probation's oldest,
// then protected's, or nil. Caller holds the lock.
func (c *UnlockedMap[K, V]) mainVictim(fresh *entry[K, V]) *entry[K, V] {
	//: probation first: protected holds what was used twice.
	if victim := olderThan(&c.probation, fresh); victim != nil {
		//: probation's oldest.
		return victim
	}
	//: protected's oldest.
	return olderThan(&c.main, fresh)
}

// windowIsLarge reports whether the window holds more than its share of a
// bound that applies. Caller holds the lock.
func (c *UnlockedMap[K, V]) windowIsLarge() bool {
	//: a count bound: a hundredth of the entries.
	if c.maxEntries > 0 && c.small.count > c.shares.window {
		//: over its share.
		return true
	}
	//: a weight bound: a hundredth of the weight.
	return c.maxWeight > 0 && c.small.weight > c.shares.windowWeight
}

// protectedIsLarge reports whether protected holds more than its share of a
// bound that applies. Caller holds the lock.
func (c *UnlockedMap[K, V]) protectedIsLarge() bool {
	//: a count bound.
	if c.maxEntries > 0 && c.main.count > c.shares.protected {
		//: over its share.
		return true
	}
	//: a weight bound.
	return c.maxWeight > 0 && c.main.weight > c.shares.protectedWeight
}

// mainHasRoom reports whether main — probation and protected — can take ent
// within its share of every bound: what is left of the bound past the
// window's share. Caller holds the lock.
func (c *UnlockedMap[K, V]) mainHasRoom(ent *entry[K, V]) bool {
	//: a count bound: one more entry.
	if c.maxEntries > 0 && c.probation.count+c.main.count >= c.maxEntries-c.shares.window {
		//: full.
		return false
	}
	//: a weight bound: ent's weight more.
	return c.maxWeight <= 0 || c.probation.weight+c.main.weight+int64(ent.weight) <= c.maxWeight-c.shares.windowWeight
}

// olderThan returns l's oldest entry, or the one before it when the oldest
// is skip, and nil for a list holding nothing else.
func olderThan[K comparable, V any](l *entryList[K, V], skip *entry[K, V]) *entry[K, V] {
	//: the tail, unless it is the entry to step over.
	if l.tail != skip {
		//: the oldest.
		return l.tail
	}
	//: the next oldest, if any.
	return skip.prev
}
