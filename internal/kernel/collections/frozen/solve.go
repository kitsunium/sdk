package frozen

import "slices"

// The shape of the hash function a build searches for.
const (
	// keysPerBucket is λ, the mean bucket size: one pilot byte serves this many
	// keys, so the pilots cost 1/λ byte a key. Three is where the search with
	// eviction stays linear at the table's load.
	keysPerBucket uint32 = 3
	// loadPercent is α: the table has n·100/α slots, the slots past n being
	// remapped onto the holes below it, so a pilot search never needs the last
	// free slot of a full table. Measured on a million keys, 99 spent half the
	// build evicting; 97 costs 0.12 byte a key of remap and builds in two
	// thirds of the time, and lower buys nothing more.
	loadPercent uint64 = 97
	// pilots is the number of pilot values a bucket can take: one byte.
	pilots int = 256
	// attempts bounds the seeds a build tries before it gives up; with a hash
	// of 64 bits a second seed is already rare.
	attempts int = 8
	// recentEvicted is how many buckets placed by eviction are not evicted
	// again at once, which breaks the two-bucket cycles eviction can fall in.
	recentEvicted int = 16
	// evictionsPerKey bounds the evictions of one seed, n times this.
	evictionsPerKey int = 32
	// percent is what loadPercent is out of.
	percent uint64 = 100
)

// plan is a solved hash function over n keys: the seed, the pilots, the remap of
// the slots past n, and where each input key landed.
type plan struct {
	seed   uint64
	n      uint32
	nb     uint32
	m      uint32
	pilots []byte
	remap  []uint32
	pos    []uint32
	hs     []uint64
}

// verdict is how one seed's search ended.
type verdict uint8

const (
	// solved: every key has a slot.
	solved verdict = iota
	// reseed: this seed failed (two keys of one hash, or too many evictions).
	reseed
	// duplicate: two keys are equal.
	duplicate
)

// seedAt is the seed of attempt a: a fixed sequence, so a build is a function
// of its key set alone and its image is reproducible.
func seedAt(a int) uint64 {
	return fold(uint64(a)+mulB, mulC)
}

// solve finds a minimal perfect hash function for n keys, hash(seed, i) being
// key i's hash under seed and same(i, j) whether keys i and j are equal. It
// reports the index of a duplicated key with ok false and dup >= 0, or ok false
// and dup < 0 when no seed worked.
func solve(n int, hash func(seed uint64, i int) uint64, same func(i, j int) bool) (p plan, dup int, ok bool) {
	if n == 0 {
		return plan{}, -1, true
	}
	hs := make([]uint64, n)
	for a := range attempts {
		seed := seedAt(a)
		for i := range hs {
			hs[i] = hash(seed, i)
		}
		s := newSearch(hs)
		switch v, d := s.run(same); v {
		case solved:
			return s.finish(seed), -1, true
		case duplicate:
			return plan{}, d, false
		case reseed:
			//: the next seed of the sequence.
		}
	}
	return plan{}, -1, false
}

// search is one seed's placement state.
type search struct {
	hs     []uint64
	n, m   uint32
	nb     uint32
	start  []uint32 // bucket b's keys are keys[start[b]:start[b+1]]
	keys   []uint32 // the keys' indexes, in bucket order
	bh     []uint64 // the keys' hashes, in bucket order, read sequentially
	owner  []uint32 // slot -> bucket+1, 0 when free
	taken  []uint64 // owner != 0 as a bitset, small enough to stay in cache
	pilot  []byte
	recent [recentEvicted]uint32
	next   int
	stack  []uint32
	buf    []uint32
}

// newSearch sizes the table and sorts the keys into their buckets.
func newSearch(hs []uint64) *search {
	n := uint32(len(hs))
	nb := (n + keysPerBucket - 1) / keysPerBucket
	m := uint32((uint64(n)*percent + loadPercent - 1) / loadPercent)
	s := &search{
		hs: hs, n: n, m: m, nb: nb,
		start: make([]uint32, nb+1), keys: make([]uint32, n), bh: make([]uint64, n),
		owner: make([]uint32, m), taken: make([]uint64, (m+63)/64), pilot: make([]byte, nb),
	}
	//: counting sort of the keys by bucket.
	for _, h := range hs {
		s.start[below(h, nb)+1]++
	}
	for b := range nb {
		s.start[b+1] += s.start[b]
	}
	fill := make([]uint32, nb)
	copy(fill, s.start[:nb])
	for i, h := range hs {
		b := below(h, nb)
		s.keys[fill[b]] = uint32(i)
		s.bh[fill[b]] = h
		fill[b]++
	}
	for i := range s.recent {
		s.recent[i] = ^uint32(0)
	}
	return s
}

// bucket returns the hashes of bucket b's keys.
func (s *search) bucket(b uint32) []uint64 { return s.bh[s.start[b]:s.start[b+1]] }

// run places every bucket, the largest first, evicting when a bucket finds no
// free pilot.
func (s *search) run(same func(i, j int) bool) (verdict, int) {
	if v, d := s.collisions(same); v != solved {
		return v, d
	}
	order := s.bySize()
	budget := int(s.n) * evictionsPerKey
	for _, b := range order {
		s.stack = append(s.stack[:0], b)
		for len(s.stack) > 0 {
			c := s.stack[len(s.stack)-1]
			s.stack = s.stack[:len(s.stack)-1]
			if s.placeFree(c) {
				continue
			}
			if budget == 0 || !s.placeEvicting(c) {
				return reseed, -1
			}
			budget--
		}
	}
	return solved, -1
}

// collisions finds two keys of one hash, which share every slot under every
// pilot: equal keys are a duplicate, different ones need another seed.
func (s *search) collisions(same func(i, j int) bool) (verdict, int) {
	for b := range s.nb {
		lo, hi := s.start[b], s.start[b+1]
		for i := lo + 1; i < hi; i++ {
			k := slices.Index(s.bh[lo:i], s.bh[i])
			if k < 0 {
				continue
			}
			if same(int(s.keys[i]), int(s.keys[lo+uint32(k)])) {
				return duplicate, int(s.keys[i])
			}
			return reseed, -1
		}
	}
	return solved, -1
}

// bySize lists the non-empty buckets, the largest first and in index order
// within one size, by a counting sort.
func (s *search) bySize() []uint32 {
	largest := uint32(0)
	for b := range s.nb {
		largest = max(largest, s.start[b+1]-s.start[b])
	}
	at := make([]uint32, largest+2)
	for b := range s.nb {
		at[largest-(s.start[b+1]-s.start[b])+1]++
	}
	for i := uint32(1); i < uint32(len(at)); i++ {
		at[i] += at[i-1]
	}
	nonEmpty := at[largest]
	order := make([]uint32, s.nb)
	for b := range s.nb {
		k := largest - (s.start[b+1] - s.start[b])
		order[at[k]] = b
		at[k]++
	}
	return order[:nonEmpty]
}

// slots writes the slots bucket b's keys take under pilot p into s.buf and
// reports false when two of them collide.
func (s *search) slots(b uint32, p byte) bool {
	s.buf = s.buf[:0]
	for _, h := range s.bucket(b) {
		x := slotOf(h, p, s.m)
		if slices.Contains(s.buf, x) {
			return false
		}
		s.buf = append(s.buf, x)
	}
	return true
}

// take gives bucket b pilot p and its slots, which s.buf holds.
func (s *search) take(b uint32, p byte) {
	s.pilot[b] = p
	for _, x := range s.buf {
		s.owner[x] = b + 1
		s.taken[x/64] |= 1 << (x % 64)
	}
}

// free releases slot x.
func (s *search) free(x uint32) {
	s.owner[x] = 0
	s.taken[x/64] &^= 1 << (x % 64)
}

// placeFree gives bucket b the first pilot whose slots are all free. Each
// pilot stops at its first taken slot, which at a high load is almost always
// its first key's.
func (s *search) placeFree(b uint32) bool {
	hs := s.bucket(b)
	for p := range pilots {
		free := true
		for _, h := range hs {
			x := slotOf(h, byte(p), s.m)
			if s.taken[x/64]&(1<<(x%64)) != 0 {
				free = false
				break
			}
		}
		if free && s.slots(b, byte(p)) {
			s.take(b, byte(p))
			return true
		}
	}
	return false
}

// placeEvicting gives bucket b the pilot whose taken slots belong to the
// cheapest set of buckets — the sum of their sizes squared — never one placed by
// a recent eviction, evicts those buckets onto the stack, and takes the slots.
func (s *search) placeEvicting(b uint32) bool {
	best, bestCost := -1, ^uint64(0)
	for p := range pilots {
		if !s.slots(b, byte(p)) {
			continue
		}
		cost, ok := s.cost()
		if ok && cost < bestCost {
			best, bestCost = p, cost
		}
	}
	if best < 0 {
		return false
	}
	s.slots(b, byte(best))
	for _, x := range s.buf {
		if o := s.owner[x]; o != 0 {
			s.evict(o - 1)
		}
	}
	s.slots(b, byte(best))
	s.take(b, byte(best))
	s.recent[s.next] = b
	s.next = (s.next + 1) % recentEvicted
	return true
}

// cost prices evicting the owners of the slots s.buf holds: false when one of
// them was placed by a recent eviction.
func (s *search) cost() (uint64, bool) {
	var cost uint64
	for i, x := range s.buf {
		o := s.owner[x]
		if o == 0 {
			continue
		}
		//: an owner holding two of these slots is counted once.
		counted := false
		for _, y := range s.buf[:i] {
			if s.owner[y] == o {
				counted = true
				break
			}
		}
		if counted {
			continue
		}
		for _, r := range s.recent {
			if r == o-1 {
				return 0, false
			}
		}
		size := uint64(s.start[o] - s.start[o-1])
		cost += size * size
	}
	return cost, true
}

// evict frees bucket c's slots and pushes it to be placed again.
func (s *search) evict(c uint32) {
	for _, h := range s.bucket(c) {
		x := slotOf(h, s.pilot[c], s.m)
		if s.owner[x] == c+1 {
			s.free(x)
		}
	}
	s.stack = append(s.stack, c)
}

// finish remaps the taken slots past n onto the free ones below it and records
// where every key landed.
func (s *search) finish(seed uint64) plan {
	p := plan{
		seed: seed, n: s.n, nb: s.nb, m: s.m, pilots: s.pilot, hs: s.hs,
		remap: make([]uint32, s.m-s.n), pos: make([]uint32, s.n),
	}
	hole := uint32(0)
	for x := s.n; x < s.m; x++ {
		if s.owner[x] == 0 {
			continue
		}
		for s.owner[hole] != 0 {
			hole++
		}
		p.remap[x-s.n] = hole
		hole++
	}
	for b := range s.nb {
		for j := s.start[b]; j < s.start[b+1]; j++ {
			x := slotOf(s.bh[j], s.pilot[b], s.m)
			if x >= s.n {
				x = p.remap[x-s.n]
			}
			p.pos[s.keys[j]] = x
		}
	}
	return p
}
