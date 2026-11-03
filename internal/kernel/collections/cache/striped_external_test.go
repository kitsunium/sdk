package cache_test

import (
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/kitsunium/sdk/internal/kernel/clock"
	"github.com/kitsunium/sdk/internal/kernel/collections/cache"
)

// TestAStripedOfOneIsACache replays the Cache contract on one stripe: the
// LRU victim, TTL expiry, Delete without OnEvict, Purge (#808's "stripes=1
// passes the Cache conformance unchanged").
func TestAStripedOfOneIsACache(t *testing.T) {
	t.Parallel()
	forEachPolicy(t, func(t *testing.T, policy cache.Policy) {
		clk := clock.NewManualClock(time.Unix(0, 0))
		var evicted []string
		s := cache.NewStriped(cache.Config[string, int]{
			MaxEntries: 2,
			Clock:      clk,
			Policy:     policy,
			OnEvict:    func(key string, _ int) { evicted = append(evicted, key) },
		}, 0, nil)
		if got := s.Stripes(); got != 1 {
			t.Fatalf("Stripes() = %d for a count below 1, want 1", got)
		}
		s.Set("a", 1)
		s.Set("b", 2)
		s.Fetch("a")
		s.Set("c", 3)
		if _, ok := s.Fetch("b"); ok {
			t.Error("the untouched entry survived")
		}
		if _, ok := s.Fetch("a"); !ok {
			t.Error("the refreshed entry was evicted")
		}
		s.SetTTL("a", 1, time.Second)
		clk.Advance(time.Minute)
		if got := s.Expire(); got != 1 {
			t.Errorf("Expire() = %d, want 1", got)
		}
		s.Delete("c")
		if got := s.Len(); got != 0 {
			t.Errorf("Len() = %d, want 0", got)
		}
		if len(evicted) != 2 {
			t.Errorf("OnEvict saw %v, want the capacity victim and the expired entry", evicted)
		}
		if stats := s.Stats(); stats.Evictions != 2 || stats.Policy != policy {
			t.Errorf("Stats() = %+v", stats)
		}
	})
}

// TestStripedEvictionsMatchOneCacheWithinTheStripeCount is #808's bound: for
// one insert sequence, the evictions summed over the stripes equal one
// cache's within the number of stripes.
func TestStripedEvictionsMatchOneCacheWithinTheStripeCount(t *testing.T) {
	t.Parallel()
	const stripes = 16
	single := cache.NewCache[int, int](cache.Config[int, int]{MaxEntries: 1000})
	striped := cache.NewStriped(cache.Config[int, int]{MaxEntries: 1000}, stripes, func(key int) uint64 { return uint64(key) * 0x9E3779B97F4A7C15 })
	for i := range 10_000 {
		single.Set(i, i)
		striped.Set(i, i)
	}
	got, want := striped.Stats().Evictions, single.Stats().Evictions
	if diff := int64(got) - int64(want); diff < -stripes || diff > stripes {
		t.Errorf("striped evictions %d, single %d: more than %d apart", got, want, stripes)
	}
	if striped.Len() > 1000+stripes {
		t.Errorf("Len() = %d, over the bound", striped.Len())
	}
}

// TestStripedUnderConcurrency hammers every stripe under -race, and checks the
// shares never let the total exceed the bound.
func TestStripedUnderConcurrency(t *testing.T) {
	t.Parallel()
	s := cache.NewStriped(cache.Config[string, int]{MaxEntries: 64}, 8, nil)
	var wg sync.WaitGroup
	for g := range 8 {
		wg.Go(func() {
			for i := range 1000 {
				key := strconv.Itoa(g*1000 + i)
				s.Set(key, i)
				s.Fetch(key)
			}
		})
	}
	wg.Wait()
	if got := s.Len(); got > 64 {
		t.Errorf("Len() = %d, over MaxEntries 64", got)
	}
	s.Purge()
	if got := s.Len(); got != 0 {
		t.Errorf("Len() = %d after Purge", got)
	}
}
