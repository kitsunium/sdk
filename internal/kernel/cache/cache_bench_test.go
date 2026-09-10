package cache

import (
	"strconv"
	"testing"
	"time"
)

// : keys are built once, outside every timed loop. Building them inside would
// : measure strconv.Itoa — which allocates — and bury the map+LRU cost this
// : file exists to expose.
func benchKeys(n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = "key-" + strconv.Itoa(i)
	}
	return out
}

// BenchmarkNewCache measures construction. NewCache allocates the Cache struct
// and its map; this is the floor for a caller that builds caches per request
// (which it should not) and the regression guard if the constructor grows a
// hidden allocation.
func BenchmarkNewCache(b *testing.B) {
	b.ReportAllocs()
	for b.Loop() {
		c := NewCache[string, int](Config[string, int]{MaxEntries: 1024})
		_ = c
	}
}

// BenchmarkFetch_Hit is the number that decides whether this primitive belongs
// on a hot path. A hit is NOT read-only: it moves the entry to the LRU front
// under the write lock, which is exactly why ADR 0025 named the method Fetch
// rather than Get. This measures that full cost, not a map lookup.
func BenchmarkFetch_Hit(b *testing.B) {
	keys := benchKeys(1024)
	c := NewCache[string, int](Config[string, int]{MaxEntries: 2048})
	for i, k := range keys {
		c.Set(k, i)
	}
	b.ReportAllocs()
	b.ResetTimer()
	i := 0
	for b.Loop() {
		//: cycle through the whole key set so the LRU list is genuinely
		//: reordered; hammering one key would keep it at the head and skip
		//: the relink this benchmark is here to price.
		v, ok := c.Fetch(keys[i&1023])
		if !ok {
			b.Fatalf("miss on seeded key %q", keys[i&1023])
		}
		_ = v
		i++
	}
}

// BenchmarkFetch_HitWithTTL is the contrast that matters: an entry carrying an
// expiry forces a clock read on every hit, while an entry without one does not
// touch the clock at all. The delta against Fetch_Hit is the price of TTL.
func BenchmarkFetch_HitWithTTL(b *testing.B) {
	keys := benchKeys(1024)
	c := NewCache[string, int](Config[string, int]{MaxEntries: 2048, DefaultTTL: time.Hour})
	for i, k := range keys {
		c.Set(k, i)
	}
	b.ReportAllocs()
	b.ResetTimer()
	i := 0
	for b.Loop() {
		v, ok := c.Fetch(keys[i&1023])
		if !ok {
			b.Fatalf("miss on seeded key %q", keys[i&1023])
		}
		_ = v
		i++
	}
}

// BenchmarkFetch_Miss prices the negative answer. A miss still takes the write
// lock, because the caller cannot know it was a miss until the lookup is done.
func BenchmarkFetch_Miss(b *testing.B) {
	c := NewCache[string, int](Config[string, int]{MaxEntries: 1024})
	b.ReportAllocs()
	for b.Loop() {
		if _, ok := c.Fetch("absent"); ok {
			b.Fatal("hit on an empty cache")
		}
	}
}

// BenchmarkSet_NewKey measures insertion below capacity: one map write, one
// entry allocation, one list push. The allocation is the entry node and is
// structural — the intrusive LRU list needs a node per live key.
func BenchmarkSet_NewKey(b *testing.B) {
	keys := benchKeys(4096)
	b.ReportAllocs()
	b.ResetTimer()
	i := 0
	for b.Loop() {
		//: a fresh cache every 4096 inserts keeps this on the below-capacity
		//: path; letting it evict would silently measure Set_AtCapacity.
		if i&4095 == 0 {
			b.StopTimer()
			c := NewCache[string, int](Config[string, int]{})
			b.StartTimer()
			benchCache = c
		}
		benchCache.Set(keys[i&4095], i)
		i++
	}
}

// : package-level so the compiler cannot prove the cache dead and elide the
// : Set calls above.
var benchCache *Cache[string, int]

// BenchmarkSet_Overwrite measures replacing a live key. No entry is allocated
// and no eviction runs — the delta against Set_NewKey is the node allocation.
func BenchmarkSet_Overwrite(b *testing.B) {
	c := NewCache[string, int](Config[string, int]{MaxEntries: 1024})
	c.Set("k", 0)
	b.ReportAllocs()
	i := 0
	for b.Loop() {
		c.Set("k", i)
		i++
	}
}

// BenchmarkSet_AtCapacity is the one that tells a caller what a bounded cache
// really costs: every insert above MaxEntries also unlinks the tail, deletes
// its map key, and (here) does not notify, since OnEvict is nil.
func BenchmarkSet_AtCapacity(b *testing.B) {
	keys := benchKeys(4096)
	c := NewCache[string, int](Config[string, int]{MaxEntries: 512})
	for i, k := range keys[:512] {
		c.Set(k, i)
	}
	b.ReportAllocs()
	b.ResetTimer()
	i := 0
	for b.Loop() {
		c.Set(keys[i&4095], i)
		i++
	}
}

// BenchmarkSet_AtCapacityOnEvict adds the callback. It runs on the caller's
// goroutine OUTSIDE the lock, so its cost lands on the setter — which is the
// property a caller must know before putting real work in OnEvict.
func BenchmarkSet_AtCapacityOnEvict(b *testing.B) {
	keys := benchKeys(4096)
	var evicted int
	c := NewCache[string, int](Config[string, int]{
		MaxEntries: 512,
		OnEvict:    func(string, int) { evicted++ },
	})
	for i, k := range keys[:512] {
		c.Set(k, i)
	}
	b.ReportAllocs()
	b.ResetTimer()
	i := 0
	for b.Loop() {
		c.Set(keys[i&4095], i)
		i++
	}
	_ = evicted
}

// BenchmarkFetch_Parallel is the contention number. One mutex covers the map
// AND the LRU list, and a hit takes it exclusively, so readers serialise —
// this is the measurement that says so instead of leaving it to be discovered.
func BenchmarkFetch_Parallel(b *testing.B) {
	keys := benchKeys(1024)
	c := NewCache[string, int](Config[string, int]{MaxEntries: 2048})
	for i, k := range keys {
		c.Set(k, i)
	}
	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		i := 0
		for pb.Next() {
			if _, ok := c.Fetch(keys[i&1023]); !ok {
				b.Errorf("miss on seeded key")
				return
			}
			i++
		}
	})
}

// BenchmarkLen and BenchmarkStats price the two read-only accessors. They take
// a read lock and copy a small struct, so they are the cheap half of the API.
func BenchmarkLen(b *testing.B) {
	c := NewCache[string, int](Config[string, int]{MaxEntries: 1024})
	for i, k := range benchKeys(512) {
		c.Set(k, i)
	}
	b.ReportAllocs()
	for b.Loop() {
		_ = c.Len()
	}
}

func BenchmarkStats(b *testing.B) {
	c := NewCache[string, int](Config[string, int]{MaxEntries: 1024})
	for i, k := range benchKeys(512) {
		c.Set(k, i)
	}
	b.ReportAllocs()
	for b.Loop() {
		_ = c.Stats()
	}
}
