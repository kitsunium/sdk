package cache

// maxFreq caps an entry's hit counter: S3-FIFO's two bits. Three hits are
// enough to survive three passes of main's clock hand.
const maxFreq uint8 = 3

// readMark is the bit of an entry's freq FetchMany marks a hit it recorded
// with, under every policy, for the length of one batch: far above maxFreq,
// and cleared before the batch returns (#840).
const readMark uint8 = 1 << 7

// smallShare is the small queue's share of the capacity, written as the
// divisor it is used as: a fiftieth. The paper's tenth admitted into main
// keys that came back once by chance; measured over 100 million accesses
// (#834, BENCH.md), a fiftieth keeps 0.9 points more of a Zipf s = 1.0 at a
// tenth of the keys, and more of every Zipf, scan and loop cell.
const smallShare int = 50

// ghostShare is the ghost's share of the capacity, written as the divisor it
// is used as: a quarter. A ghost as long as main (the paper's) remembers keys
// that recur too rarely to earn main, which they then share with the hot set;
// a quarter admits a key straight into main only when it came back soon.
const ghostShare int = 4

// admitS3FIFO places a new entry in the small queue, or in main when the
// ghost remembers its key. Caller holds the lock.
func (c *UnlockedMap[K, V]) admitS3FIFO(ent *entry[K, V]) {
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

// evictS3FIFO evicts from the small queue while it holds its share or main is
// empty, from main otherwise, and falls back to the other queue when the
// first has nothing it may evict. A walk of probation that evicts nothing may
// still promote its hit entries into main, which then holds victims it did
// not have: main is asked again rather than the bound given up (#1380).
// Caller holds the lock.
func (c *UnlockedMap[K, V]) evictS3FIFO(fresh *entry[K, V], pending *[]evictionValue[K, V]) bool {
	//: probation first while it is over its share, or when main is empty.
	if c.small.count > 0 && (c.main.count == 0 || c.smallIsLarge()) {
		//: small, then main, which holds what small promoted.
		return c.evictSmall(fresh, pending) || c.evictMain(fresh, pending)
	}
	//: main, then small, then main again with what small promoted.
	return c.evictMain(fresh, pending) || c.evictSmall(fresh, pending) || c.evictMain(fresh, pending)
}

// smallIsLarge reports whether the small queue holds its share of the bound
// that applies. Caller holds the lock.
func (c *UnlockedMap[K, V]) smallIsLarge() bool {
	//: a count bound: a fiftieth of the entries.
	if c.maxEntries > 0 && c.small.count*smallShare >= c.maxEntries {
		//: over its share.
		return true
	}
	//: a weight bound: a fiftieth of the weight.
	return c.maxWeight > 0 && c.small.weight*int64(smallShare) >= c.maxWeight
}

// evictSmall walks the small queue from its tail: an entry hit since its
// admission moves to main with its counter cleared, the first one never hit
// is evicted and its key remembered by the ghost. The entry the current Set
// wrote is stepped over, never stopped at: a new one is the head, so the walk
// ends with it, but an overwrite leaves the entry where it sits (S3-FIFO moves
// nothing on a write), and the newer entries ahead of it are victims too
// (#1380). Caller holds the lock.
func (c *UnlockedMap[K, V]) evictSmall(fresh *entry[K, V], pending *[]evictionValue[K, V]) bool {
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
// (#1380). Caller holds the lock.
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

// ghostBound is how many hashes the ghost keeps: a quarter of the entry
// bound when there is one, else of the live count, and at least one. Caller
// holds the lock.
func (c *UnlockedMap[K, V]) ghostBound() int {
	//: a count bound sizes the ghost.
	if c.maxEntries > 0 {
		//: a quarter of the entry bound.
		return max(c.maxEntries/ghostShare, 1)
	}
	//: a weight bound alone: a quarter of the live entries.
	return max(len(c.items)/ghostShare, 1)
}
