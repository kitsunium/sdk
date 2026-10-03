// Package cache — white-box tests for the intrusive LRU list and the expiry
// predicate. Head/tail consistency is invisible from outside the package: a
// corrupted link shows up much later as a lost entry or a wrong eviction
// victim, so the invariant is checked here at the point it is maintained.
package cache

import (
	"testing"
	"time"
)

// fixedClock is a settable clock for deterministic expiry tests.
type fixedClock struct{ now time.Time }

// Now returns the frozen instant.
func (f *fixedClock) Now() time.Time { return f.now }

// Since measures against the frozen instant.
func (f *fixedClock) Since(t time.Time) time.Duration { return f.now.Sub(t) }

// listKeys walks the list front-to-back and returns the keys in order, so a
// test can state the expected ordering as a plain string.
func listKeys[K comparable, V any](c *Cache[K, V]) []K {
	keys := make([]K, 0, len(c.items))
	//: walk from most- to least-recently-used.
	for node := c.head; node != nil; node = node.next {
		keys = append(keys, node.key)
	}
	return keys
}

// listKeysBackward walks tail-to-front. Comparing it against the reverse of
// listKeys is what proves the prev links agree with the next links; a list can
// read correctly forwards while being broken backwards, and eviction walks
// backwards.
func listKeysBackward[K comparable, V any](c *Cache[K, V]) []K {
	keys := make([]K, 0, len(c.items))
	//: walk from least- to most-recently-used.
	for node := c.tail; node != nil; node = node.prev {
		keys = append(keys, node.key)
	}
	return keys
}

// assertListIntact checks the forward order and that the backward walk is its
// exact reverse.
func assertListIntact[V any](t *testing.T, c *Cache[string, V], want []string) {
	t.Helper()
	forward := listKeys(c)
	if len(forward) != len(want) {
		t.Fatalf("list = %v, want %v", forward, want)
	}
	for i, k := range want {
		if forward[i] != k {
			t.Fatalf("list = %v, want %v", forward, want)
		}
	}
	backward := listKeysBackward(c)
	if len(backward) != len(forward) {
		t.Fatalf("backward walk = %v, forward = %v", backward, forward)
	}
	for i, k := range backward {
		if k != forward[len(forward)-1-i] {
			t.Fatalf("backward walk = %v, want the reverse of %v", backward, forward)
		}
	}
}

// Test_Cache_pushFront pins that inserting at the head keeps both ends of the
// list correct, including the first insertion where head and tail are the same
// node.
func Test_Cache_pushFront(t *testing.T) {
	t.Parallel()
	type tc struct {
		name   string
		insert []string
		want   []string
	}
	tests := []tc{
		{"the first node is both head and tail", []string{"a"}, []string{"a"}},
		{"the newest node becomes the head", []string{"a", "b"}, []string{"b", "a"}},
		{"three nodes stack front to back", []string{"a", "b", "c"}, []string{"c", "b", "a"}},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		cch := NewCache[string, int](Config[string, int]{})
		for _, k := range c.insert {
			ent := &entry[string, int]{key: k}
			cch.items[k] = ent
			cch.pushFront(ent)
		}
		assertListIntact(t, cch, c.want)
		//: the tail must be the oldest insertion, which is what eviction picks.
		if cch.tail.key != c.want[len(c.want)-1] {
			t.Errorf("tail = %q, want %q", cch.tail.key, c.want[len(c.want)-1])
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_Cache_unlink pins that removing a node repairs the list from whichever
// position it held. The head and tail cases are the ones that silently corrupt
// the ends, because they are the only ones where c.head / c.tail must change.
func Test_Cache_unlink(t *testing.T) {
	t.Parallel()
	type tc struct {
		name   string
		remove string
		want   []string
	}
	//: inserted a, b, c → the list reads c, b, a.
	tests := []tc{
		{"the head", "c", []string{"b", "a"}},
		{"a middle node", "b", []string{"c", "a"}},
		{"the tail", "a", []string{"c", "b"}},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		cch := NewCache[string, int](Config[string, int]{})
		for _, k := range []string{"a", "b", "c"} {
			ent := &entry[string, int]{key: k}
			cch.items[k] = ent
			cch.pushFront(ent)
		}

		victim := cch.items[c.remove]
		cch.unlink(victim)
		delete(cch.items, c.remove)
		assertListIntact(t, cch, c.want)
		//: a detached node must carry no stale links, or re-inserting it would
		//: splice the old neighbours back in.
		if victim.prev != nil || victim.next != nil {
			t.Errorf("unlinked %q still links to prev=%v next=%v", c.remove, victim.prev, victim.next)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_Cache_moveFront pins LRU promotion, including the no-op case where the
// node is already the head — the fast path that must not touch the links.
func Test_Cache_moveFront(t *testing.T) {
	t.Parallel()
	type tc struct {
		name    string
		promote string
		want    []string
	}
	//: inserted a, b, c → the list reads c, b, a.
	tests := []tc{
		{"the head stays put", "c", []string{"c", "b", "a"}},
		{"a middle node moves to the front", "b", []string{"b", "c", "a"}},
		{"the tail moves to the front", "a", []string{"a", "c", "b"}},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		cch := NewCache[string, int](Config[string, int]{})
		for _, k := range []string{"a", "b", "c"} {
			ent := &entry[string, int]{key: k}
			cch.items[k] = ent
			cch.pushFront(ent)
		}

		cch.moveFront(cch.items[c.promote])
		assertListIntact(t, cch, c.want)
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_Cache_expired pins the deadline predicate. The boundary matters: an
// entry whose deadline is exactly now must count as expired, or a TTL of d
// would in practice last a hair longer than d.
func Test_Cache_expired(t *testing.T) {
	t.Parallel()
	base := time.Unix(1_700_000_000, 0)
	type tc struct {
		name     string
		expireAt time.Time
		now      time.Time
		want     bool
	}
	tests := []tc{
		{"a zero deadline never expires", time.Time{}, base.Add(time.Hour), false},
		{"a deadline in the future has not passed", base.Add(time.Second), base, false},
		{"a deadline exactly now has passed", base, base, true},
		{"a deadline in the past has passed", base, base.Add(time.Second), true},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		cch := NewCache[string, int](Config[string, int]{Clock: &fixedClock{now: c.now}})
		got := cch.expired(&entry[string, int]{key: "k", expireAt: c.expireAt})
		if got != c.want {
			t.Errorf("expired() = %v, want %v", got, c.want)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_Cache_evictIfNeeded pins that eviction takes from the tail and takes as
// many as it needs, and that a non-positive cap means unbounded rather than
// "evict everything".
func Test_Cache_evictIfNeeded(t *testing.T) {
	t.Parallel()
	type tc struct {
		name        string
		maxEntries  int
		insert      []string
		wantRemains []string
		wantPending []string
	}
	tests := []tc{
		//: inserted a, b, c → the list reads c, b, a, so "a" is the victim.
		{"one over capacity takes the tail", 2, []string{"a", "b", "c"}, []string{"c", "b"}, []string{"a"}},
		{"two over capacity takes two from the tail", 1, []string{"a", "b", "c"}, []string{"c"}, []string{"a", "b"}},
		{"at capacity takes nothing", 3, []string{"a", "b", "c"}, []string{"c", "b", "a"}, nil},
		{"an unbounded cache never evicts", 0, []string{"a", "b", "c"}, []string{"c", "b", "a"}, nil},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		cch := NewCache[string, int](Config[string, int]{
			MaxEntries: c.maxEntries,
			OnEvict:    func(string, int) {},
		})
		for _, k := range c.insert {
			ent := &entry[string, int]{key: k}
			cch.items[k] = ent
			cch.pushFront(ent)
		}

		var pending []evictionValue[string, int]
		cch.evictIfNeeded(&pending)

		assertListIntact(t, cch, c.wantRemains)
		if len(pending) != len(c.wantPending) {
			t.Fatalf("pending = %v, want %v", pending, c.wantPending)
		}
		for i, want := range c.wantPending {
			if pending[i].key != want {
				t.Errorf("pending[%d] = %q, want %q", i, pending[i].key, want)
			}
		}
		//: the map must shed exactly what the list did.
		if len(cch.items) != len(c.wantRemains) {
			t.Errorf("items = %d, want %d", len(cch.items), len(c.wantRemains))
		}
		if got := cch.evictions; got != uint64(len(c.wantPending)) {
			t.Errorf("evictions = %d, want %d", got, len(c.wantPending))
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_Cache_removeEvict pins that the queue stays empty when no observer is
// configured. Queuing pairs nobody will read would keep every evicted value
// alive until the caller drained the slice, which is a leak in the one path
// that exists to shed memory.
func Test_Cache_removeEvict(t *testing.T) {
	t.Parallel()
	type tc struct {
		name        string
		onEvict     func(string, int)
		wantPending int
	}
	tests := []tc{
		{"with no observer nothing is queued", nil, 0},
		{"with an observer the pair is queued", func(string, int) {}, 1},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		cch := NewCache[string, int](Config[string, int]{OnEvict: c.onEvict})
		ent := &entry[string, int]{key: "k", val: 7}
		cch.items["k"] = ent
		cch.pushFront(ent)

		var pending []evictionValue[string, int]
		cch.removeEvict(ent, &pending)

		if len(pending) != c.wantPending {
			t.Fatalf("pending = %d entries, want %d", len(pending), c.wantPending)
		}
		//: the entry leaves the map and the list either way.
		if _, still := cch.items["k"]; still {
			t.Error("the evicted key is still in the map")
		}
		if cch.head != nil || cch.tail != nil {
			t.Error("the list is not empty after evicting its only node")
		}
		if cch.evictions != 1 {
			t.Errorf("evictions = %d, want 1", cch.evictions)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_Cache_notifyEvicted pins the drain: every queued pair reaches the
// observer, in order, and an empty queue costs nothing — including when no
// observer is configured at all, where calling one would be a nil dereference.
func Test_Cache_notifyEvicted(t *testing.T) {
	t.Parallel()
	type tc struct {
		name        string
		withObserve bool
		pending     []evictionValue[string, int]
		want        []string
	}
	tests := []tc{
		{"an empty queue with an observer", true, nil, nil},
		{"an empty queue with no observer at all", false, nil, nil},
		{
			"a queue of three drains in order", true,
			[]evictionValue[string, int]{{key: "a"}, {key: "b"}, {key: "c"}},
			[]string{"a", "b", "c"},
		},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		var seen []string
		cfg := Config[string, int]{}
		if c.withObserve {
			cfg.OnEvict = func(key string, _ int) { seen = append(seen, key) }
		}
		cch := NewCache[string, int](cfg)

		cch.notifyEvicted(c.pending)

		if len(seen) != len(c.want) {
			t.Fatalf("observer saw %v, want %v", seen, c.want)
		}
		for i, want := range c.want {
			if seen[i] != want {
				t.Errorf("observer[%d] = %q, want %q", i, seen[i], want)
			}
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_Cache_setTTL pins the shared write path both Set and SetTTL funnel
// through: a zero ttl means "no expiry", a positive one becomes an absolute
// deadline off the injected clock, and an overwrite replaces the deadline
// rather than keeping whichever was set first.
func Test_Cache_setTTL(t *testing.T) {
	t.Parallel()
	base := time.Unix(1_700_000_000, 0)
	type tc struct {
		name string
		//: the ttl of the first write, then of the overwrite (0 = only one write).
		first        time.Duration
		second       time.Duration
		wantExpireAt time.Time
	}
	tests := []tc{
		{"a zero ttl leaves no deadline", 0, 0, time.Time{}},
		{"a positive ttl becomes an absolute deadline", time.Minute, 0, base.Add(time.Minute)},
		{"an overwrite replaces the deadline", time.Minute, 2 * time.Minute, base.Add(2 * time.Minute)},
		{"an overwrite can clear the deadline", time.Minute, -1, time.Time{}},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		cch := NewCache[string, int](Config[string, int]{Clock: &fixedClock{now: base}})

		cch.setTTL("k", 1, c.first)
		//: a negative marker means "write again with no ttl", which is what a
		//: plain Set does on a key that previously carried one.
		switch {
		case c.second < 0:
			cch.setTTL("k", 2, 0)
		case c.second > 0:
			cch.setTTL("k", 2, c.second)
		}

		ent, ok := cch.items["k"]
		if !ok {
			t.Fatal("the key is absent after setTTL")
		}
		if !ent.expireAt.Equal(c.wantExpireAt) {
			t.Errorf("expireAt = %v, want %v", ent.expireAt, c.wantExpireAt)
		}
		//: an overwrite must not grow the cache.
		if len(cch.items) != 1 {
			t.Errorf("items = %d, want 1", len(cch.items))
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}
