package cache_test

import (
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/kitsunium/sdk/internal/kernel/collections/cache"
)

// noDeadlock runs body and fails the test when it has not returned within a
// generous bound: a loop body that calls a cache whose lock Keys still held
// would never return.
func noDeadlock(t *testing.T, body func()) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		defer close(done)
		body()
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("the loop's body is blocked: Keys held the cache's lock while it ran")
	}
}

// TestKeysYieldsThePresentUnexpiredKeys pins #816's Keys: every key present
// and unexpired, once, an expired one left out as Fetch leaves it out.
func TestKeysYieldsThePresentUnexpiredKeys(t *testing.T) {
	t.Parallel()
	forEachPolicy(t, func(t *testing.T, policy cache.Policy) {
		clk := &fakeClock{now: time.Unix(1_700_000_000, 0)}
		c := cache.NewCache(cache.Config[string, int]{Clock: clk, Policy: policy})
		c.Set("a", 1)
		c.SetTTL("b", 2, time.Hour)
		c.SetTTL("gone", 3, time.Minute)
		clk.advance(2 * time.Minute)
		got := slices.Sorted(c.Keys())
		if want := []string{"a", "b"}; !slices.Equal(got, want) {
			t.Fatalf("Keys() = %v, want %v", got, want)
		}
	})
}

// TestKeysBodyMayCallTheCache pins the lock rule the issue names: the body
// never runs under the cache's lock, so it may write to the cache, and what
// it writes is not seen by the loop it runs in.
func TestKeysBodyMayCallTheCache(t *testing.T) {
	t.Parallel()
	forEachPolicy(t, func(t *testing.T, policy cache.Policy) {
		c := cache.NewCache(cache.Config[string, int]{Policy: policy})
		for i := range 8 {
			c.Set("k"+strconv.Itoa(i), i)
		}
		seen := 0
		noDeadlock(t, func() {
			for key := range c.Keys() {
				seen++
				c.Set(key+"-copy", 0)
				c.Delete(key)
				c.Fetch(key)
			}
		})
		if seen != 8 {
			t.Fatalf("the loop saw %d keys, want the 8 present when it started", seen)
		}
		if n := c.Len(); n != 8 {
			t.Fatalf("Len() after the loop = %d, want the 8 copies", n)
		}
	})
}

// TestKeysIsNotARead pins that a walk moves no counter and promotes nothing:
// the LRU victim after a walk is the one before it.
func TestKeysIsNotARead(t *testing.T) {
	t.Parallel()
	c := cache.NewCache(cache.Config[string, int]{MaxEntries: 2})
	c.Set("old", 1)
	c.Set("new", 2)
	before := c.Stats()
	walked := 0
	for range c.Keys() {
		walked++
	}
	if walked != 2 {
		t.Fatalf("the walk saw %d keys, want 2", walked)
	}
	if after := c.Stats(); after != before {
		t.Fatalf("Stats() after a walk = %+v, want %+v", after, before)
	}
	c.Set("third", 3)
	if _, ok := c.Fetch("old"); ok {
		t.Fatal("the walk promoted the LRU tail: a third key evicted another entry")
	}
}

// TestKeysStopsEarly pins the early stop: a loop that breaks gets no more.
func TestKeysStopsEarly(t *testing.T) {
	t.Parallel()
	c := cache.NewCache(cache.Config[string, int]{})
	for i := range 5 {
		c.Set(strconv.Itoa(i), i)
	}
	seen := 0
	for range c.Keys() {
		seen++
		if seen == 2 {
			break
		}
	}
	if seen != 2 {
		t.Fatalf("the loop saw %d keys after breaking at 2", seen)
	}
}

// TestStripedKeysYieldsEveryStripe pins Striped's Keys: every key of every
// stripe once, the body free to write, an early stop honoured.
func TestStripedKeysYieldsEveryStripe(t *testing.T) {
	t.Parallel()
	s := cache.NewStriped(cache.Config[string, int]{}, 4, nil)
	want := make([]string, 0, 64)
	for i := range 64 {
		key := "k" + strconv.Itoa(i)
		s.Set(key, i)
		want = append(want, key)
	}
	var got []string
	noDeadlock(t, func() {
		for key := range s.Keys() {
			got = append(got, key)
			s.Set(key, -1)
		}
	})
	slices.Sort(got)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Fatalf("Keys() = %d keys %v, want the %d set", len(got), got, len(want))
	}
	seen := 0
	for range s.Keys() {
		seen++
		if seen == 3 {
			break
		}
	}
	if seen != 3 {
		t.Fatalf("the striped loop saw %d keys after breaking at 3", seen)
	}
}
