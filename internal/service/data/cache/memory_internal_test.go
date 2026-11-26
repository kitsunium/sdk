package cache

import (
	"hash/maphash"
	"math/rand/v2"
	"strconv"
	"testing"
	"time"

	corecache "github.com/kitsunium/sdk/internal/core/data/cache"
	"github.com/kitsunium/sdk/internal/kernel/clock"
	kcache "github.com/kitsunium/sdk/internal/kernel/collections/cache"
)

// concrete builds a memoryStore and hands back the concrete type, which the
// constructor deliberately hides behind the port.
func concrete(t *testing.T, cfg MemoryConfig) *memoryStore[string] {
	t.Helper()
	store, err := NewMemory[string](cfg)
	if err != nil {
		t.Fatalf("NewMemory: %v", err)
	}
	typed, ok := store.(*memoryStore[string])
	if !ok {
		t.Fatalf("NewMemory returned %T, want *memoryStore[string]", store)
	}
	return typed
}

// TestTagIndexNeverNamesAKeyTheStoreNoLongerHolds is the invariant that keeps
// the reverse index from becoming a leak.
//
// Every removal path must unlink the index, and they do NOT share a mechanism:
// capacity eviction and TTL expiry come back through OnEvict, an overwrite is
// handled inside tagIndex.add, and Delete/InvalidateTag unlink directly —
// because the underlying primitive deliberately does not fire OnEvict for an
// explicit Delete. Four paths, one invariant, so it is asserted after a
// workload that exercises all four rather than after each one.
func TestTagIndexNeverNamesAKeyTheStoreNoLongerHolds(t *testing.T) {
	t.Parallel()
	forEachShape(t, func(t *testing.T, stripes int, policy kcache.Policy) {
		tagIndexWorkload(t, MemoryConfig{MaxEntries: 16, Stripes: stripes, Policy: policy})
	})
}

// tagIndexWorkload is TestTagIndexNeverNamesAKeyTheStoreNoLongerHolds on one
// configuration. The sweep (#1059) is a fifth removal path, and a batch's
// lazy expiry (#840) a sixth, so the workload runs both too.
func tagIndexWorkload(t *testing.T, cfg MemoryConfig) {
	t.Helper()
	manual := clock.NewManualClock(time.Unix(0, 0))
	cfg.Clock = manual
	store := concrete(t, cfg)
	ctx := t.Context()
	probes := make([]string, 40)
	for at := range probes {
		probes[at] = strconv.Itoa(at)
	}

	for round := range 200 {
		key := strconv.Itoa(round % 40)
		//: mixed TTLs so some entries expire and some do not.
		ttl := time.Duration(round%3) * time.Second
		if err := store.Set(ctx, key, corecache.EntryValue[string]{
			Value: "v", TTL: ttl, Tags: []string{"all", "mod:" + strconv.Itoa(round%5)},
		}); err != nil {
			t.Fatalf("Set: %v", err)
		}
		//: exercise the explicit-Delete path, which OnEvict never sees.
		if round%7 == 0 {
			if err := store.Delete(ctx, key); err != nil {
				t.Fatalf("Delete: %v", err)
			}
		}
		//: exercise the invalidation path.
		if round%11 == 0 {
			if _, err := store.InvalidateTag(ctx, "mod:1"); err != nil {
				t.Fatalf("InvalidateTag: %v", err)
			}
		}
		manual.Advance(400 * time.Millisecond)
		//: the caller-driven sweep, every few rounds.
		if round%13 == 0 {
			if _, err := store.Expire(ctx); err != nil {
				t.Fatalf("Expire: %v", err)
			}
		}
		//: touch every key so lazy expiry actually fires — one Fetch a key on
		//: even rounds, one FetchMany of all forty on odd ones, whose batch
		//: reaps through the same onEvict (#840).
		if round%2 == 1 {
			if _, _, fetchErr := store.FetchMany(ctx, probes, nil, nil); fetchErr != nil {
				t.Fatalf("FetchMany: %v", fetchErr)
			}
			continue
		}
		for _, probe := range probes {
			if _, _, fetchErr := store.Fetch(ctx, probe); fetchErr != nil {
				t.Fatalf("Fetch: %v", fetchErr)
			}
		}
	}

	for _, stripe := range store.stripes {
		assertStripeIndexed(t, stripe)
	}
}

// assertStripeIndexed checks one stripe's index against its primitive.
func assertStripeIndexed(t *testing.T, stripe *memoryStripe[string]) {
	t.Helper()
	stripe.mu.Lock()
	defer stripe.mu.Unlock()
	if indexed, held := len(stripe.tags.byKey), stripe.lru.Len(); indexed > held {
		t.Fatalf("the index names %d keys but the stripe holds %d — a removal path left the reverse index behind", indexed, held)
	}
	for key := range stripe.tags.byKey {
		if _, ok := stripe.lru.Fetch(key); !ok {
			t.Fatalf("the index names %q, which the store no longer holds", key)
		}
	}
	//: no empty bucket may survive, or the outer map grows with every tag the
	//: store has EVER seen rather than with the tags it currently carries.
	for tag, bucket := range stripe.tags.byTag {
		if len(bucket) == 0 {
			t.Fatalf("tag %q kept an empty bucket", tag)
		}
	}
}

// forEachShape runs body at one stripe and at eight (#808), under every
// eviction policy (#834), as parallel subtests.
func forEachShape(t *testing.T, body func(t *testing.T, stripes int, policy kcache.Policy)) {
	t.Helper()
	for _, stripes := range []int{1, 8} {
		for _, policy := range []kcache.Policy{kcache.PolicyLRU, kcache.PolicyS3FIFO, kcache.PolicyTinyLFU} {
			t.Run("stripes="+strconv.Itoa(stripes)+"/policy="+strconv.Itoa(int(policy)), func(t *testing.T) {
				t.Parallel()
				body(t, stripes, policy)
			})
		}
	}
}

// TestEvictionUnderTheStoreLockDoesNotDeadlock pins the onEvict rule.
//
// The primitive invokes OnEvict on the CALLER's goroutine, which is inside a
// method holding s.mu, and sync.Mutex is not reentrant. If onEvict ever grows
// a lock, this test does not fail — it HANGS, and the go test timeout reports
// it by name. That is the honest failure mode: a deadlock cannot be asserted
// against, only provoked.
func TestEvictionUnderTheStoreLockDoesNotDeadlock(t *testing.T) {
	t.Parallel()
	forEachShape(t, func(t *testing.T, stripes int, policy kcache.Policy) {
		evictionWorkload(t, MemoryConfig{MaxEntries: 16, Stripes: stripes, Policy: policy})
	})
}

// evictionWorkload is TestEvictionUnderTheStoreLockDoesNotDeadlock on one
// configuration.
func evictionWorkload(t *testing.T, cfg MemoryConfig) {
	t.Helper()
	store := concrete(t, cfg)
	for i := range 500 {
		if err := store.Set(t.Context(), strconv.Itoa(i), corecache.EntryValue[string]{
			Value: "v", Tags: []string{"t"},
		}); err != nil {
			t.Fatalf("Set: %v", err)
		}
	}
	held := 0
	for _, stripe := range store.stripes {
		held += stripe.lru.Len()
	}
	if held != cfg.MaxEntries {
		t.Fatalf("the store holds %d entries, want %d", held, cfg.MaxEntries)
	}
}

// TestPrimitiveTTLMapsTheThreeMeaningsOntoTwo pins the translation that keeps
// "no deadline" spellable in a store that has a default.
func TestPrimitiveTTLMapsTheThreeMeaningsOntoTwo(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name         string
		ttl          time.Duration
		storeDefault time.Duration
		want         time.Duration
	}{
		{"explicit lifetime wins over the default", time.Second, time.Minute, time.Second},
		{"zero defers to the default", 0, time.Minute, time.Minute},
		{"zero with no default means no deadline", 0, 0, 0},
		{"NoExpiry beats a default", corecache.NoExpiry, time.Minute, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := primitiveTTL(tc.ttl, tc.storeDefault); got != tc.want {
				t.Fatalf("primitiveTTL(%v, %v) = %v, want %v", tc.ttl, tc.storeDefault, got, tc.want)
			}
		})
	}
}

// TestTagsAreDeduplicated: a tag is a set membership, so indexing the same
// pair twice would make InvalidateTag's removed count wrong.
func TestTagsAreDeduplicated(t *testing.T) {
	t.Parallel()
	store := concrete(t, MemoryConfig{MaxEntries: 8})
	if err := store.Set(t.Context(), "k", corecache.EntryValue[string]{
		Value: "v", Tags: []string{"t", "t", "t"},
	}); err != nil {
		t.Fatalf("Set: %v", err)
	}
	stripe := store.stripeOf("k")
	stripe.mu.Lock()
	owned := len(stripe.tags.byKey["k"])
	bucket := len(stripe.tags.byTag["t"])
	stripe.mu.Unlock()
	if owned != 1 || bucket != 1 {
		t.Fatalf("the index holds %d tags and a bucket of %d, want 1 and 1", owned, bucket)
	}
}

// TestTheCallersTagSliceIsNotRetained: a caller may reuse or mutate the slice
// it passed, and doing so must not reach into the store.
func TestTheCallersTagSliceIsNotRetained(t *testing.T) {
	t.Parallel()
	store := concrete(t, MemoryConfig{MaxEntries: 8})
	tags := []string{"keep"}
	if err := store.Set(t.Context(), "k", corecache.EntryValue[string]{Value: "v", Tags: tags}); err != nil {
		t.Fatalf("Set: %v", err)
	}
	tags[0] = "hijacked"

	removed, err := store.InvalidateTag(t.Context(), "keep")
	if err != nil || removed != 1 {
		t.Fatalf("InvalidateTag = (%d, %v), want (1, nil) — the store kept the caller's slice", removed, err)
	}
}

// TestTheByteBoundHoldsAfterEverySet is #839's acceptance: a 1 MiB budget
// under values of 1 KiB to 256 KiB never holds more than 1 MiB, read off the
// stripes' primitives after every Set, at one stripe and at eight. Each stripe
// is held to its own share too (#1380): the total hid a stripe over its share
// whenever the others were under theirs, so the S3-FIFO walk that left a
// stripe over its bound failed this test about once in a hundred runs, as the
// per-process hash seed spread the keys.
func TestTheByteBoundHoldsAfterEverySet(t *testing.T) {
	t.Parallel()
	forEachShape(t, func(t *testing.T, stripes int, policy kcache.Policy) {
		store := weighedBytesStore(t, stripes, policy)
		holdsTheByteBound(t, store, stripes)
	})
}

// TestTheByteBoundHoldsWhateverTheStripeSeed is #1380's reproduction: the
// keys' spread over the stripes follows the store's hash seed, drawn per
// process, so the run that failed one time in a hundred was a seed. The seed
// is set here, a hundred of them at each shape, and the bound is held to
// every one.
func TestTheByteBoundHoldsWhateverTheStripeSeed(t *testing.T) {
	t.Parallel()
	forEachShape(t, func(t *testing.T, stripes int, policy kcache.Policy) {
		//: one stripe has no hash to seed.
		if stripes == 1 {
			t.Skip("one stripe: no seed picks a stripe")
		}
		for range 100 {
			store := weighedBytesStore(t, stripes, policy)
			store.seed = maphash.MakeSeed()
			holdsTheByteBound(t, store, stripes)
		}
	})
}

// weighedBytesStore builds the 1 MiB store of the byte-bound tests.
func weighedBytesStore(t *testing.T, stripes int, policy kcache.Policy) *memoryStore[[]byte] {
	t.Helper()
	built, err := NewWeighedMemory(MemoryConfig{MaxEntries: 1000, MaxBytes: 1 << 20, Stripes: stripes, Policy: policy},
		func(value []byte) int64 { return int64(len(value)) })
	if err != nil {
		t.Fatal(err)
	}
	return built.(*memoryStore[[]byte])
}

// holdsTheByteBound sets 600 values of 1 KiB to 256 KiB and reads the bound
// off the stripes' primitives after every Set.
func holdsTheByteBound(t *testing.T, store *memoryStore[[]byte], stripes int) {
	t.Helper()
	const budget = 1 << 20
	random := rand.New(rand.NewPCG(uint64(stripes), 11))
	for i := range 600 {
		value := make([]byte, 1024+random.IntN(255*1024))
		if err := store.Set(t.Context(), "k"+strconv.Itoa(random.IntN(200)), corecache.EntryValue[[]byte]{Value: value}); err != nil {
			//: a value over one stripe's share is refused; that is the bound.
			if stripes > 1 {
				continue
			}
			t.Fatalf("Set %d: %v", i, err)
		}
		var held int64
		for at, stripe := range store.stripes {
			weight := stripe.lru.Stats().Weight
			if share := shareOf64(budget, len(store.stripes), at); weight > share {
				t.Fatalf("after Set %d stripe %d holds %d bytes, over its share of %d", i, at, weight, share)
			}
			held += weight
		}
		if held > budget {
			t.Fatalf("after Set %d the store holds %d bytes, over %d", i, held, budget)
		}
	}
}

// TestFetchManyReadsARepeatOnce pins the port's read-once rule on the memory
// store (#840), striped and not: a key a batch names three times is answered
// at every position and recorded once by its stripe's primitive, a negative
// entry is a miss at its position, and every miss counts as Fetch counts it.
func TestFetchManyReadsARepeatOnce(t *testing.T) {
	t.Parallel()
	type tc struct {
		name       string
		keys       []string
		want       []string
		hits, miss uint64
	}
	tests := []tc{
		{name: "a repeat", keys: []string{"a", "b", "a", "a"}, want: []string{"A", "B", "A", "A"}, hits: 2},
		{name: "a negative entry", keys: []string{"n", "a", "n"}, want: []string{"", "A", ""}, hits: 2},
		{name: "a repeated miss", keys: []string{"x", "a", "x"}, want: []string{"", "A", ""}, hits: 1, miss: 2},
	}
	runCase := func(t *testing.T, c tc, stripes int, policy kcache.Policy) {
		t.Helper()
		store := concrete(t, MemoryConfig{MaxEntries: 64, Stripes: stripes, Policy: policy})
		mustSet(t, store, "a", "A")
		mustSet(t, store, "b", "B")
		negative := store.stripeOf("n")
		negative.mu.Lock()
		store.store(negative, "n", record[string]{absent: corecache.CacheBackendFailed}, time.Hour, 0)
		negative.mu.Unlock()
		before := primitiveStats(store)
		values, found, err := store.FetchMany(t.Context(), c.keys, nil, nil)
		if err != nil {
			t.Fatalf("FetchMany: %v", err)
		}
		for at, want := range c.want {
			if values[at] != want || found[at] != (want != "") {
				t.Errorf("position %d (%q) = (%q, %v), want (%q, %v)", at, c.keys[at], values[at], found[at], want, want != "")
			}
		}
		after := primitiveStats(store)
		if hits, misses := after.Hits-before.Hits, after.Misses-before.Misses; hits != c.hits || misses != c.miss {
			t.Fatalf("the primitives counted %d hits and %d misses, want %d and %d", hits, misses, c.hits, c.miss)
		}
	}
	forEachShape(t, func(t *testing.T, stripes int, policy kcache.Policy) {
		for _, c := range tests {
			t.Run(c.name, func(t *testing.T) {
				t.Parallel()
				runCase(t, c, stripes, policy)
			})
		}
	})
}

// primitiveStats sums the counters of every stripe's primitive.
func primitiveStats(store *memoryStore[string]) kcache.StatsValue {
	var sum kcache.StatsValue
	for _, stripe := range store.stripes {
		stripe.mu.Lock()
		stats := stripe.lru.Stats()
		stripe.mu.Unlock()
		sum.Hits += stats.Hits
		sum.Misses += stats.Misses
	}
	return sum
}
