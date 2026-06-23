package cache_test

import (
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
	c := cache.NewCache[string, int](cache.Config[string, int]{MaxEntries: 2})
	c.Set("a", 1)
	c.Set("b", 2)
	//: touching "a" makes "b" the LRU victim.
	c.Fetch("a")
	c.Set("c", 3)
	//: "b" was least-recently-used and must be gone.
	if _, ok := c.Fetch("b"); ok {
		t.Error("b should have been evicted as LRU")
	}
	//: "a" and "c" must survive.
	if _, ok := c.Fetch("a"); !ok {
		t.Error("a should survive")
	}
	if _, ok := c.Fetch("c"); !ok {
		t.Error("c should survive")
	}
}

// TestTTLExpiry expires an entry once the fake clock passes its deadline.
func TestTTLExpiry(t *testing.T) {
	t.Parallel()
	clk := &fakeClock{now: time.Unix(0, 0)}
	c := cache.NewCache[string, int](cache.Config[string, int]{Clock: clk})
	c.SetTTL("k", 7, time.Minute)
	//: before the deadline the entry is live.
	if _, ok := c.Fetch("k"); !ok {
		t.Fatal("k should be live before TTL")
	}
	//: advance past the deadline.
	clk.advance(2 * time.Minute)
	//: after the deadline the entry is a miss.
	if _, ok := c.Fetch("k"); ok {
		t.Error("k should have expired")
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
	c := cache.NewCache[string, int](cache.Config[string, int]{MaxEntries: 4})
	c.Set("k", 1)
	c.Set("k", 2)
	//: an overwrite does not grow the cache.
	if c.Len() != 1 {
		t.Errorf("Len=%d after overwrite, want 1", c.Len())
	}
	//: the value reflects the latest Set.
	if v, _ := c.Fetch("k"); v != 2 {
		t.Errorf("Fetch=%d, want 2", v)
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
