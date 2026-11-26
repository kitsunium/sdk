package cache

// Policy chooses which entry a full Cache evicts: PolicyLRU, PolicyS3FIFO or
// PolicyTinyLFU. The zero value is PolicyLRU, today's behaviour; an unknown
// value runs as PolicyLRU, and Stats says which policy runs.
type Policy uint8

const (
	// PolicyLRU evicts the least-recently-used entry: every hit moves its entry
	// to the front. A scan of cold keys longer than the capacity flushes the
	// whole hot set.
	PolicyLRU Policy = iota
	// PolicyS3FIFO is S3-FIFO (Yang et al., SOSP 2023): a new key enters a small
	// probationary FIFO holding a fiftieth of the capacity; one hit there
	// promotes it to the main FIFO when it reaches the tail, none sends its key
	// to a ghost set holding a quarter of the capacity in hashes, and a key the
	// ghost remembers is admitted straight into main. A one-time scan therefore
	// churns the small queue and leaves the hot set in main. A hit only sets a
	// counter — it moves nothing.
	PolicyS3FIFO
	// PolicyTinyLFU is W-TinyLFU (Einziger, Friedman and Manes, ACM TOS 2017):
	// a new key enters an LRU window holding a hundredth of the capacity; the
	// key the window pushes out moves into main when main has room, and
	// otherwise contests main's next victim — the least recently used of main's
	// probationary segment — and stays only when a frequency sketch estimates it
	// was used more, and twice at least. A second use in main moves an entry into
	// the protected segment, four fifths of main; an entry heavier than that
	// segment alone stays in probation. Every Set and every hit counts its key:
	// the first use in a period in a doorkeeper, a Bloom filter, the later ones in
	// a count-min sketch of four-bit counters halved every ten uses an entry of
	// the bound, so a key used once — a scan, the long tail — is estimated at one
	// use and does not win a contest against a key estimated higher, and the
	// counts follow what is used now. The sketch shares its counters between
	// keys: the contest compares estimates, which can run above a key's real use,
	// and nothing is promised about actual uses. A key just written is always
	// served: the window holds it, and no contest is held for it, so with room for
	// one key it replaces any other.
	PolicyTinyLFU
)

// admit places a new entry by the policy. Caller holds the lock.
func (c *UnlockedMap[K, V]) admit(ent *entry[K, V]) {
	//: each policy has its own door.
	switch c.policy {
	//: S3-FIFO: probation, or main for a key the ghost remembers.
	case PolicyS3FIFO:
		c.admitS3FIFO(ent)
	//: W-TinyLFU: the window, the key counted by the sketch.
	case PolicyTinyLFU:
		c.admitTinyLFU(ent)
	//: LRU has one list, and a new entry is its most recent.
	default:
		ent.queue = queueMain
		c.main.pushFront(ent)
	}
}

// touch records a hit by the policy: LRU promotes the entry, S3-FIFO counts
// it, W-TinyLFU counts it and moves it up its segment. Caller holds the lock.
func (c *UnlockedMap[K, V]) touch(ent *entry[K, V]) {
	//: each policy records a hit its own way.
	switch c.policy {
	//: S3-FIFO moves nothing on a hit; the counter is read at eviction.
	case PolicyS3FIFO:
		//: saturating at the counter's cap.
		if ent.freq < maxFreq {
			ent.freq++
		}
	//: W-TinyLFU: the sketch counts it, the segments order it.
	case PolicyTinyLFU:
		c.touchTinyLFU(ent)
	//: under LRU every entry sits in main.
	default:
		c.main.moveFront(ent)
	}
}

// evictOne removes one entry by the policy, never fresh — the entry the
// current Set wrote — and reports whether it found a victim. Caller holds
// the lock.
func (c *UnlockedMap[K, V]) evictOne(fresh *entry[K, V], pending *[]evictionValue[K, V]) bool {
	//: each policy picks its own victim.
	switch c.policy {
	//: S3-FIFO: probation's walk, then main's clock.
	case PolicyS3FIFO:
		//: the two queues, in the order the policy prefers.
		return c.evictS3FIFO(fresh, pending)
	//: W-TinyLFU: the window's candidate against main's victim.
	case PolicyTinyLFU:
		//: the admission contest, or main's victim.
		return c.evictTinyLFU(fresh, pending)
	//: LRU takes main's tail.
	default:
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
}
