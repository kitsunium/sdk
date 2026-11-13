package cache

import (
	"iter"
	"sync"
	"time"
)

// cacheLine is the padding that separates two Caches allocated side by side.
const cacheLine int = 64

// Cache is a generic LRU+TTL cache safe for concurrent use. The zero value is
// NOT usable — construct with NewCache.
type Cache[K comparable, V any] struct {
	// mu serialises every call: a sync.Mutex, whose uncontended lock and
	// unlock are one atomic each and inlined — a RWMutex's write lock costs
	// four, and only Len and Stats ever read shared (#1038).
	mu sync.Mutex
	// core is the cache itself; every access holds mu.
	core UnlockedMap[K, V]
	// The padding keeps the counters every operation writes off the cache line of the
	// mutex of whatever is allocated next — in a Striped, the next stripe.
	_ [cacheLine]byte
}

// NewCache builds a Cache from cfg. A nil cfg.Clock defaults to clock.System.
func NewCache[K comparable, V any](cfg Config[K, V]) *Cache[K, V] {
	built := &Cache[K, V]{}
	built.core.init(cfg)
	//: a fresh cache starts with a pre-sized map and no list nodes.
	return built
}

// Fetch returns the value for key and whether it was present and unexpired. A
// hit promotes the entry to most-recently-used (hence Fetch, not a pure Get).
func (c *Cache[K, V]) Fetch(key K) (value V, ok bool) {
	//: registered BEFORE the unlock defer so LIFO runs it AFTER the unlock —
	//: OnEvict is user code and must never see the lock held.
	var pending []evictionValue[K, V]
	defer func() { c.core.notifyEvicted(pending) }()
	c.mu.Lock()
	defer c.mu.Unlock()
	//: the lookup, a lazy expiry and the policy's record of a hit.
	return c.core.fetch(key, &pending)
}

// Set stores key=val with the cache's default TTL.
func (c *Cache[K, V]) Set(key K, val V) {
	//: delegate to the TTL form with the configured default.
	c.setTTL(key, val, c.core.defaultTTL)
}

// SetTTL stores key=val with an explicit ttl (0 = no expiry), overriding the
// default for this entry only.
func (c *Cache[K, V]) SetTTL(key K, val V, ttl time.Duration) {
	//: delegate to the shared insert/update path.
	c.setTTL(key, val, ttl)
}

// setTTL inserts or updates key=val under the lock, computing the deadline
// and applying eviction when a bound is exceeded.
func (c *Cache[K, V]) setTTL(key K, val V, ttl time.Duration) {
	//: registered BEFORE the unlock defer so LIFO runs it AFTER the unlock.
	var pending []evictionValue[K, V]
	defer func() { c.core.notifyEvicted(pending) }()
	c.mu.Lock()
	defer c.mu.Unlock()
	c.core.set(key, val, ttl, &pending)
}

// Delete removes key if present. An explicit Delete does NOT fire OnEvict (that
// hook is for capacity/expiry removals only).
func (c *Cache[K, V]) Delete(key K) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.core.remove(key)
}

// Expire removes every entry whose deadline has passed and reports how many
// it removed. Each counts as an eviction and reaches OnEvict after the lock is
// released, as a lazy expiry does. The cache starts no goroutine: the caller
// decides when, and how often, to call it — on a ticker of its own, or on its
// scheduler. It costs O(k log n) for k expired entries; an entry without a
// deadline costs nothing, and a cache without any reads no clock.
func (c *Cache[K, V]) Expire() int {
	//: registered BEFORE the unlock defer so LIFO runs it AFTER the unlock.
	var pending []evictionValue[K, V]
	defer func() { c.core.notifyEvicted(pending) }()
	c.mu.Lock()
	defer c.mu.Unlock()
	//: the count reaped by this sweep.
	return c.core.expire(&pending)
}

// Keys yields the keys present and unexpired when the loop starts, in no
// particular order. They are copied under the lock and yielded after it is
// released, so the loop's body may call the cache — Set, Delete, Fetch —
// without deadlocking; what the body changes is not seen by the loop. A walk
// is not a read: no entry is promoted and no counter moves. Each loop
// allocates one slice of the keys.
func (c *Cache[K, V]) Keys() iter.Seq[K] {
	//: the snapshot is taken when the loop starts, not when Keys is called,
	//: so one sequence ranged twice reads the cache twice.
	return func(yield func(K) bool) {
		//: the body never runs under the lock: OnEvict's rule.
		for _, key := range c.keySnapshot() {
			//: the caller stopped.
			if !yield(key) {
				//: the rest of the snapshot is dropped.
				return
			}
		}
	}
}

// keySnapshot copies the keys present and unexpired, under the lock.
func (c *Cache[K, V]) keySnapshot() []K {
	c.mu.Lock()
	defer c.mu.Unlock()
	//: the copy the loop ranges over.
	return c.core.keySnapshot()
}

// Len returns the current live entry count (including not-yet-reaped expired
// entries — expiry is lazy unless Expire runs).
func (c *Cache[K, V]) Len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	//: the map length is the authoritative live count.
	return c.core.Len()
}

// Purge drops every entry without firing OnEvict.
func (c *Cache[K, V]) Purge() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.core.Purge()
}

// Stats returns a point-in-time copy of the hit/miss/eviction counters.
func (c *Cache[K, V]) Stats() StatsValue {
	c.mu.Lock()
	defer c.mu.Unlock()
	//: copy the counters under the lock.
	return c.core.Stats()
}
