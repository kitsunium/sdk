package cache

import "hash/maphash"

// minGhostRing is the ring's first allocation: small enough for a tiny cache,
// large enough that a busy one does not double its way up from one slot.
const minGhostRing int = 8

// ghostSet is S3-FIFO's ghost queue: the hashes of the keys the small queue
// evicted without a hit, oldest first, bounded. A key it remembers skips
// probation on its next admission. It holds hashes, never keys or values, so
// a remembered key costs a few words whatever K is; a collision admits a
// stranger into main, which costs at worst one misplaced entry.
type ghostSet[K comparable] struct {
	// seed keys the hash; fixed for the cache's life.
	seed maphash.Seed
	// ring is the FIFO of hashes; head indexes the oldest of size live ones.
	ring []uint64
	head int
	size int
	// members counts each hash's occurrences in the ring.
	members map[uint64]uint32
}

// newGhostSet returns an empty ghost set with its own seed.
func newGhostSet[K comparable]() ghostSet[K] {
	//: the ring grows on demand; the map starts empty.
	return ghostSet[K]{seed: maphash.MakeSeed(), members: make(map[uint64]uint32)}
}

// remembers reports whether key's hash is in the ring.
func (g *ghostSet[K]) remembers(key K) bool {
	//: a cache that never evicted from probation has no ghost at all.
	if g.size == 0 {
		//: nothing remembered.
		return false
	}
	//: membership by hash.
	return g.members[maphash.Comparable(g.seed, key)] > 0
}

// add remembers key, forgetting the oldest hashes while the ring holds bound
// or more.
func (g *ghostSet[K]) add(key K, bound int) {
	//: a ghost of nothing is a ghost of one.
	bound = max(bound, 1)
	//: forget the oldest until there is room under the bound.
	for g.size >= bound {
		g.dropOldest()
	}
	//: grow the ring when it is full but the bound allows more.
	if g.size == len(g.ring) {
		g.grow(bound)
	}
	hash := maphash.Comparable(g.seed, key)
	g.ring[(g.head+g.size)%len(g.ring)] = hash
	g.size++
	g.members[hash]++
}

// dropOldest forgets the oldest hash.
func (g *ghostSet[K]) dropOldest() {
	hash := g.ring[g.head]
	g.head = (g.head + 1) % len(g.ring)
	g.size--
	//: the last occurrence leaves the map, so it does not grow with churn.
	if g.members[hash] <= 1 {
		delete(g.members, hash)
		//: forgotten.
		return
	}
	g.members[hash]--
}

// grow doubles the ring, never past bound, keeping the hashes oldest first.
func (g *ghostSet[K]) grow(bound int) {
	next := make([]uint64, min(max(2*len(g.ring), minGhostRing), max(bound, len(g.ring)+1)))
	//: unroll the ring into the new array, oldest at index 0.
	for i := range g.size {
		next[i] = g.ring[(g.head+i)%len(g.ring)]
	}
	g.ring = next
	g.head = 0
}

// reset forgets everything, keeping the ring's array.
func (g *ghostSet[K]) reset() {
	g.head = 0
	g.size = 0
	clear(g.members)
}
