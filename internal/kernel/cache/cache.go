// Package cache provides a generic, concurrency-safe LRU + TTL cache
// (Cache[K,V]) — a kernel primitive (stdlib-only, no domain vocabulary). It
// reuses the kernel clock so TTL expiry is testable. Fetch on a hit is
// allocation-free (map lookup + intrusive-list splice); Set allocates one entry
// per new key. Reads and writes are serialised by a single mutex. Fetch is
// named Fetch (not Get) because a hit mutates state (LRU promotion + counters).
package cache

import (
	"sync"
	"time"

	"github.com/kitsunium/sdk/internal/kernel/clock"
)

// Cache is a generic LRU+TTL cache safe for concurrent use. The zero value is
// NOT usable — construct with NewCache.
type Cache[K comparable, V any] struct {
	mu         sync.RWMutex
	clk        clock.Clock
	maxEntries int
	defaultTTL time.Duration
	onEvict    func(key K, val V)
	items      map[K]*entry[K, V]
	head       *entry[K, V] // most-recently-used
	tail       *entry[K, V] // least-recently-used
	hits       uint64
	misses     uint64
	evictions  uint64
}

// NewCache builds a Cache from cfg. A nil cfg.Clock defaults to clock.System.
func NewCache[K comparable, V any](cfg Config[K, V]) *Cache[K, V] {
	//: default the clock so callers need not wire it in production.
	clk := cfg.Clock
	//: a nil clock falls back to the system source.
	if clk == nil {
		//: the system clock is the production default.
		clk = clock.System
	}
	//: a fresh cache starts with a pre-sized map and no list nodes.
	return &Cache[K, V]{
		clk:        clk,
		maxEntries: cfg.MaxEntries,
		defaultTTL: cfg.DefaultTTL,
		onEvict:    cfg.OnEvict,
		items:      make(map[K]*entry[K, V], cfg.MaxEntries),
	}
}

// Fetch returns the value for key and whether it was present and unexpired. A
// hit promotes the entry to most-recently-used (hence Fetch, not a pure Get).
func (c *Cache[K, V]) Fetch(key K) (value V, ok bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	//: absence is a clean miss.
	ent, found := c.items[key]
	//: a missing key returns the zero value.
	if !found {
		//: count the miss and return the zero value.
		c.misses++
		var zero V
		//: hand back the zero value on the miss.
		return zero, false
	}
	//: a lazily-expired entry is evicted and reported as a miss.
	if c.expired(ent) {
		//: drop it (firing OnEvict) and count the miss.
		c.removeEvict(ent)
		c.misses++
		var zero V
		//: hand back the zero value on the expired miss.
		return zero, false
	}
	//: a live hit moves to the front and returns the value.
	c.moveFront(ent)
	c.hits++
	//: return the live value.
	return ent.val, true
}

// Set stores key=val with the cache's default TTL.
func (c *Cache[K, V]) Set(key K, val V) {
	//: delegate to the TTL form with the configured default.
	c.setTTL(key, val, c.defaultTTL)
}

// SetTTL stores key=val with an explicit ttl (0 = no expiry), overriding the
// default for this entry only.
func (c *Cache[K, V]) SetTTL(key K, val V, ttl time.Duration) {
	//: delegate to the shared insert/update path.
	c.setTTL(key, val, ttl)
}

// setTTL inserts or updates key=val, computing the absolute expiry and applying
// LRU eviction when the capacity is exceeded.
func (c *Cache[K, V]) setTTL(key K, val V, ttl time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	//: a positive ttl becomes an absolute deadline; otherwise no expiry.
	var exp time.Time
	//: only a positive ttl sets a deadline.
	if ttl > 0 {
		//: deadline relative to the injectable clock.
		exp = c.clk.Now().Add(ttl)
	}
	//: updating an existing key reuses its node (no eviction, no OnEvict).
	if ent, found := c.items[key]; found {
		//: refresh value + expiry and promote to most-recently-used.
		ent.val = val
		ent.expireAt = exp
		c.moveFront(ent)
		//: the update is complete — no insert path.
		return
	}
	//: a new key allocates one node, inserted at the front.
	ent := &entry[K, V]{key: key, val: val, expireAt: exp}
	c.items[key] = ent
	c.pushFront(ent)
	//: enforce the capacity bound after the insert.
	c.evictIfNeeded()
}

// Delete removes key if present. An explicit Delete does NOT fire OnEvict (that
// hook is for capacity/expiry removals only).
func (c *Cache[K, V]) Delete(key K) {
	c.mu.Lock()
	defer c.mu.Unlock()
	//: only a present key needs unlinking + map removal.
	if ent, found := c.items[key]; found {
		//: unlink from the LRU list and drop from the map.
		c.unlink(ent)
		delete(c.items, key)
	}
}

// Len returns the current live entry count (including not-yet-reaped expired
// entries — expiry is lazy).
func (c *Cache[K, V]) Len() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	//: the map length is the authoritative live count.
	return len(c.items)
}

// Purge drops every entry without firing OnEvict.
func (c *Cache[K, V]) Purge() {
	c.mu.Lock()
	defer c.mu.Unlock()
	//: replace the map and clear the list head/tail in one shot.
	c.items = make(map[K]*entry[K, V], c.maxEntries)
	c.head = nil
	c.tail = nil
}

// Stats returns a point-in-time copy of the hit/miss/eviction counters.
func (c *Cache[K, V]) Stats() StatsValue {
	c.mu.RLock()
	defer c.mu.RUnlock()
	//: copy the counters under the lock.
	return StatsValue{Hits: c.hits, Misses: c.misses, Evictions: c.evictions}
}

// expired reports whether ent carries a deadline that has passed. Caller holds mu.
func (c *Cache[K, V]) expired(ent *entry[K, V]) bool {
	//: a zero deadline never expires.
	if ent.expireAt.IsZero() {
		//: no TTL was set on this entry.
		return false
	}
	//: expired once the clock reaches (is not before) the deadline.
	return !c.clk.Now().Before(ent.expireAt)
}

// evictIfNeeded removes least-recently-used entries while over capacity. Caller
// holds mu.
func (c *Cache[K, V]) evictIfNeeded() {
	//: a non-positive max means unbounded — nothing to evict.
	for c.maxEntries > 0 && len(c.items) > c.maxEntries {
		//: the tail is the least-recently-used victim.
		c.removeEvict(c.tail)
	}
}

// removeEvict unlinks ent, drops it from the map, counts an eviction, and fires
// OnEvict. Caller holds mu.
func (c *Cache[K, V]) removeEvict(ent *entry[K, V]) {
	//: detach from the list and the map.
	c.unlink(ent)
	delete(c.items, ent.key)
	//: record the eviction.
	c.evictions++
	//: notify the observer when one is configured.
	if c.onEvict != nil {
		//: hand the evicted key+value to the callback.
		c.onEvict(ent.key, ent.val)
	}
}

// moveFront promotes ent to most-recently-used. Caller holds mu.
func (c *Cache[K, V]) moveFront(ent *entry[K, V]) {
	//: already the head — nothing to move.
	if c.head == ent {
		//: fast-path no-op.
		return
	}
	//: detach then re-insert at the front.
	c.unlink(ent)
	c.pushFront(ent)
}

// pushFront inserts ent at the head. Caller holds mu; ent MUST be detached.
func (c *Cache[K, V]) pushFront(ent *entry[K, V]) {
	//: the new node's next is the old head.
	ent.prev = nil
	ent.next = c.head
	//: the old head (if any) links back to the new node.
	if c.head != nil {
		//: maintain the back-link.
		c.head.prev = ent
	}
	//: the new node becomes the head.
	c.head = ent
	//: an empty list also gains its tail.
	if c.tail == nil {
		//: first node is both head and tail.
		c.tail = ent
	}
}

// unlink detaches ent from the list, fixing head/tail. Caller holds mu.
func (c *Cache[K, V]) unlink(ent *entry[K, V]) {
	//: bridge the previous node (or the head) across ent.
	if ent.prev != nil {
		//: previous node skips ent.
		ent.prev.next = ent.next
	} else {
		//: ent was the head — the next node becomes head.
		c.head = ent.next
	}
	//: bridge the next node (or the tail) across ent.
	if ent.next != nil {
		//: next node skips ent.
		ent.next.prev = ent.prev
	} else {
		//: ent was the tail — the previous node becomes tail.
		c.tail = ent.prev
	}
	//: clear ent's links so a stale node can't corrupt the list.
	ent.prev = nil
	ent.next = nil
}
