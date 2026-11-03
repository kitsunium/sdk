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
