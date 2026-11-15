package cache_test

import (
	"strconv"
	"testing"

	"github.com/kitsunium/sdk/internal/kernel/collections/cache"
)

// TestS3FIFOKeepsTheHotSetThroughAScan is the reason the policy exists: a hot
// set hit repeatedly, then a sweep of cold keys twice the capacity. LRU loses
// every hot key to the sweep; S3-FIFO's probation absorbs it.
func TestS3FIFOKeepsTheHotSetThroughAScan(t *testing.T) {
	t.Parallel()
	const capacity = 100
	survivors := func(policy cache.Policy) int {
		c := cache.NewCache[string, int](cache.Config[string, int]{MaxEntries: capacity, Policy: policy})
		hot := make([]string, 50)
		for i := range hot {
			hot[i] = "hot-" + strconv.Itoa(i)
		}
		for range 3 {
			for _, key := range hot {
				if _, ok := c.Fetch(key); !ok {
					c.Set(key, 1)
				}
			}
		}
		for i := range 2 * capacity {
			c.Set("scan-"+strconv.Itoa(i), 0)
		}
		kept := 0
		for _, key := range hot {
			if _, ok := c.Fetch(key); ok {
				kept++
			}
		}
		return kept
	}
	if got := survivors(cache.PolicyLRU); got != 0 {
		t.Errorf("LRU kept %d hot keys through the scan; the premise of the test is that it keeps none", got)
	}
	if got := survivors(cache.PolicyS3FIFO); got < 45 {
		t.Errorf("S3-FIFO kept %d of 50 hot keys through the scan, want at least 45", got)
	}
}

// TestAnUnknownPolicyRunsAsLRU pins the kernel's no-error rule: an unknown
// value is not refused, it runs as LRU and Stats says so.
func TestAnUnknownPolicyRunsAsLRU(t *testing.T) {
	t.Parallel()
	c := cache.NewCache[string, int](cache.Config[string, int]{MaxEntries: 2, Policy: cache.Policy(200)})
	if got := c.Stats().Policy; got != cache.PolicyLRU {
		t.Errorf("Stats().Policy = %d, want PolicyLRU", got)
	}
}

// TestAGhostKeyIsAdmittedIntoMain pins the ghost: a key probation evicted
// without a hit comes back into main, where a following scan cannot reach it
// before the probationary keys.
func TestAGhostKeyIsAdmittedIntoMain(t *testing.T) {
	t.Parallel()
	c := cache.NewCache[string, int](cache.Config[string, int]{MaxEntries: 10, Policy: cache.PolicyS3FIFO})
	c.Set("returning", 1)
	for i := range 20 {
		c.Set("filler-"+strconv.Itoa(i), 0)
	}
	if _, ok := c.Fetch("returning"); ok {
		t.Fatal("the key survived probation without a hit; the test needs it evicted")
	}
	c.Set("returning", 1)
	for i := range 9 {
		c.Set("scan-"+strconv.Itoa(i), 0)
	}
	if _, ok := c.Fetch("returning"); !ok {
		t.Error("a key the ghost remembered was evicted by a scan shorter than main")
	}
}

// TestTinyLFUKeepsTheHotSetThroughAScan is W-TinyLFU's scan resistance: a hot
// set used several times, then a sweep of cold keys twice the capacity. Each
// cold key leaves the window as a candidate used once, against main's victim
// used more: it is refused, and main keeps the hot set.
func TestTinyLFUKeepsTheHotSetThroughAScan(t *testing.T) {
	t.Parallel()
	type tc struct {
		name     string
		capacity int
		hot      int
		// least is the hot keys that must survive: a cold key whose four
		// counters all share with hot keys' is admitted (a count-min
		// sketch's error, about one key in two hundred), hence a margin.
		least int
	}
	tests := []tc{
		{name: "a hundred slots, fifty hot keys", capacity: 100, hot: 50, least: 45},
		{name: "four hundred slots, two hundred hot keys", capacity: 400, hot: 200, least: 180},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		store := cache.NewCache[string, int](cache.Config[string, int]{MaxEntries: c.capacity, Policy: cache.PolicyTinyLFU})
		hot := make([]string, c.hot)
		for i := range hot {
			hot[i] = "hot-" + strconv.Itoa(i)
		}
		for range 3 {
			for _, key := range hot {
				if _, ok := store.Fetch(key); !ok {
					store.Set(key, 1)
				}
			}
		}
		for i := range 2 * c.capacity {
			store.Set("scan-"+strconv.Itoa(i), 0)
		}
		kept := 0
		for _, key := range hot {
			if _, ok := store.Fetch(key); ok {
				kept++
			}
		}
		if kept < c.least {
			t.Errorf("W-TinyLFU kept %d of %d hot keys through the scan, want at least %d", kept, c.hot, c.least)
		}
		if got := store.Stats().Policy; got != cache.PolicyTinyLFU {
			t.Errorf("Stats().Policy = %d, want PolicyTinyLFU", got)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// TestTinyLFUServesTheKeyJustWritten pins that the admission never refuses a
// write outright: a new key enters the window, so a Set followed by a Fetch
// hits even when main is full of keys used far more. The guarantee is the
// write's, not the usage's: a fresh write is never the loser of a contest,
// so with room for one key a key used often is displaced by one written once.
func TestTinyLFUServesTheKeyJustWritten(t *testing.T) {
	t.Parallel()
	type tc struct {
		name     string
		capacity int
		// displaced is whether the fresh writes push every busy key out.
		displaced bool
	}
	tests := []tc{
		{name: "main full of busy keys", capacity: 20, displaced: false},
		{name: "room for one key", capacity: 1, displaced: true},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		store := cache.NewCache[string, int](cache.Config[string, int]{MaxEntries: c.capacity, Policy: cache.PolicyTinyLFU})
		for range 5 {
			for i := range c.capacity {
				key := "busy-" + strconv.Itoa(i)
				if _, ok := store.Fetch(key); !ok {
					store.Set(key, i)
				}
			}
		}
		for i := range 50 {
			key := "new-" + strconv.Itoa(i)
			store.Set(key, i)
			if _, ok := store.Fetch(key); !ok {
				t.Fatalf("%s missed right after its Set", key)
			}
		}
		_, held := store.Fetch("busy-0")
		if held == c.displaced {
			t.Errorf("a busy key held = %v after the fresh writes, want %v", held, !c.displaced)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}
