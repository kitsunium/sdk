package cache

import (
	"hash/maphash"
	"strconv"
	"testing"
	"time"

	corecache "github.com/kitsunium/sdk/internal/core/data/cache"
	"github.com/kitsunium/sdk/internal/kernel/clock"
	kcache "github.com/kitsunium/sdk/internal/kernel/collections/cache"
)

// expireKeys is the thirty keys of the sweep scenario of #1405; a third of
// them, the multiples of three, carry no expiry.
const expireKeys, expireStripes = 30, 4

// crowdedSeed returns a hash seed that sends at least want of the thirty keys
// to one of four stripes, or fails the test: the seed #1405's runs drew by
// chance, one store in a thousand, made certain.
func crowdedSeed(t *testing.T, want int) maphash.Seed {
	t.Helper()
	probe := &memoryStore[string]{}
	for range 1 << 20 {
		probe.seed = maphash.MakeSeed()
		var perStripe [expireStripes]int
		for key := range expireKeys {
			perStripe[maphashString(probe, strconv.Itoa(key))%expireStripes]++
		}
		for _, held := range perStripe {
			if held >= want {
				return probe.seed
			}
		}
	}
	t.Fatalf("no seed crowds %d of %d keys into one of %d stripes", want, expireKeys, expireStripes)
	return probe.seed
}

// setSweepScenario sets the thirty keys, undated ones first being every third.
func setSweepScenario(t *testing.T, store *memoryStore[string]) {
	t.Helper()
	for key := range expireKeys {
		ttl := time.Second
		if key%3 == 0 {
			ttl = corecache.NoExpiry
		}
		if err := store.Set(t.Context(), strconv.Itoa(key), corecache.EntryValue[string]{Value: "v", TTL: ttl, Tags: []string{"all"}}); err != nil {
			t.Fatal(err)
		}
	}
}

// held counts the keys of the scenario the store still answers, whatever
// their deadline: the clock has not moved.
func held(t *testing.T, store *memoryStore[string]) int {
	t.Helper()
	found := 0
	for key := range expireKeys {
		if _, ok, _ := store.Fetch(t.Context(), strconv.Itoa(key)); ok {
			found++
		}
	}
	return found
}

// TestAStripeHoldsOnlyItsShareOfTheEntries is the cause of #1405, forced: a
// seed that sends seventeen of the thirty keys to one stripe of a store of 64
// entries on four stripes (sixteen each) loses an entry before any sweep, as
// the documented bound says. The sweep's test failed on such a draw, and the
// loss was the engine keeping its stripe's share, not Expire dropping an
// undated entry.
func TestAStripeHoldsOnlyItsShareOfTheEntries(t *testing.T) {
	t.Parallel()
	seed := crowdedSeed(t, 17)
	type tc struct {
		name   string
		policy kcache.Policy
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		store := concrete(t, MemoryConfig{MaxEntries: 64, Stripes: expireStripes, Policy: c.policy, Clock: clock.NewManualClock(time.Unix(0, 0))})
		store.seed = seed
		setSweepScenario(t, store)
		//: a stripe of 16 took 17: some key is no longer there.
		if got := held(t, store); got >= expireKeys {
			t.Errorf("the store holds %d of %d keys, want fewer: a stripe of 16 took 17", got, expireKeys)
		}
	}
	for _, c := range seedPolicyCases() {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc{name: c.name, policy: c.policy})
		})
	}
}

// seedPolicyCase names one eviction policy of the seeded scenarios.
type seedPolicyCase struct {
	name   string
	policy kcache.Policy
}

// seedPolicyCases lists the three eviction policies the scenarios run under.
func seedPolicyCases() []seedPolicyCase {
	//: every policy the store offers, by name.
	return []seedPolicyCase{
		{name: "lru", policy: kcache.PolicyLRU},
		{name: "s3fifo", policy: kcache.PolicyS3FIFO},
		{name: "tinylfu", policy: kcache.PolicyTinyLFU},
	}
}

// TestExpireRemovesEveryDueEntryWhateverTheStripeSeed is #1405's acceptance
// under the seed that crowds the most keys into one stripe, at the sizing the
// sweep's test uses: every stripe can hold every key, so nothing leaves before
// the clock moves, the sweep removes the twenty dated entries and the ten
// undated ones are all still there.
func TestExpireRemovesEveryDueEntryWhateverTheStripeSeed(t *testing.T) {
	t.Parallel()
	seed := crowdedSeed(t, 17)
	type tc struct {
		name   string
		policy kcache.Policy
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		manual := clock.NewManualClock(time.Unix(0, 0))
		store := concrete(t, MemoryConfig{MaxEntries: expireKeys * expireStripes, Stripes: expireStripes, Policy: c.policy, Clock: manual})
		store.seed = seed
		setSweepScenario(t, store)
		manual.Advance(2 * time.Second)
		removed, err := store.Expire(t.Context())
		//: the twenty dated entries, no more, no fewer.
		if err != nil || removed != 20 {
			t.Fatalf("Expire = (%d, %v), want (20, nil)", removed, err)
		}
		//: the ten undated ones are all still tagged.
		if dropped, _ := store.InvalidateTag(t.Context(), "all"); dropped != 10 {
			t.Errorf("InvalidateTag after the sweep dropped %d, want the 10 undated entries", dropped)
		}
	}
	for _, c := range seedPolicyCases() {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc{name: c.name, policy: c.policy})
		})
	}
}
