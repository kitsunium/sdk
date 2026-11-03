package cache

import (
	"iter"
	"math"
	"sync"
	"time"

	"github.com/kitsunium/sdk/internal/kernel/clock"
	"github.com/kitsunium/sdk/internal/kernel/collections/heap"
)

// cacheLine is the padding that separates two Caches allocated side by side.
const cacheLine int = 64

// Cache is a generic LRU+TTL cache safe for concurrent use. The zero value is
// NOT usable — construct with NewCache.
type Cache[K comparable, V any] struct {
	mu         sync.RWMutex
	clk        clock.Clock
	maxEntries int
	maxWeight  int64
	weightOf   func(key K, val V) int64
	defaultTTL time.Duration
	onEvict    func(key K, val V)
	policy     Policy
	items      map[K]*entry[K, V]
	// main is the LRU list, and S3-FIFO's main queue.
	main entryList[K, V]
	// small is S3-FIFO's probationary queue; empty under LRU.
	small entryList[K, V]
	// ghost remembers the keys probation evicted (S3-FIFO only).
	ghost ghostSet[K]
	// expiry orders the entries that carry a deadline, earliest first; nil
	// until the first one is stored.
	expiry    *heap.IndexedHeap[*entry[K, V]]
	hits      uint64
	misses    uint64
	evictions uint64
	// The padding keeps the counters every operation writes off the cache line of the
	// mutex of whatever is allocated next — in a Striped, the next stripe.
	_ [cacheLine]byte
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
	//: a negative MaxEntries is a bad option, not a capacity. Clamp it to 0 —
	//: the value evictIfNeeded already reads as "unbounded". Note this is NOT
	//: crash avoidance: make(map, n) silently clamps a negative hint to 0 (only
	//: make([]T, n) panics), so the old code worked by accident. The clamp makes
	//: ADR 0025's "bad options clamp to sane defaults" explicit in the field
	//: itself, instead of leaning on an undocumented runtime detail — and Purge
	//: re-pre-sizes from this same field, so it inherits the guarantee.
	maxEntries := max(cfg.MaxEntries, 0)
	built := &Cache[K, V]{
		clk:        clk,
		maxEntries: maxEntries,
		maxWeight:  max(cfg.MaxWeight, 0),
		weightOf:   cfg.WeightOf,
		defaultTTL: cfg.DefaultTTL,
		onEvict:    cfg.OnEvict,
		policy:     PolicyLRU,
		items:      make(map[K]*entry[K, V], maxEntries),
	}
	//: an unknown policy runs as LRU, and Stats says so; only S3-FIFO needs a
	//: ghost, so only it pays for one.
	if cfg.Policy == PolicyS3FIFO {
		built.policy = PolicyS3FIFO
		built.ghost = newGhostSet[K]()
	}
	//: a fresh cache starts with a pre-sized map and no list nodes.
	return built
}

// Fetch returns the value for key and whether it was present and unexpired. A
// hit promotes the entry to most-recently-used (hence Fetch, not a pure Get).
func (c *Cache[K, V]) Fetch(key K) (value V, ok bool) {
	//: registered BEFORE the unlock defer so LIFO runs it AFTER the unlock —
	//: OnEvict is user code and must never see the lock held.
	var pending []evictionValue[K, V]
	defer func() { c.notifyEvicted(pending) }()
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
		//: drop it (queuing OnEvict for after the unlock) and count the miss.
		c.removeEvict(ent, &pending)
		c.misses++
		var zero V
		//: hand back the zero value on the expired miss.
		return zero, false
	}
	//: a live hit is recorded by the policy and returns the value.
	c.touch(ent)
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
// eviction when a bound is exceeded.
func (c *Cache[K, V]) setTTL(key K, val V, ttl time.Duration) {
	//: registered BEFORE the unlock defer so LIFO runs it AFTER the unlock.
	var pending []evictionValue[K, V]
	defer func() { c.notifyEvicted(pending) }()
	c.mu.Lock()
	defer c.mu.Unlock()
	//: a positive ttl becomes an absolute deadline; otherwise no expiry.
	var exp time.Time
	//: only a positive ttl sets a deadline.
	if ttl > 0 {
		//: deadline relative to the injectable clock.
		exp = c.clk.Now().Add(ttl)
	}
	//: updating an existing key reuses its node (no OnEvict for it).
	if ent, found := c.items[key]; found {
		c.overwrite(ent, val, exp, &pending)
		//: the update is complete — no insert path.
		return
	}
	//: a new key allocates one node, placed by the policy.
	ent := &entry[K, V]{key: key, val: val, weight: c.weigh(key, val)}
	c.items[key] = ent
	c.admit(ent)
	c.setDeadline(ent, exp)
	//: enforce the bounds after the insert.
	c.evictIfNeeded(ent, &pending)
}

// overwrite refreshes a live entry's value, weight and expiry, and records the
// write as a hit would be recorded. Caller holds mu.
func (c *Cache[K, V]) overwrite(ent *entry[K, V], val V, exp time.Time, pending *[]evictionValue[K, V]) {
	//: only a weigher can change an entry's weight; without one it stays 1.
	if c.weightOf != nil {
		c.reweigh(ent, c.weigh(ent.key, val))
	}
	ent.val = val
	c.setDeadline(ent, exp)
	c.touch(ent)
	//: a heavier value may push the cache over its weight bound; a count
	//: bound cannot move on an overwrite.
	if c.maxWeight > 0 {
		c.evictIfNeeded(ent, pending)
	}
}

// Delete removes key if present. An explicit Delete does NOT fire OnEvict (that
// hook is for capacity/expiry removals only).
func (c *Cache[K, V]) Delete(key K) {
	c.mu.Lock()
	defer c.mu.Unlock()
	//: only a present key needs unlinking + map removal.
	if ent, found := c.items[key]; found {
		//: unlink from its list, its deadline and the map.
		c.detach(ent)
	}
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
	defer func() { c.notifyEvicted(pending) }()
	c.mu.Lock()
	defer c.mu.Unlock()
	//: no deadline anywhere: nothing can be due, and no clock is read.
	if c.expiry == nil || c.expiry.Len() == 0 {
		//: nothing reaped.
		return 0
	}
	now := c.clk.Now()
	reaped := 0
	//: the heap's top is the earliest deadline: stop at the first live one.
	for {
		top, _, ok := c.expiry.Peek()
		//: drained, or the earliest deadline is still ahead.
		if !ok || now.Before(top.expireAt) {
			//: the count reaped by this sweep.
			return reaped
		}
		c.removeEvict(top, &pending)
		reaped++
	}
}

// Keys yields the keys present and unexpired when the loop starts, in no
// particular order. They are copied under the read lock and yielded after it
// is released, so the loop's body may call the cache — Set, Delete, Fetch —
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

// keySnapshot copies the keys present and unexpired, under the read lock.
func (c *Cache[K, V]) keySnapshot() []K {
	c.mu.RLock()
	defer c.mu.RUnlock()
	keys := make([]K, 0, len(c.items))
	var now time.Time
	//: a map has no order; the caller was promised none.
	for key, ent := range c.items {
		//: an entry past its deadline is absent, as Fetch reads it; the clock
		//: is read once, and only when an entry carries a deadline.
		if !ent.expireAt.IsZero() {
			//: the first deadline met reads the clock.
			if now.IsZero() {
				now = c.clk.Now()
			}
			//: expired once the clock reaches the deadline, as expired says.
			if !now.Before(ent.expireAt) {
				continue
			}
		}
		keys = append(keys, key)
	}
	//: the copy the loop ranges over.
	return keys
}

// Len returns the current live entry count (including not-yet-reaped expired
// entries — expiry is lazy unless Expire runs).
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
	//: replace the map and clear both lists, the deadlines and the ghost.
	c.items = make(map[K]*entry[K, V], c.maxEntries)
	c.main = entryList[K, V]{}
	c.small = entryList[K, V]{}
	c.expiry = nil
	//: only S3-FIFO built a ghost.
	if c.policy == PolicyS3FIFO {
		c.ghost.reset()
	}
}

// Stats returns a point-in-time copy of the hit/miss/eviction counters.
func (c *Cache[K, V]) Stats() StatsValue {
	c.mu.RLock()
	defer c.mu.RUnlock()
	var weight int64
	//: a cache that weighs nothing reports no weight.
	if c.maxWeight > 0 || c.weightOf != nil {
		weight = c.main.weight + c.small.weight
	}
	//: copy the counters under the lock.
	return StatsValue{Hits: c.hits, Misses: c.misses, Evictions: c.evictions, Weight: weight, Policy: c.policy}
}

// weigh returns what key=val counts against MaxWeight.
func (c *Cache[K, V]) weigh(key K, val V) int32 {
	//: without a weigher every entry weighs 1, so MaxWeight is a count.
	if c.weightOf == nil {
		//: one unit.
		return 1
	}
	//: a negative weight would let an entry pay for others; it counts as 0,
	//: and one past the 32 bits an entry holds counts as their maximum.
	return int32(min(max(c.weightOf(key, val), 0), math.MaxInt32))
}

// reweigh changes ent's weight in place, keeping its list's sum. Caller holds
// mu.
func (c *Cache[K, V]) reweigh(ent *entry[K, V], weight int32) {
	list := c.listOf(ent)
	list.weight += int64(weight) - int64(ent.weight)
	ent.weight = weight
}

// setDeadline gives ent the deadline exp (zero = none) and keeps the expiry
// heap in step. Caller holds mu.
func (c *Cache[K, V]) setDeadline(ent *entry[K, V], exp time.Time) {
	ent.expireAt = exp
	//: no deadline: the entry leaves the heap if it was in it.
	if exp.IsZero() {
		//: the common case — no deadline before, none now — touches nothing.
		//: only an entry that had a deadline has a node to remove.
		if ent.expiry != (heap.Handle{}) {
			c.expiry.Remove(ent.expiry)
			ent.expiry = heap.Handle{}
		}
		//: nothing to order.
		return
	}
	//: the first deadline the cache sees builds the heap.
	if c.expiry == nil {
		c.expiry = heap.NewIndexed(byDeadline[K, V])
	}
	//: a re-stamped entry moves in place; a new one is pushed.
	if ent.expiry != (heap.Handle{}) {
		c.expiry.Update(ent.expiry, ent)
		//: re-ordered.
		return
	}
	ent.expiry = c.expiry.Push(ent)
}

// byDeadline orders entries earliest deadline first.
func byDeadline[K comparable, V any](a, b *entry[K, V]) int {
	//: time.Time's own order.
	return a.expireAt.Compare(b.expireAt)
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

// overCapacity reports whether a bound is exceeded. The weight bound never
// asks a lone entry to leave: an entry heavier than MaxWeight is stored and
// evicts everything else. Caller holds mu.
func (c *Cache[K, V]) overCapacity() bool {
	count := len(c.items)
	//: the count bound.
	if c.maxEntries > 0 && count > c.maxEntries {
		//: over.
		return true
	}
	//: the weight bound, with something besides one entry to evict.
	return c.maxWeight > 0 && count > 1 && c.main.weight+c.small.weight > c.maxWeight
}

// evictIfNeeded removes entries while a bound is exceeded, never fresh — the
// entry the current Set wrote. An entry whose deadline has passed goes before
// any live one: the heap's top is tried first, so a dead entry never costs a
// live one its slot. Caller holds mu.
func (c *Cache[K, V]) evictIfNeeded(fresh *entry[K, V], pending *[]evictionValue[K, V]) {
	//: the common case: within every bound, nothing to set up.
	if !c.overCapacity() {
		//: nothing to evict.
		return
	}
	var now time.Time
	read := false
	//: one victim per pass, until the bounds hold.
	for c.overCapacity() {
		//: a dead entry first, the clock read once for the whole loop.
		if c.expiry != nil && c.expiry.Len() > 0 {
			//: the first pass that needs the time reads it.
			if !read {
				now, read = c.clk.Now(), true
			}
			//: the earliest deadline has passed: that entry is the victim.
			if top, _, _ := c.expiry.Peek(); top != fresh && !now.Before(top.expireAt) {
				c.removeEvict(top, pending)
				//: the next pass.
				continue
			}
		}
		//: the policy's victim; none left means the bounds cannot be met.
		if !c.evictOne(fresh, pending) {
			//: nothing more may go.
			return
		}
	}
}

// removeEvict detaches ent, counts an eviction, and QUEUES the evicted pair
// onto pending. Caller holds mu.
//
// OnEvict is deliberately NOT invoked here. Every caller holds c.mu and
// sync.Mutex is not reentrant, so a callback that touches the cache would
// deadlock outright, and a merely slow one would hold every other operation
// off for its whole duration — turning eviction into an unbounded critical
// section. Callers drain pending through notifyEvicted after unlocking.
func (c *Cache[K, V]) removeEvict(ent *entry[K, V], pending *[]evictionValue[K, V]) {
	//: detach from the list, the deadlines and the map.
	c.detach(ent)
	//: record the eviction.
	c.evictions++
	//: queue the notification only when an observer is configured.
	if c.onEvict != nil {
		//: carry the pair out of the locked section by value; ent is recycled.
		*pending = append(*pending, evictionValue[K, V]{key: ent.key, val: ent.val})
	}
}

// detach takes ent out of its list, the expiry heap and the map. Caller holds
// mu.
func (c *Cache[K, V]) detach(ent *entry[K, V]) {
	c.unlink(ent)
	//: an entry with a deadline also leaves the heap.
	if ent.expiry != (heap.Handle{}) {
		c.expiry.Remove(ent.expiry)
		ent.expiry = heap.Handle{}
	}
	delete(c.items, ent.key)
}

// notifyEvicted fires OnEvict for every queued pair. It MUST be called with
// c.mu released — the callback is user code and may re-enter the cache.
func (c *Cache[K, V]) notifyEvicted(pending []evictionValue[K, V]) {
	//: the overwhelmingly common case is "nothing evicted" — exit cheaply.
	if len(pending) == 0 {
		//: no eviction happened, or no observer is configured.
		return
	}
	//: hand each evicted key+value to the observer, outside the lock.
	for _, ev := range pending {
		//: user code runs here; a panic propagates to the caller unchanged.
		c.onEvict(ev.key, ev.val)
	}
}

// listOf returns the list ent sits in. Caller holds mu.
func (c *Cache[K, V]) listOf(ent *entry[K, V]) *entryList[K, V] {
	//: only S3-FIFO's probation uses the small list.
	if ent.queue == queueSmall {
		//: probation.
		return &c.small
	}
	//: the LRU list, or S3-FIFO's main.
	return &c.main
}

// unlink detaches ent from its list, fixing head/tail. Caller holds mu.
func (c *Cache[K, V]) unlink(ent *entry[K, V]) {
	//: the list its queue names.
	c.listOf(ent).unlink(ent)
}
