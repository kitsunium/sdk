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

// TestCache_Fetch pins the read path: a hit promotes the entry to
// most-recently-used, which is what makes the LRU victim predictable.
func TestCache_Fetch(t *testing.T) {
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

// TestCache_SetTTL expires an entry once the clock passes the per-entry
// deadline, and not one tick before it.
func TestCache_SetTTL(t *testing.T) {
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

// TestCache_Stats checks the callback + counters semantics: an explicit
// Delete must NOT notify, because the caller already knows it removed the
// entry, while a capacity eviction must, because nobody else can see it happen.
func TestCache_Stats(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		//: what the caller does, in order, after the cache is built.
		steps func(c *cache.Cache[string, int])
		//: the keys OnEvict is expected to have been handed, in order.
		wantEvicted []string
		wantStats   cache.StatsValue
	}
	tests := []tc{
		{
			name: "a capacity eviction notifies; an explicit Delete does not",
			steps: func(c *cache.Cache[string, int]) {
				c.Set("a", 1)
				c.Set("b", 2) //: evicts "a"
				c.Fetch("b")  //: hit
				c.Fetch("z")  //: miss
				c.Delete("b") //: explicit delete: NO OnEvict
			},
			wantEvicted: []string{"a"},
			wantStats:   cache.StatsValue{Hits: 1, Misses: 1, Evictions: 1},
		},
		{
			name: "an overwrite of the live key never evicts",
			steps: func(c *cache.Cache[string, int]) {
				c.Set("a", 1)
				c.Set("a", 2)
				c.Fetch("a")
			},
			wantEvicted: nil,
			wantStats:   cache.StatsValue{Hits: 1, Misses: 0, Evictions: 0},
		},
		{
			name: "each insert past capacity evicts exactly one",
			steps: func(c *cache.Cache[string, int]) {
				c.Set("a", 1)
				c.Set("b", 2)
				c.Set("c", 3)
			},
			wantEvicted: []string{"a", "b"},
			wantStats:   cache.StatsValue{Hits: 0, Misses: 0, Evictions: 2},
		},
		{
			name: "a miss on an empty cache only counts",
			steps: func(c *cache.Cache[string, int]) {
				c.Fetch("absent")
			},
			wantEvicted: nil,
			wantStats:   cache.StatsValue{Hits: 0, Misses: 1, Evictions: 0},
		},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		var evicted []string
		var mu sync.Mutex
		cch := cache.NewCache[string, int](cache.Config[string, int]{
			MaxEntries: 1,
			OnEvict: func(key string, _ int) {
				mu.Lock()
				evicted = append(evicted, key)
				mu.Unlock()
			},
		})
		c.steps(cch)

		if st := cch.Stats(); st != c.wantStats {
			t.Errorf("stats = %+v, want %+v", st, c.wantStats)
		}
		mu.Lock()
		defer mu.Unlock()
		if len(evicted) != len(c.wantEvicted) {
			t.Fatalf("OnEvict fired for %v, want %v", evicted, c.wantEvicted)
		}
		for i, want := range c.wantEvicted {
			if evicted[i] != want {
				t.Errorf("OnEvict[%d] = %q, want %q", i, evicted[i], want)
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

// TestCache_Set keeps the length stable on an overwrite and stores the latest
// value: a Set that grew the cache would make MaxEntries meaningless.
func TestCache_Set(t *testing.T) {
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

// TestCache_Purge drops everything and leaves the cache usable.
func TestCache_Purge(t *testing.T) {
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

// TestCache_Len exercises the mutex under -race and pins that the reported
// length never exceeds the configured capacity, however hard it is hammered.
func TestCache_Len(t *testing.T) {
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

// TestNewCache pins ADR 0025's "bad options clamp to sane
// defaults" contract for the capacity field.
//
// This is a contract pin, NOT a crash regression: make(map, n) silently clamps
// a negative hint to 0 (only make([]T, n) panics), so the pre-clamp code
// already behaved correctly — by accident, via an undocumented runtime detail.
// The test fixes the observable behaviour (unbounded, Purge-safe) so a future
// refactor toward a slice-backed or hint-validating store cannot regress it
// silently.
func TestNewCache(t *testing.T) {
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
// these cases hang until the package timeout.
//
// The expiry case is the second removeEvict call site: an entry reaped lazily
// by Fetch rather than by capacity pressure.
func TestOnEvictRunsOutsideLock(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		//: builds the cache with the re-entrant observer, then drives it to
		//: exactly one eviction; returns the key the observer should have seen.
		drive func(t *testing.T, observe func(key string)) string
		want  string
	}
	tests := []tc{
		{
			name: "an eviction under capacity pressure",
			drive: func(t *testing.T, observe func(string)) string {
				t.Helper()
				var c *cache.Cache[string, int]
				c = cache.NewCache[string, int](cache.Config[string, int]{
					MaxEntries: 1,
					OnEvict: func(key string, _ int) {
						//: re-enter the cache from inside the callback. With
						//: OnEvict fired under c.mu this blocks forever.
						c.Len()
						_, _ = c.Fetch(key)
						observe(key)
					},
				})
				//: two inserts at capacity 1 force exactly one eviction.
				c.Set("a", 1)
				c.Set("b", 2)
				if got := c.Stats().Evictions; got != 1 {
					t.Errorf("Evictions = %d, want 1", got)
				}
				return "a"
			},
			want: "a",
		},
		{
			name: "an entry reaped lazily on expiry",
			drive: func(t *testing.T, observe func(string)) string {
				t.Helper()
				clk := &fakeClock{now: time.Unix(0, 0)}
				var c *cache.Cache[string, int]
				c = cache.NewCache[string, int](cache.Config[string, int]{
					Clock: clk,
					OnEvict: func(key string, _ int) {
						//: re-entering here proves the expiry path also
						//: released the lock.
						c.Len()
						observe(key)
					},
				})
				c.SetTTL("gone", 7, time.Second)
				//: push the clock past the deadline so Fetch reaps the entry.
				clk.advance(2 * time.Second)
				if _, ok := c.Fetch("gone"); ok {
					t.Error("Fetch returned a hit on an expired entry")
				}
				return "gone"
			},
			want: "gone",
		},
		{
			name: "an entry reaped on expiry with a purge behind it",
			drive: func(t *testing.T, observe func(string)) string {
				t.Helper()
				clk := &fakeClock{now: time.Unix(0, 0)}
				var c *cache.Cache[string, int]
				c = cache.NewCache[string, int](cache.Config[string, int]{
					Clock: clk,
					OnEvict: func(key string, _ int) {
						//: Purge takes the same lock; a held mutex deadlocks.
						c.Purge()
						observe(key)
					},
				})
				c.SetTTL("stale", 1, time.Second)
				clk.advance(2 * time.Second)
				if _, ok := c.Fetch("stale"); ok {
					t.Error("Fetch returned a hit on an expired entry")
				}
				return "stale"
			},
			want: "stale",
		},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		var got string
		observed := false
		key := c.drive(t, func(k string) {
			got = k
			observed = true
		})
		//: the callback ran to completion, so the lock was not held.
		if !observed {
			t.Fatalf("OnEvict did not complete for %q — the callback could not re-enter the cache", key)
		}
		if got != c.want {
			t.Errorf("OnEvict key = %q, want %q", got, c.want)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// TestCache_Delete removes the entry without notifying the eviction observer.
// A Delete is the caller's own decision, so telling them about it would make
// OnEvict useless as a "something left the cache behind your back" signal.
func TestCache_Delete(t *testing.T) {
	t.Parallel()
	type tc struct {
		name    string
		seed    []string
		remove  string
		wantLen int
	}
	tests := []tc{
		{"the only entry", []string{"a"}, "a", 0},
		{"one of several", []string{"a", "b", "c"}, "b", 2},
		{"a key that was never there", []string{"a"}, "absent", 1},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		var notified []string
		cch := cache.NewCache[string, int](cache.Config[string, int]{
			OnEvict: func(key string, _ int) { notified = append(notified, key) },
		})
		for i, k := range c.seed {
			cch.Set(k, i)
		}

		cch.Delete(c.remove)

		if got := cch.Len(); got != c.wantLen {
			t.Errorf("Len = %d after deleting %q, want %d", got, c.remove, c.wantLen)
		}
		if _, ok := cch.Fetch(c.remove); ok {
			t.Errorf("Fetch(%q) still hit after Delete", c.remove)
		}
		//: a Delete is not an eviction.
		if len(notified) != 0 {
			t.Errorf("Delete notified OnEvict for %v", notified)
		}
		if got := cch.Stats().Evictions; got != 0 {
			t.Errorf("Evictions = %d after a Delete, want 0", got)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}
