package cache

// maxFreq caps an entry's hit counter: S3-FIFO's two bits. Three hits are
// enough to survive three passes of main's clock hand.
const maxFreq uint8 = 3

// smallShare is the small queue's share of the capacity: one tenth, the
// paper's figure, written as the divisor it is used as.
const smallShare int = 10

// admit places a new entry: at the front of the LRU list, or for S3-FIFO in
// the small queue — main when the ghost remembers its key. Caller holds the lock.
func (c *UnlockedMap[K, V]) admit(ent *entry[K, V]) {
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
// holds the lock.
func (c *UnlockedMap[K, V]) touch(ent *entry[K, V]) {
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
// current Set wrote — and reports whether it found a victim. Caller holds the lock.
func (c *UnlockedMap[K, V]) evictOne(fresh *entry[K, V], pending *[]evictionValue[K, V]) bool {
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
// first has nothing it may evict. Caller holds the lock.
func (c *UnlockedMap[K, V]) evictS3FIFO(fresh *entry[K, V], pending *[]evictionValue[K, V]) bool {
	//: probation first while it is over its tenth, or when main is empty.
	if c.small.count > 0 && (c.main.count == 0 || c.smallIsLarge()) {
		//: small, then main.
		return c.evictSmall(fresh, pending) || c.evictMain(fresh, pending)
	}
	//: main, then small.
	return c.evictMain(fresh, pending) || c.evictSmall(fresh, pending)
}

// smallIsLarge reports whether the small queue holds its tenth of the bound
// that applies. Caller holds the lock.
func (c *UnlockedMap[K, V]) smallIsLarge() bool {
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
// is evicted and its key remembered by the ghost. Caller holds the lock.
func (c *UnlockedMap[K, V]) evictSmall(fresh *entry[K, V], pending *[]evictionValue[K, V]) bool {
	//: stop at the entry just written: it is the newest, so everything past
	//: it was examined.
	for victim := c.small.tail; victim != nil && victim != fresh; victim = c.small.tail {
		//: hit during probation: promoted, its counter starting over.
		if victim.freq > 0 {
			c.small.unlink(victim)
			victim.freq = 0
			victim.queue = queueMain
			c.main.pushFront(victim)
			//: examine the next tail.
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
// the head with one hit fewer, the first without any is evicted. Caller holds
// the lock.
func (c *UnlockedMap[K, V]) evictMain(fresh *entry[K, V], pending *[]evictionValue[K, V]) bool {
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
			//: alone in main, it cannot make way for anything.
			if c.main.count == 1 {
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
// one, else the live count. Caller holds the lock.
func (c *UnlockedMap[K, V]) ghostBound() int {
	//: a count bound sizes the ghost like main.
	if c.maxEntries > 0 {
		//: the entry bound.
		return c.maxEntries
	}
	//: a weight bound alone: as many hashes as live entries.
	return len(c.items)
}
