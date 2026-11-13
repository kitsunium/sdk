package cache

import (
	"iter"
	"math"
	"time"

	"github.com/kitsunium/sdk/internal/kernel/clock"
)

// UnlockedMap is the cache without its lock: the same LRU or S3-FIFO policy, TTL,
// bounds, sweep and counters as Cache, for a caller that already serialises
// every call under a lock of its own — a store that keeps more state beside
// the entries, which a second lock inside would only slow down. It is NOT
// safe for concurrent use: two goroutines calling it at once race, which the
// race detector reports. OnEvict runs at the end of the call that evicted,
// once the cache is consistent again, so it may call back into the cache;
// under the caller's lock it runs under that lock. The zero value is NOT
// usable — construct with NewUnlockedMap.
type UnlockedMap[K comparable, V any] struct {
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
	// expiry orders the entries that carry a deadline, earliest first.
	expiry deadlineHeap[K, V]
	// epoch is the instant every deadline counts from: the clock's reading at
	// the first deadline, set once epochSet says it was read.
	epoch     time.Time
	epochSet  bool
	hits      uint64
	misses    uint64
	evictions uint64
}

// NewUnlockedMap builds an UnlockedMap from cfg, as NewCache builds a Cache. A nil
// cfg.Clock defaults to clock.System.
func NewUnlockedMap[K comparable, V any](cfg Config[K, V]) *UnlockedMap[K, V] {
	built := &UnlockedMap[K, V]{}
	built.init(cfg)
	//: ready.
	return built
}

// init configures c from cfg; NewUnlockedMap and NewCache share it.
func (c *UnlockedMap[K, V]) init(cfg Config[K, V]) {
	c.clk = cfg.Clock
	//: a nil clock falls back to the system source.
	if c.clk == nil {
		//: the system clock is the production default.
		c.clk = clock.System
	}
	//: a negative MaxEntries is a bad option, not a capacity. Clamp it to 0 —
	//: the value evictIfNeeded already reads as "unbounded". Note this is NOT
	//: crash avoidance: make(map, n) silently clamps a negative hint to 0 (only
	//: make([]T, n) panics), so the old code worked by accident. The clamp makes
	//: ADR 0025's "bad options clamp to sane defaults" explicit in the field
	//: itself, instead of leaning on an undocumented runtime detail — and Purge
	//: re-pre-sizes from this same field, so it inherits the guarantee.
	c.maxEntries = max(cfg.MaxEntries, 0)
	c.maxWeight = max(cfg.MaxWeight, 0)
	c.weightOf = cfg.WeightOf
	c.defaultTTL = cfg.DefaultTTL
	c.onEvict = cfg.OnEvict
	c.policy = PolicyLRU
	c.items = make(map[K]*entry[K, V], c.maxEntries)
	//: an unknown policy runs as LRU, and Stats says so; only S3-FIFO needs a
	//: ghost, so only it pays for one.
	if cfg.Policy == PolicyS3FIFO {
		c.policy = PolicyS3FIFO
		c.ghost = newGhostSet[K]()
	}
}

// Fetch returns the value for key and whether it was present and unexpired. A
// hit is recorded by the policy, as Cache.Fetch records it.
func (c *UnlockedMap[K, V]) Fetch(key K) (value V, ok bool) {
	var pending []evictionValue[K, V]
	value, ok = c.fetch(key, &pending)
	c.notifyEvicted(pending)
	//: the value, once the observer has seen what the lookup evicted.
	return value, ok
}

// Set stores key=val with the cache's default TTL.
func (c *UnlockedMap[K, V]) Set(key K, val V) {
	var pending []evictionValue[K, V]
	c.set(key, val, c.defaultTTL, &pending)
	c.notifyEvicted(pending)
}

// SetTTL stores key=val with an explicit ttl (0 = no expiry), overriding the
// default for this entry only.
func (c *UnlockedMap[K, V]) SetTTL(key K, val V, ttl time.Duration) {
	var pending []evictionValue[K, V]
	c.set(key, val, ttl, &pending)
	c.notifyEvicted(pending)
}

// Delete removes key if present, without firing OnEvict.
func (c *UnlockedMap[K, V]) Delete(key K) {
	c.remove(key)
}

// Expire removes every entry whose deadline has passed and reports how many
// it removed, each counted as an eviction and passed to OnEvict, as
// Cache.Expire does.
func (c *UnlockedMap[K, V]) Expire() int {
	var pending []evictionValue[K, V]
	reaped := c.expire(&pending)
	c.notifyEvicted(pending)
	//: the count reaped by this sweep.
	return reaped
}

// Keys yields the keys present and unexpired when the loop starts, in no
// particular order, from a copy: the loop's body may call the cache, and
// what it changes is not seen by the loop. A walk is not a read: no entry is
// promoted and no counter moves. Each loop allocates one slice of the keys.
func (c *UnlockedMap[K, V]) Keys() iter.Seq[K] {
	//: the snapshot is taken when the loop starts, not when Keys is called.
	return func(yield func(K) bool) {
		//: the copy, so the body may write.
		for _, key := range c.keySnapshot() {
			//: the caller stopped.
			if !yield(key) {
				//: the rest of the snapshot is dropped.
				return
			}
		}
	}
}

// Len returns the current entry count, including expired entries not yet
// reaped.
func (c *UnlockedMap[K, V]) Len() int {
	//: the map length is the authoritative live count.
	return len(c.items)
}

// Purge drops every entry without firing OnEvict.
func (c *UnlockedMap[K, V]) Purge() {
	//: replace the map and clear both lists, the deadlines and the ghost.
	c.items = make(map[K]*entry[K, V], c.maxEntries)
	c.main = entryList[K, V]{}
	c.small = entryList[K, V]{}
	c.expiry = deadlineHeap[K, V]{}
	//: only S3-FIFO built a ghost.
	if c.policy == PolicyS3FIFO {
		c.ghost.reset()
	}
}

// Stats returns a copy of the hit/miss/eviction counters.
func (c *UnlockedMap[K, V]) Stats() StatsValue {
	var weight int64
	//: a cache that weighs nothing reports no weight.
	if c.maxWeight > 0 || c.weightOf != nil {
		weight = c.main.weight + c.small.weight
	}
	//: copy the counters.
	return StatsValue{Hits: c.hits, Misses: c.misses, Evictions: c.evictions, Weight: weight, Policy: c.policy}
}

// fetch looks key up, reaping it when its deadline has passed, and records a
// hit by the policy. Caller holds the lock.
func (c *UnlockedMap[K, V]) fetch(key K, pending *[]evictionValue[K, V]) (value V, ok bool) {
	ent := c.items[key]
	//: a missing key, or one whose deadline has passed, is a miss.
	if ent == nil || (ent.slot != 0 && c.sinceEpoch() >= ent.deadline) {
		//: a lazily-expired entry is evicted (OnEvict queued) before the miss.
		if ent != nil {
			c.removeEvict(ent, pending)
		}
		c.misses++
		var zero V
		//: the zero value on a miss.
		return zero, false
	}
	//: S3-FIFO counts the hit; LRU promotes it — inlined, the hot path.
	if c.policy == PolicyS3FIFO {
		c.touch(ent)
	} else {
		c.main.moveFront(ent)
	}
	c.hits++
	//: the live value.
	return ent.val, true
}

// set inserts or updates key=val with ttl (0 or less = no deadline), and
// applies the bounds. Caller holds the lock.
func (c *UnlockedMap[K, V]) set(key K, val V, ttl time.Duration, pending *[]evictionValue[K, V]) {
	var at int64
	//: only a positive ttl sets a deadline, off the clock read once.
	if ttl > 0 {
		at = c.deadlineAfter(ttl)
	}
	//: updating an existing key reuses its node (no OnEvict for it).
	if ent := c.items[key]; ent != nil {
		c.overwrite(ent, val, ttl > 0, at, pending)
		//: the update is complete — no insert path.
		return
	}
	//: a new key allocates one node, placed by the policy.
	ent := &entry[K, V]{key: key, val: val, weight: 1}
	//: only a weigher makes a weight other than one.
	if c.weightOf != nil {
		ent.weight = c.weigh(key, val)
	}
	c.items[key] = ent
	//: LRU, the zero policy, pushes onto main; S3-FIFO picks the queue.
	if c.policy == PolicyS3FIFO {
		c.admit(ent)
	} else {
		c.main.pushFront(ent)
	}
	//: an entry without a deadline never touches the heap.
	if ttl > 0 {
		c.setDeadline(ent, at)
	}
	//: an unbounded cache has nothing to enforce.
	if c.maxEntries > 0 || c.maxWeight > 0 {
		c.evictIfNeeded(ent, pending)
	}
}

// overwrite refreshes a live entry's value, weight and deadline, and records
// the write as a hit would be recorded. Caller holds the lock.
func (c *UnlockedMap[K, V]) overwrite(ent *entry[K, V], val V, timed bool, at int64, pending *[]evictionValue[K, V]) {
	//: only a weigher can change an entry's weight; without one it stays 1.
	if c.weightOf != nil {
		c.reweigh(ent, c.weigh(ent.key, val))
	}
	ent.val = val
	//: a positive ttl re-stamps the deadline; none clears it.
	if timed {
		c.setDeadline(ent, at)
	} else {
		c.clearDeadline(ent)
	}
	c.touch(ent)
	//: a heavier value may push the cache over its weight bound; a count
	//: bound cannot move on an overwrite.
	if c.maxWeight > 0 {
		c.evictIfNeeded(ent, pending)
	}
}

// remove takes key out, if present, without an eviction. Caller holds the
// lock.
func (c *UnlockedMap[K, V]) remove(key K) {
	//: only a present key needs unlinking + map removal.
	if ent := c.items[key]; ent != nil {
		//: unlink from its list, its deadline and the map.
		c.detach(ent)
	}
}

// expire evicts every entry whose deadline has passed, the clock read once,
// and reports how many. Caller holds the lock.
func (c *UnlockedMap[K, V]) expire(pending *[]evictionValue[K, V]) int {
	//: no deadline anywhere: nothing can be due, and no clock is read.
	if c.expiry.size() == 0 {
		//: nothing reaped.
		return 0
	}
	now := c.sinceEpoch()
	reaped := 0
	//: the heap's top is the earliest deadline: stop at the first live one.
	for {
		top, at := c.expiry.top()
		//: drained, or the earliest deadline is still ahead.
		if top == nil || now < at {
			//: the count reaped by this sweep.
			return reaped
		}
		c.removeEvict(top, pending)
		reaped++
	}
}

// keySnapshot copies the keys present and unexpired. Caller holds the lock.
func (c *UnlockedMap[K, V]) keySnapshot() []K {
	keys := make([]K, 0, len(c.items))
	var now int64
	read := false
	//: a map has no order; the caller was promised none.
	for key, ent := range c.items {
		//: an entry past its deadline is absent, as Fetch reads it; the clock
		//: is read once, and only when an entry carries a deadline.
		if ent.slot != 0 {
			//: the first deadline met reads the clock.
			if !read {
				now, read = c.sinceEpoch(), true
			}
			//: expired once the clock reaches the deadline, as fetch says.
			if now >= ent.deadline {
				continue
			}
		}
		keys = append(keys, key)
	}
	//: the copy the loop ranges over.
	return keys
}

// deadlineAfter returns the deadline ttl (positive) from now, in nanoseconds
// since the epoch, the first call fixing the epoch. The difference of two
// clock readings is taken on their monotonic readings when both carry one,
// as time.Time.Before compares them, so a deadline means what a time.Time
// deadline meant; a sum past the int64 range — a TTL of centuries — is held
// at its maximum. Caller holds the lock.
func (c *UnlockedMap[K, V]) deadlineAfter(ttl time.Duration) int64 {
	now := c.clk.Now()
	//: the first deadline fixes the instant every other counts from.
	if !c.epochSet {
		c.epoch, c.epochSet = now, true
	}
	since := now.Sub(c.epoch)
	//: a sum past the range is held at its maximum.
	if since > math.MaxInt64-ttl {
		//: as far as an int64 reaches.
		return math.MaxInt64
	}
	//: the offset of the deadline.
	return int64(since + ttl)
}

// sinceEpoch reads the clock as nanoseconds since the epoch. Called only once
// some entry carries a deadline, so the epoch is set. Caller holds the lock.
func (c *UnlockedMap[K, V]) sinceEpoch() int64 {
	//: the same subtraction deadlineAfter makes.
	return int64(c.clk.Now().Sub(c.epoch))
}

// setDeadline gives ent the deadline at, keeping the expiry heap in step.
// Caller holds the lock.
func (c *UnlockedMap[K, V]) setDeadline(ent *entry[K, V], at int64) {
	ent.deadline = at
	//: a re-stamped entry moves in place; a new one is pushed.
	if ent.slot != 0 {
		c.expiry.fix(ent, at)
		//: re-ordered.
		return
	}
	c.expiry.push(ent, at)
}

// clearDeadline takes ent's deadline away, if it had one. Caller holds the
// lock.
func (c *UnlockedMap[K, V]) clearDeadline(ent *entry[K, V]) {
	//: the common case — no deadline before, none now — touches nothing.
	if ent.slot != 0 {
		c.expiry.remove(ent)
	}
	ent.deadline = 0
}

// weigh returns what key=val counts against MaxWeight.
func (c *UnlockedMap[K, V]) weigh(key K, val V) int32 {
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
// the lock.
func (c *UnlockedMap[K, V]) reweigh(ent *entry[K, V], weight int32) {
	list := c.listOf(ent)
	list.weight += int64(weight) - int64(ent.weight)
	ent.weight = weight
}

// overCapacity reports whether a bound is exceeded. The weight bound never
// asks a lone entry to leave: an entry heavier than MaxWeight is stored and
// evicts everything else. Caller holds the lock.
func (c *UnlockedMap[K, V]) overCapacity() bool {
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
// live one its slot. Caller holds the lock.
func (c *UnlockedMap[K, V]) evictIfNeeded(fresh *entry[K, V], pending *[]evictionValue[K, V]) {
	//: the common case: within every bound, nothing to set up.
	if !c.overCapacity() {
		//: nothing to evict.
		return
	}
	var now int64
	read := false
	//: one victim per pass, until the bounds hold.
	for c.overCapacity() {
		//: a dead entry first, the clock read once for the whole loop.
		if c.expiry.size() > 0 {
			//: the first pass that needs the time reads it.
			if !read {
				now, read = c.sinceEpoch(), true
			}
			//: the earliest deadline has passed: that entry is the victim.
			if top, at := c.expiry.top(); top != fresh && now >= at {
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
// onto pending. Caller holds the lock.
//
// OnEvict is deliberately NOT invoked here. Under a Cache every caller holds
// its mutex and sync.Mutex is not reentrant, so a callback that touches the
// cache would deadlock outright, and a merely slow one would hold every other
// operation off for its whole duration — turning eviction into an unbounded
// critical section; and an UnlockedMap must be consistent before user code runs.
// Callers drain pending through notifyEvicted once the operation is done.
func (c *UnlockedMap[K, V]) removeEvict(ent *entry[K, V], pending *[]evictionValue[K, V]) {
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
// the lock.
func (c *UnlockedMap[K, V]) detach(ent *entry[K, V]) {
	c.unlink(ent)
	//: an entry with a deadline also leaves the heap.
	if ent.slot != 0 {
		c.expiry.remove(ent)
	}
	delete(c.items, ent.key)
}

// notifyEvicted fires OnEvict for every queued pair. Under a Cache it MUST be
// called with the mutex released — the callback is user code and may re-enter
// the cache.
func (c *UnlockedMap[K, V]) notifyEvicted(pending []evictionValue[K, V]) {
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

// listOf returns the list ent sits in. Caller holds the lock.
func (c *UnlockedMap[K, V]) listOf(ent *entry[K, V]) *entryList[K, V] {
	//: only S3-FIFO's probation uses the small list.
	if ent.queue == queueSmall {
		//: probation.
		return &c.small
	}
	//: the LRU list, or S3-FIFO's main.
	return &c.main
}

// unlink detaches ent from its list, fixing head/tail. Caller holds the lock.
func (c *UnlockedMap[K, V]) unlink(ent *entry[K, V]) {
	//: the list its queue names.
	c.listOf(ent).unlink(ent)
}
