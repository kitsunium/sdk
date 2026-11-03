package cache_test

import (
	"math/rand/v2"
	"strconv"
	"testing"

	"github.com/kitsunium/sdk/internal/kernel/collections/cache"
)

// byLength weighs a value by its length, the shape a byte budget takes.
func byLength(_ string, val []byte) int64 { return int64(len(val)) }

// TestTheWeightNeverExceedsMaxWeight is #839's acceptance: a 1 MiB budget
// under values of 1 KiB to 256 KiB never reports more than 1 MiB, checked
// after every Set.
func TestTheWeightNeverExceedsMaxWeight(t *testing.T) {
	t.Parallel()
	const budget = 1 << 20
	forEachPolicy(t, func(t *testing.T, policy cache.Policy) {
		random := rand.New(rand.NewPCG(5, 8))
		c := cache.NewCache[string, []byte](cache.Config[string, []byte]{MaxWeight: budget, WeightOf: byLength, Policy: policy})
		for i := range 2000 {
			key := "k" + strconv.Itoa(random.IntN(300))
			c.Set(key, make([]byte, 1024+random.IntN(255*1024)))
			if i%7 == 0 {
				c.Fetch("k" + strconv.Itoa(random.IntN(300)))
			}
			if got := c.Stats().Weight; got > budget {
				t.Fatalf("after Set %d the weight is %d, over the %d budget", i, got, budget)
			}
		}
	})
}

// TestAnEntryHeavierThanMaxWeightEvictsEverythingElse pins the stated limit:
// the primitive cannot refuse, so an oversized entry is stored alone.
func TestAnEntryHeavierThanMaxWeightEvictsEverythingElse(t *testing.T) {
	t.Parallel()
	forEachPolicy(t, func(t *testing.T, policy cache.Policy) {
		c := cache.NewCache[string, []byte](cache.Config[string, []byte]{MaxWeight: 100, WeightOf: byLength, Policy: policy})
		c.Set("a", make([]byte, 40))
		c.Set("b", make([]byte, 40))
		c.Set("huge", make([]byte, 500))
		if got := c.Len(); got != 1 {
			t.Fatalf("Len() = %d, want only the oversized entry", got)
		}
		if _, ok := c.Fetch("huge"); !ok {
			t.Error("the oversized entry was not stored")
		}
		if got := c.Stats().Weight; got != 500 {
			t.Errorf("Weight = %d, want 500", got)
		}
	})
}

// TestWithoutWeightOfMaxWeightIsACount pins nil WeightOf: every entry weighs
// one, so MaxWeight bounds the count as MaxEntries would.
func TestWithoutWeightOfMaxWeightIsACount(t *testing.T) {
	t.Parallel()
	forEachPolicy(t, func(t *testing.T, policy cache.Policy) {
		c := cache.NewCache[string, int](cache.Config[string, int]{MaxWeight: 3, Policy: policy})
		for i := range 10 {
			c.Set(strconv.Itoa(i), i)
		}
		if got := c.Len(); got != 3 {
			t.Errorf("Len() = %d, want 3", got)
		}
		if got := c.Stats().Weight; got != 3 {
			t.Errorf("Weight = %d, want 3", got)
		}
	})
}

// TestAHeavierOverwriteEvictsOthers pins that an overwrite re-weighs: a value
// grown past the budget makes room by evicting other entries.
func TestAHeavierOverwriteEvictsOthers(t *testing.T) {
	t.Parallel()
	forEachPolicy(t, func(t *testing.T, policy cache.Policy) {
		c := cache.NewCache[string, []byte](cache.Config[string, []byte]{MaxWeight: 100, WeightOf: byLength, Policy: policy})
		c.Set("a", make([]byte, 30))
		c.Set("b", make([]byte, 30))
		c.Set("c", make([]byte, 30))
		c.Set("c", make([]byte, 60))
		if got := c.Stats().Weight; got > 100 {
			t.Fatalf("Weight = %d after the overwrite, over 100", got)
		}
		if _, ok := c.Fetch("c"); !ok {
			t.Error("the overwritten entry itself was evicted")
		}
	})
}

// TestACacheThatWeighsNothingReportsNoWeight keeps StatsValue byte-for-byte
// what it was for a cache bounded by count alone.
func TestACacheThatWeighsNothingReportsNoWeight(t *testing.T) {
	t.Parallel()
	c := cache.NewCache[string, int](cache.Config[string, int]{MaxEntries: 4})
	c.Set("a", 1)
	if got := c.Stats().Weight; got != 0 {
		t.Errorf("Weight = %d, want 0 for a cache configured without weights", got)
	}
}
