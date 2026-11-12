package http1

import (
	"hash/maphash"
	"sync"
	"time"
)

// The bucket table's shape.
const (
	// bucketShardsLarge is how many shards a large table is split into.
	bucketShardsLarge int = 64
	// bucketShardsThreshold is the key count at which a table takes its shards:
	// below it the LRU is one list, exact.
	bucketShardsThreshold int = 8192
	// nanosPerSecond is how many nanoseconds a second holds.
	nanosPerSecond float64 = float64(time.Second)
	// fullTable is the fraction of slots an open-addressed index may use.
	indexGrowth int = 2
	// noEntry marks an empty index slot and the ends of a shard's LRU list.
	noEntry int32 = -1
)

// KeyedBucketsValue configures NewKeyedBuckets: the refill rate, the burst and
// how many keys the table holds.
type KeyedBucketsValue struct {
	// Rate is the refill, in tokens per second.
	Rate float64
	// Burst is the most tokens a key holds.
	Burst int
	// Keys is the table's capacity, the most keys it remembers at once.
	Keys int
}

// KeyedBuckets is the token-bucket table NewKeyedBuckets builds: a
// corenet.HTTP1Limiter, safe for concurrent use, allocating nothing per call.
type KeyedBuckets struct {
	// seed makes the keys' hashes unpredictable to a client that chooses them.
	seed maphash.Seed
	// shards are the table's parts; a key's hash picks one.
	shards []bucketShard
	// rate is the refill per nanosecond.
	rate float64
	// burst is a full bucket.
	burst float64
}

// bucketShard is one part of the table: a fixed array of entries, an
// open-addressed index into it, and an LRU list through it. Nothing past
// construction allocates.
type bucketShard struct {
	// mu serialises the lanes that land on this shard.
	mu sync.Mutex
	// entries holds the keys; used of them are in use.
	entries []bucketEntry
	used    int32
	// index maps a key's hash to an entry, by position in entries; noEntry is empty.
	index []int32
	// mask keeps a hash inside index.
	mask uint64
	// head is the most recently used entry, tail the least.
	head, tail int32
}

// bucketEntry is one key's bucket, linked by position in the LRU list.
type bucketEntry struct {
	// key is the hash of the bucket's key: the key's bytes are never kept.
	key uint64
	// tokens are the tokens the bucket held at last.
	tokens float64
	// last is when the bucket was last refilled.
	last int64
	// prev and next link the LRU list.
	prev, next int32
}

// NewKeyedBuckets returns a token-bucket table: at most cfg.Keys keys, keyed by a
// hash, allocated once here and never again, the least recently used key
// evicted first when it is full. A negative or zero Rate, Burst or Keys is
// refused with corenet.HTTP1Misconfigured.
func NewKeyedBuckets(cfg KeyedBucketsValue) (*KeyedBuckets, error) {
	//: every bound, the first one out named.
	switch {
	case cfg.Rate <= 0:
		return nil, misconfigured("NewKeyedBuckets", "rate")
	case cfg.Burst <= 0:
		return nil, misconfigured("NewKeyedBuckets", "burst")
	case cfg.Keys <= 0:
		return nil, misconfigured("NewKeyedBuckets", "keys")
	}
	n := 1
	//: a large table divides its lock; a small one keeps one exact LRU.
	if cfg.Keys >= bucketShardsThreshold {
		n = bucketShardsLarge
	}
	b := &KeyedBuckets{seed: maphash.MakeSeed(), shards: make([]bucketShard, n), rate: cfg.Rate / nanosPerSecond, burst: float64(cfg.Burst)}
	perShard := max(1, cfg.Keys/n)
	//: each shard, sized alike.
	for i := range b.shards {
		b.shards[i].init(perShard)
	}
	//: sized once and for all.
	return b, nil
}

// init sizes the shard for capacity entries, its index at twice that, a power of
// two.
func (s *bucketShard) init(capacity int) {
	size := 1
	//: at least twice the entries: a linear probe stays short.
	for size < capacity*indexGrowth {
		size *= indexGrowth
	}
	s.entries = make([]bucketEntry, 0, capacity)[:capacity]
	s.index = make([]int32, 0, size)[:size]
	s.mask = uint64(size - 1)
	s.head, s.tail = noEntry, noEntry
	//: empty.
	for i := range s.index {
		s.index[i] = noEntry
	}
}

// Allow implements corenet.HTTP1Limiter: it takes one token from key's bucket,
// a key seen for the first time starting full.
func (b *KeyedBuckets) Allow(key []byte, now int64) (ok bool, retryAfter int) {
	h := maphash.Bytes(b.seed, key)
	s := &b.shards[h%uint64(len(b.shards))]
	s.mu.Lock()
	e := s.lookup(h, b.burst, now)
	elapsed := now - e.last
	//: an instant that moves backwards refills nothing.
	if elapsed > 0 {
		e.tokens = min(b.burst, e.tokens+float64(elapsed)*b.rate)
		e.last = now
	}
	//: a whole token.
	if e.tokens >= 1 {
		e.tokens--
		s.mu.Unlock()
		//: served.
		return true, 0
	}
	wait := (1 - e.tokens) / (b.rate * nanosPerSecond)
	s.mu.Unlock()
	//: at least one second: RFC 9110 §10.2.3 counts in whole seconds.
	return false, max(1, ceilToInt(wait))
}

// ceilToInt rounds up a positive count of seconds.
func ceilToInt(v float64) int {
	n := int(v)
	//: a fraction left over.
	if float64(n) < v {
		n++
	}
	//: rounded.
	return n
}

// lookup returns key's entry, moved to the head of the LRU list, new and full
// when key is unseen, the least recent entry evicted when the shard is full.
func (s *bucketShard) lookup(key uint64, burst float64, now int64) *bucketEntry {
	pos := key & s.mask
	//: the first empty slot ends the probe: the key is new.
	for ; s.index[pos] != noEntry; pos = (pos + 1) & s.mask {
		e := &s.entries[s.index[pos]]
		//: found.
		if e.key == key {
			s.touch(s.index[pos])
			//: the existing bucket.
			return e
		}
	}
	//: the shard has no room left: forget the least recently used, whose slot
	//: the probe may have passed over.
	if int(s.used) == len(s.entries) {
		s.evictTail()
		pos = key & s.mask
		//: find the slot again, the eviction having moved entries.
		for s.index[pos] != noEntry {
			pos = (pos + 1) & s.mask
		}
	}
	id := s.used
	s.used++
	s.index[pos] = id
	s.entries[id] = bucketEntry{key: key, tokens: burst, last: now, prev: noEntry, next: s.head}
	s.link(id)
	//: the new bucket, full.
	return &s.entries[id]
}

// touch moves entry id to the head of the LRU list.
func (s *bucketShard) touch(id int32) {
	//: already the most recent.
	if s.head == id {
		//: nothing to move.
		return
	}
	s.unlink(id)
	s.entries[id].prev, s.entries[id].next = noEntry, s.head
	s.link(id)
}

// link makes id, whose next is the old head, the list's head.
func (s *bucketShard) link(id int32) {
	//: the first entry is also the tail.
	if s.head == noEntry {
		s.tail = id
	} else {
		s.entries[s.head].prev = id
	}
	s.head = id
}

// unlink removes id from the LRU list.
func (s *bucketShard) unlink(id int32) {
	e := &s.entries[id]
	//: the neighbour before it, or the head.
	if e.prev != noEntry {
		s.entries[e.prev].next = e.next
	} else {
		s.head = e.next
	}
	//: the neighbour after it, or the tail.
	if e.next != noEntry {
		s.entries[e.next].prev = e.prev
	} else {
		s.tail = e.prev
	}
}

// evictTail forgets the least recently used entry, moving the last used entry
// into its place so entries stay dense, and repairs the index by backward shift.
func (s *bucketShard) evictTail() {
	id := s.tail
	s.unlink(id)
	s.removeIndex(s.entries[id].key)
	last := s.used - 1
	//: a hole would break "entries are dense": the last entry moves into it.
	if id != last {
		s.moveEntry(last, id)
	}
	s.used = last
}

// moveEntry moves entry from to position to, relinking its neighbours and its
// index slot.
func (s *bucketShard) moveEntry(from, to int32) {
	e := s.entries[from]
	s.entries[to] = e
	//: the slot that named the old position.
	for pos := e.key & s.mask; ; pos = (pos + 1) & s.mask {
		//: found, rewritten.
		if s.index[pos] == from {
			s.index[pos] = to
			break
		}
	}
	//: its neighbours follow it.
	if e.prev != noEntry {
		s.entries[e.prev].next = to
	} else {
		s.head = to
	}
	//: the one after it, or the tail.
	if e.next != noEntry {
		s.entries[e.next].prev = to
	} else {
		s.tail = to
	}
}

// removeIndex deletes key's slot from the open-addressed index by backward
// shifting, so no tombstone ever lengthens a probe.
func (s *bucketShard) removeIndex(key uint64) {
	pos := key & s.mask
	//: to the key's slot.
	for s.entries[s.index[pos]].key != key {
		pos = (pos + 1) & s.mask
	}
	//: slide back every later entry the removal would strand: one whose home is
	//: not after the hole moves into it, and the hole moves to where it was.
	hole := pos
	for next := (hole + 1) & s.mask; s.index[next] != noEntry; next = (next + 1) & s.mask {
		//: the only entries that may move.
		if s.movable(s.entries[s.index[next]].key&s.mask, hole, next) {
			s.index[hole] = s.index[next]
			hole = next
		}
	}
	s.index[hole] = noEntry
}

// movable reports whether an entry at next whose home slot is home may move into
// the hole at pos: whether home lies at or before the hole, walking the ring from
// next backwards.
func (s *bucketShard) movable(home, pos, next uint64) bool {
	//: the cyclic distance from home to next must reach pos.
	return (next-home)&s.mask >= (next-pos)&s.mask
}
