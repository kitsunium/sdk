package cache_test

import (
	"runtime"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kitsunium/sdk/internal/kernel/clock"
	"github.com/kitsunium/sdk/internal/kernel/collections/cache"
)

// countingClock counts its reads, so a test can pin how many a sweep makes.
type countingClock struct {
	now   time.Time
	reads atomic.Int64
}

func (c *countingClock) Now() time.Time                  { c.reads.Add(1); return c.now }
func (c *countingClock) Since(t time.Time) time.Duration { return c.Now().Sub(t) }

// TestExpireReapsAnIdleEntryPastItsDeadline is #1038's acceptance: an entry
// whose TTL passed and that nothing fetched is reaped by Expire — counted,
// gone from Len, and handed to OnEvict exactly once. Before Expire existed it
// stayed in memory until a Fetch of its own key or a capacity eviction.
func TestExpireReapsAnIdleEntryPastItsDeadline(t *testing.T) {
	t.Parallel()
	forEachPolicy(t, func(t *testing.T, policy cache.Policy) {
		clk := clock.NewManualClock(time.Unix(0, 0))
		var evicted []string
		c := cache.NewCache[string, int](cache.Config[string, int]{
			Clock:   clk,
			Policy:  policy,
			OnEvict: func(key string, _ int) { evicted = append(evicted, key) },
		})
		c.SetTTL("idle", 1, time.Minute)
		clk.Advance(2 * time.Minute)
		if got := c.Expire(); got != 1 {
			t.Fatalf("Expire() = %d, want 1", got)
		}
		if got := c.Len(); got != 0 {
			t.Errorf("Len() = %d after the sweep, want 0", got)
		}
		if len(evicted) != 1 || evicted[0] != "idle" {
			t.Errorf("OnEvict saw %v, want [idle] once", evicted)
		}
		if got := c.Stats().Evictions; got != 1 {
			t.Errorf("Evictions = %d, want 1", got)
		}
	})
}

// TestExpireTakesOnlyWhatIsDue mixes entries with no deadline, a future one
// and two past ones: the sweep takes exactly the two.
func TestExpireTakesOnlyWhatIsDue(t *testing.T) {
	t.Parallel()
	forEachPolicy(t, func(t *testing.T, policy cache.Policy) {
		clk := clock.NewManualClock(time.Unix(0, 0))
		c := cache.NewCache[string, int](cache.Config[string, int]{Clock: clk, Policy: policy})
		c.Set("forever", 1)
		c.SetTTL("later", 2, time.Hour)
		c.SetTTL("soon", 3, time.Second)
		c.SetTTL("sooner", 4, time.Millisecond)
		clk.Advance(time.Minute)
		if got := c.Expire(); got != 2 {
			t.Fatalf("Expire() = %d, want 2", got)
		}
		for key, want := range map[string]bool{"forever": true, "later": true, "soon": false, "sooner": false} {
			if _, ok := c.Fetch(key); ok != want {
				t.Errorf("Fetch(%q) hit = %v, want %v", key, ok, want)
			}
		}
		if got := c.Expire(); got != 0 {
			t.Errorf("a second sweep reaped %d, want 0", got)
		}
	})
}

// TestARestampedOrDeletedEntryKeepsNoStaleDeadline pins that the expiry index
// follows the entry: a re-Set moves its deadline, a Set without a TTL drops it,
// and a Delete takes it out — none leaves a deadline behind to fire later.
func TestARestampedOrDeletedEntryKeepsNoStaleDeadline(t *testing.T) {
	t.Parallel()
	forEachPolicy(t, func(t *testing.T, policy cache.Policy) {
		clk := clock.NewManualClock(time.Unix(0, 0))
		var evicted []string
		c := cache.NewCache[string, int](cache.Config[string, int]{
			Clock:   clk,
			Policy:  policy,
			OnEvict: func(key string, _ int) { evicted = append(evicted, key) },
		})
		c.SetTTL("moved", 1, time.Minute)
		c.SetTTL("moved", 2, time.Hour)
		c.SetTTL("cleared", 1, time.Minute)
		c.SetTTL("cleared", 2, 0)
		c.SetTTL("deleted", 1, time.Minute)
		c.Delete("deleted")
		clk.Advance(2 * time.Minute)
		if got := c.Expire(); got != 0 {
			t.Fatalf("Expire() = %d, want 0: every deadline moved, was cleared or left with its entry", got)
		}
		if len(evicted) != 0 {
			t.Errorf("OnEvict saw %v, want nothing", evicted)
		}
		if got := c.Len(); got != 2 {
			t.Errorf("Len() = %d, want 2", got)
		}
	})
}

// TestExpireWithoutADeadlineReadsNoClock pins the cost of a sweep that has
// nothing to do: on an empty cache and on one whose entries carry no TTL it
// reads the clock at most once (in fact never).
func TestExpireWithoutADeadlineReadsNoClock(t *testing.T) {
	t.Parallel()
	forEachPolicy(t, func(t *testing.T, policy cache.Policy) {
		clk := &countingClock{now: time.Unix(0, 0)}
		c := cache.NewCache[string, int](cache.Config[string, int]{Clock: clk, Policy: policy})
		c.Expire()
		c.Set("forever", 1)
		c.Expire()
		if got := clk.reads.Load(); got > 1 {
			t.Errorf("two sweeps with no deadline read the clock %d times, want at most 1", got)
		}
	})
}

// TestAFullCacheEvictsADeadEntryBeforeTheLiveTail is #1059's refinement: the
// most recently set entry has expired, so it — not the live least-recently-used
// entry — makes room for the next insert.
func TestAFullCacheEvictsADeadEntryBeforeTheLiveTail(t *testing.T) {
	t.Parallel()
	forEachPolicy(t, func(t *testing.T, policy cache.Policy) {
		clk := clock.NewManualClock(time.Unix(0, 0))
		c := cache.NewCache[string, int](cache.Config[string, int]{MaxEntries: 3, Clock: clk, Policy: policy})
		c.Set("oldest", 1)
		c.Set("older", 2)
		c.SetTTL("dead", 3, time.Second)
		clk.Advance(time.Minute)
		c.Set("new", 4)
		for key, want := range map[string]bool{"oldest": true, "older": true, "dead": false, "new": true} {
			if _, ok := c.Fetch(key); ok != want {
				t.Errorf("Fetch(%q) hit = %v, want %v", key, ok, want)
			}
		}
	})
}

// TestACoarseClockExpiresAnEntryWithinOneResolution pins #814's bound on the
// cache side: with a CoarseClock as the cache's clock, an entry is a miss no
// later than one resolution after its deadline.
func TestACoarseClockExpiresAnEntryWithinOneResolution(t *testing.T) {
	t.Parallel()
	const resolution = 10 * time.Millisecond
	start := time.Unix(1_700_000_000, 0)
	base := clock.NewManualClock(start)
	coarse := clock.NewCoarse(base, resolution)
	defer coarse.Stop()
	c := cache.NewCache[string, int](cache.Config[string, int]{Clock: coarse})
	c.SetTTL("k", 1, 25*time.Millisecond)
	deadline := start.Add(25 * time.Millisecond)
	for range 5 {
		base.BlockUntil(1)
		base.Advance(resolution)
		for coarse.Now().Before(base.Now()) {
			runtime.Gosched()
		}
		if _, ok := c.Fetch("k"); ok && base.Since(deadline) >= resolution {
			t.Fatalf("a hit %v past the deadline, over one resolution", base.Since(deadline))
		}
	}
	if _, ok := c.Fetch("k"); ok {
		t.Fatal("the entry outlived its deadline by 25ms with a 10ms resolution")
	}
}
