package cache

// maxFreq caps an entry's hit counter: S3-FIFO's two bits. Three hits are
// enough to survive three passes of main's clock hand.
const maxFreq uint8 = 3

// smallShare is the small queue's share of the capacity: one tenth, the
// paper's figure, written as the divisor it is used as.
const smallShare int = 10

// admit places a new entry: at the front of the LRU list, or for S3-FIFO in
// the small queue — main when the ghost remembers its key. Caller holds mu.
func (c *Cache[K, V]) admit(ent *entry[K, V]) {
	//: LRU has one list, and a new entry is its most recent.
	if c.policy != PolicyS3FIFO {
		ent.queue = queueMain
		c.main.pushFront(ent)
		//: admitted.
		return
	}
	//: a key evicted from probation without a hit comes back: it skips
	//: probation, which is what makes a recurring key survive a scan.
	if c.ghost.remembers(ent.key) {
		ent.queue = queueMain
		c.main.pushFront(ent)
		//: admitted into main.
		return
	}
	ent.queue = queueSmall
	c.small.pushFront(ent)
}

// touch records a hit: LRU promotes the entry, S3-FIFO counts it. Caller
// holds mu.
func (c *Cache[K, V]) touch(ent *entry[K, V]) {
	//: S3-FIFO moves nothing on a hit; the counter is read at eviction.
	if c.policy == PolicyS3FIFO {
		//: saturating, two bits' worth.
		if ent.freq < maxFreq {
			ent.freq++
		}
		//: counted.
		return
	}
	//: under LRU every entry sits in main.
	c.main.moveFront(ent)
}

// evictOne removes one entry by the policy, never fresh — the entry the
// current Set wrote — and reports whether it found a victim. Caller holds mu.
func (c *Cache[K, V]) evictOne(fresh *entry[K, V], pending *[]evictionValue[K, V]) bool {
	//: S3-FIFO picks its queue; LRU takes main's tail.
	if c.policy == PolicyS3FIFO {
		//: the two queues, in the order the policy prefers.
		return c.evictS3FIFO(fresh, pending)
	}
	victim := c.main.tail
	//: nothing but the entry just written is left to evict.
	if victim == nil || victim == fresh {
		//: no victim.
		return false
	}
	c.removeEvict(victim, pending)
	//: evicted.
	return true
}

// evictS3FIFO evicts from the small queue while it holds its share or main is
// empty, from main otherwise, and falls back to the other queue when the
// first has nothing it may evict. A walk of probation that evicts nothing may
// still promote its hit entries into main, which then holds victims it did
// not have: main is asked again rather than the bound given up (#1380).
// Caller holds mu.
func (c *Cache[K, V]) evictS3FIFO(fresh *entry[K, V], pending *[]evictionValue[K, V]) bool {
	//: probation first while it is over its tenth, or when main is empty.
	if c.small.count > 0 && (c.main.count == 0 || c.smallIsLarge()) {
		//: small, then main, which holds what small promoted.
		return c.evictSmall(fresh, pending) || c.evictMain(fresh, pending)
	}
	//: main, then small, then main again with what small promoted.
	return c.evictMain(fresh, pending) || c.evictSmall(fresh, pending) || c.evictMain(fresh, pending)
}

// smallIsLarge reports whether the small queue holds its tenth of the bound
// that applies. Caller holds mu.
func (c *Cache[K, V]) smallIsLarge() bool {
	//: a count bound: a tenth of the entries.
	if c.maxEntries > 0 && c.small.count*smallShare >= c.maxEntries {
		//: over its share.
		return true
	}
	//: a weight bound: a tenth of the weight.
	return c.maxWeight > 0 && c.small.weight*int64(smallShare) >= c.maxWeight
}

// evictSmall walks the small queue from its tail: an entry hit since its
// admission moves to main with its counter cleared, the first one never hit
// is evicted and its key remembered by the ghost. The entry the current Set
// wrote is stepped over, never stopped at: a new one is the head, so the walk
// ends with it, but an overwrite leaves the entry where it sits (S3-FIFO moves
// nothing on a write), and the newer entries ahead of it are victims too
// (#1380). Caller holds mu.
func (c *Cache[K, V]) evictSmall(fresh *entry[K, V], pending *[]evictionValue[K, V]) bool {
	//: from the tail to the head; prev is read before a promotion unlinks.
	for victim := c.small.tail; victim != nil; {
		newer := victim.prev
		//: the entry just written stays: the walk goes on past it.
		if victim == fresh {
			victim = newer
			//: examine the next newer entry.
			continue
		}
		//: hit during probation: promoted, its counter starting over.
		if victim.freq > 0 {
			c.small.unlink(victim)
			victim.freq = 0
			victim.queue = queueMain
			c.main.pushFront(victim)
			victim = newer
			//: examine the next newer entry.
			continue
		}
		c.ghost.add(victim.key, c.ghostBound())
		c.removeEvict(victim, pending)
		//: evicted from probation.
		return true
	}
	//: nothing in probation may go.
	return false
}

// evictMain is main's clock hand: the tail with hits left is re-inserted at
// the head with one hit fewer, the first without any is evicted. Only the
// entry just written, alone in main, leaves it without a victim: an entry
// alone there with hits left spends them and goes, as it would among others
// (#1380). Caller holds mu.
func (c *Cache[K, V]) evictMain(fresh *entry[K, V], pending *[]evictionValue[K, V]) bool {
	//: every pass takes one hit away, so the walk ends within the queue's
	//: length times the counter's cap; the bound makes that explicit.
	for budget := c.main.count*int(maxFreq+1) + 1; budget > 0; budget-- {
		victim := c.main.tail
		//: an empty main has no victim.
		if victim == nil {
			//: no victim.
			return false
		}
		//: a hit left, or the entry just written: a second chance at the head.
		if victim.freq > 0 || victim == fresh {
			//: the entry just written, alone in main, cannot make way for itself.
			if victim == fresh && c.main.count == 1 {
				//: no victim.
				return false
			}
			//: one hit spent.
			if victim.freq > 0 {
				victim.freq--
			}
			c.main.moveFront(victim)
			//: the next tail.
			continue
		}
		c.removeEvict(victim, pending)
		//: evicted from main.
		return true
	}
	//: unreachable while the counters are capped; stated rather than looped.
	return false
}

// ghostBound is how many hashes the ghost keeps: the entry bound when there is
// one, else the live count. Caller holds mu.
func (c *Cache[K, V]) ghostBound() int {
	//: a count bound sizes the ghost like main.
	if c.maxEntries > 0 {
		//: the entry bound.
		return c.maxEntries
	}
	//: a weight bound alone: as many hashes as live entries.
	return len(c.items)
}
