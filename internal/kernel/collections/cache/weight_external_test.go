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

// TestTheS3FIFOWalkMakesRoomWhileAVictimIsLeft is #1380: under S3-FIFO the
// eviction gave up with a victim left, and the weight stayed over the bound,
// in three places. The probation walk stopped at the entry the Set wrote,
// taken for the newest, though an overwrite leaves it where it sits, newer
// entries ahead of it. A walk that promoted its hit entries into main and
// evicted nothing did not ask main again. An entry alone in main with a hit
// left was taken for the entry just written and never evicted. Each case
// ends with a Set over the bound of 100, and is run under both policies.
func TestTheS3FIFOWalkMakesRoomWhileAVictimIsLeft(t *testing.T) {
	t.Parallel()
	type tc struct {
		name   string
		writes func(c *cache.Cache[string, []byte])
		// kept is the key the last Set wrote, never its own victim.
		kept string
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		forEachPolicy(t, func(t *testing.T, policy cache.Policy) {
			store := cache.NewCache[string, []byte](cache.Config[string, []byte]{MaxWeight: 100, WeightOf: byLength, Policy: policy})
			c.writes(store)
			if got := store.Stats().Weight; got > 100 {
				t.Fatalf("Weight = %d after %q was written, over 100", got, c.kept)
			}
			if _, ok := store.Fetch(c.kept); !ok {
				t.Errorf("the entry just written, %q, was evicted", c.kept)
			}
		})
	}
	// overwrite writes keys of 10 bytes in order, then key again with 95.
	overwrite := func(key string, keys ...string) func(c *cache.Cache[string, []byte]) {
		return func(c *cache.Cache[string, []byte]) {
			for _, k := range keys {
				c.Set(k, make([]byte, 10))
			}
			c.Set(key, make([]byte, 95))
		}
	}
	// ghostX makes the ghost remember x: x leaves probation without a hit.
	ghostX := func(c *cache.Cache[string, []byte]) {
		c.Set("x", make([]byte, 10))
		c.Set("y", make([]byte, 95))
		c.Delete("y")
	}
	cases := []tc{
		{name: "an overwrite of the oldest of two", writes: overwrite("a", "a", "b"), kept: "a"},
		{name: "an overwrite of the oldest of three", writes: overwrite("a", "a", "b", "c"), kept: "a"},
		{name: "an overwrite of the middle of three", writes: overwrite("b", "a", "b", "c"), kept: "b"},
		{name: "a promotion out of probation", kept: "x", writes: func(c *cache.Cache[string, []byte]) {
			ghostX(c)
			//: s sits in probation, under its tenth, with a hit.
			c.Set("s", make([]byte, 5))
			c.Fetch("s")
			//: x comes back into main, alone there, over the bound with s.
			c.Set("x", make([]byte, 99))
		}},
		{name: "an entry alone in main with a hit", kept: "n", writes: func(c *cache.Cache[string, []byte]) {
			ghostX(c)
			//: x comes back into main, alone there, and is hit.
			c.Set("x", make([]byte, 40))
			c.Fetch("x")
			//: n enters probation, over the bound with x.
			c.Set("n", make([]byte, 70))
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}
