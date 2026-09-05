package cache_test

import (
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/kitsunium/sdk/internal/kernel/cache"
)

// fakeClock is a settable clock for deterministic TTL tests.
type fakeClock struct{ now time.Time }

func (f *fakeClock) Now() time.Time                  { return f.now }
func (f *fakeClock) Since(t time.Time) time.Duration { return f.now.Sub(t) }
func (f *fakeClock) advance(d time.Duration)         { f.now = f.now.Add(d) }

// TestLRUEviction evicts the least-recently-used entry past the capacity.
func TestLRUEviction(t *testing.T) {
	t.Parallel()
	type tc struct {
		name    string
		key     string
		wantHit bool
	}
	tests := []tc{
		{"the untouched entry is the victim", "b", false},
		{"the entry a Fetch refreshed survives", "a", true},
		{"the newest entry survives", "c", true},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		cch := cache.NewCache[string, int](cache.Config[string, int]{MaxEntries: 2})
		cch.Set("a", 1)
		cch.Set("b", 2)
		//: touching "a" makes "b" the LRU victim.
		cch.Fetch("a")
		cch.Set("c", 3)
		if _, ok := cch.Fetch(c.key); ok != c.wantHit {
			t.Errorf("Fetch(%q) hit = %v, want %v", c.key, ok, c.wantHit)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// TestTTLExpiry expires an entry once the fake clock passes its deadline.
func TestTTLExpiry(t *testing.T) {
	t.Parallel()
	type tc struct {
		name    string
		advance time.Duration
		wantHit bool
	}
	tests := []tc{
		{"before the deadline", 0, true},
		{"just short of the deadline", 59 * time.Second, true},
		{"past the deadline", 2 * time.Minute, false},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		clk := &fakeClock{now: time.Unix(0, 0)}
		cch := cache.NewCache[string, int](cache.Config[string, int]{Clock: clk})
		cch.SetTTL("k", 7, time.Minute)
		clk.advance(c.advance)
		if _, ok := cch.Fetch("k"); ok != c.wantHit {
			t.Errorf("Fetch hit = %v after %v, want %v", ok, c.advance, c.wantHit)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// TestOnEvictAndStats checks the callback + counters semantics.
func TestOnEvictAndStats(t *testing.T) {
	t.Parallel()
	var evicted []string
	var mu sync.Mutex
	c := cache.NewCache[string, int](cache.Config[string, int]{
		MaxEntries: 1,
		OnEvict: func(key string, _ int) {
			mu.Lock()
			evicted = append(evicted, key)
			mu.Unlock()
		},
	})
	c.Set("a", 1)
	c.Set("b", 2) // evicts "a"
	c.Fetch("b")  // hit
	c.Fetch("z")  // miss
	c.Delete("b") // explicit delete: NO OnEvict
	st := c.Stats()
	//: one hit, one miss, one capacity eviction recorded.
	if st.Hits != 1 || st.Misses != 1 || st.Evictions != 1 {
		t.Errorf("stats = %+v, want hits=1 misses=1 evictions=1", st)
	}
	//: OnEvict fired only for the capacity eviction of "a".
	mu.Lock()
	defer mu.Unlock()
	if len(evicted) != 1 || evicted[0] != "a" {
		t.Errorf("OnEvict fired for %v, want [a]", evicted)
	}
}

// TestUpdateExisting keeps the length stable and updates the value.
func TestUpdateExisting(t *testing.T) {
	t.Parallel()
	type tc struct {
		name      string
		wantLen   int
		wantValue int
	}
	tests := []tc{
		{"an overwrite does not grow the cache", 1, 0},
		{"the value reflects the latest Set", 0, 2},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		cch := cache.NewCache[string, int](cache.Config[string, int]{MaxEntries: 4})
		cch.Set("k", 1)
		cch.Set("k", 2)
		if c.wantLen != 0 && cch.Len() != c.wantLen {
			t.Errorf("Len = %d, want %d", cch.Len(), c.wantLen)
		}
		if c.wantValue != 0 {
			if v, _ := cch.Fetch("k"); v != c.wantValue {
				t.Errorf("Fetch = %d, want %d", v, c.wantValue)
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

// TestPurge drops everything.
func TestPurge(t *testing.T) {
	t.Parallel()
	c := cache.NewCache[int, int](cache.Config[int, int]{})
	for i := range 10 {
		c.Set(i, i)
	}
	c.Purge()
	//: a purged cache is empty.
	if c.Len() != 0 {
		t.Errorf("Len=%d after Purge, want 0", c.Len())
	}
}

// TestConcurrent exercises the mutex under -race.
func TestConcurrent(t *testing.T) {
	t.Parallel()
	c := cache.NewCache[int, int](cache.Config[int, int]{MaxEntries: 64})
	var wg sync.WaitGroup
	//: many goroutines hammer Set/Fetch concurrently.
	for g := range 8 {
		wg.Go(func() {
			for i := range 1000 {
				c.Set(g*1000+i, i)
				c.Fetch(g*1000 + i)
			}
		})
	}
	wg.Wait()
	//: the cache stayed within its capacity bound.
	if c.Len() > 64 {
		t.Errorf("Len=%d exceeds MaxEntries=64", c.Len())
	}
}

// TestNegativeMaxEntriesClamps pins ADR 0025's "bad options clamp to sane
// defaults" contract for the capacity field.
//
// This is a contract pin, NOT a crash regression: make(map, n) silently clamps
// a negative hint to 0 (only make([]T, n) panics), so the pre-clamp code
// already behaved correctly — by accident, via an undocumented runtime detail.
// The test fixes the observable behaviour (unbounded, Purge-safe) so a future
// refactor toward a slice-backed or hint-validating store cannot regress it
// silently.
func TestNegativeMaxEntriesClamps(t *testing.T) {
	t.Parallel()
	type tc struct {
		name       string
		maxEntries int
	}
	tests := []tc{
		{"negative-one", -1},
		{"large-negative", -4096},
		{"zero-means-unbounded", 0},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		//: construction must not panic on a bad capacity.
		c := cache.NewCache[string, int](cache.Config[string, int]{MaxEntries: tc.maxEntries})
		//: the cache stays usable and unbounded (nothing is evicted).
		for i := range 100 {
			c.Set("k"+strconv.Itoa(i), i)
		}
		if got := c.Len(); got != 100 {
			t.Errorf("%s: Len=%d after 100 Sets, want 100 (clamped to unbounded)", tc.name, got)
		}
		//: Purge re-pre-sizes the map from the same field — it must not panic.
		c.Purge()
		if got := c.Len(); got != 0 {
			t.Errorf("%s: Len=%d after Purge, want 0", tc.name, got)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) { t.Parallel(); runCase(t, tc) })
	}
}

// TestOnEvictRunsOutsideLock pins the eviction-callback contract: OnEvict must
// run with the cache mutex released. It previously fired from removeEvict with
// c.mu held, so a callback touching the cache deadlocked on the non-reentrant
// mutex. Re-entering from the callback is the direct probe — under the old code
// this test hangs until the package timeout.
func TestOnEvictRunsOutsideLock(t *testing.T) {
	t.Parallel()
	var reentered bool
	var c *cache.Cache[string, int]
	c = cache.NewCache[string, int](cache.Config[string, int]{
		MaxEntries: 1,
		OnEvict: func(key string, val int) {
			//: re-enter the cache from inside the callback. With OnEvict fired
			//: under c.mu this call blocks forever on the same mutex.
			c.Len()
			_, _ = c.Fetch(key)
			reentered = true
		},
	})
	//: two inserts at capacity 1 force exactly one eviction.
	c.Set("a", 1)
	c.Set("b", 2)
	//: the callback ran to completion, so the lock was not held.
	if !reentered {
		t.Fatal("OnEvict did not complete — the callback could not re-enter the cache")
	}
	//: the eviction is still accounted for.
	if got := c.Stats().Evictions; got != 1 {
		t.Errorf("Evictions=%d, want 1", got)
	}
}

// TestOnEvictFiresOnExpiry covers the second removeEvict call site: a lazily
// expired entry found by Fetch. The callback must fire, and must likewise run
// outside the lock.
func TestOnEvictFiresOnExpiry(t *testing.T) {
	t.Parallel()
	var gotKey string
	var c *cache.Cache[string, int]
	clk := &fakeClock{now: time.Unix(0, 0)}
	c = cache.NewCache[string, int](cache.Config[string, int]{
		Clock: clk,
		OnEvict: func(key string, val int) {
			//: re-entering here proves the expiry path also released the lock.
			c.Len()
			gotKey = key
		},
	})
	c.SetTTL("gone", 7, time.Second)
	//: push the clock past the deadline so the next Fetch reaps the entry.
	clk.advance(2 * time.Second)
	if _, ok := c.Fetch("gone"); ok {
		t.Fatal("Fetch returned a hit on an expired entry")
	}
	//: the expiry eviction notified the observer with the right key.
	if gotKey != "gone" {
		t.Errorf("OnEvict key=%q, want \"gone\"", gotKey)
	}
}
