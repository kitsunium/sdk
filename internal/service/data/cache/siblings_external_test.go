package cache_test

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	corecache "github.com/kitsunium/sdk/internal/core/data/cache"
	"github.com/kitsunium/sdk/internal/kernel/clock"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	svccache "github.com/kitsunium/sdk/internal/service/data/cache"
)

// TestEveryStoreImplementsTheNewSiblings pins the four siblings of this wave on
// both stores — a sibling silently dropped would take every caller's fallback
// branch.
func TestEveryStoreImplementsTheNewSiblings(t *testing.T) {
	t.Parallel()
	near := newStore(t, svccache.MemoryConfig{MaxEntries: 8})
	far := newStore(t, svccache.MemoryConfig{MaxEntries: 8})
	chain, err := svccache.NewChain(svccache.ChainConfig{}, near, far)
	if err != nil {
		t.Fatal(err)
	}
	for name, store := range map[string]corecache.Store[string]{"memory": near, "chain": chain} {
		if _, ok := store.(corecache.Expirer); !ok {
			t.Errorf("%s: no Expirer", name)
		}
		if _, ok := store.(corecache.BulkFetcher[string]); !ok {
			t.Errorf("%s: no BulkFetcher", name)
		}
		if _, ok := store.(corecache.BulkSetter[string]); !ok {
			t.Errorf("%s: no BulkSetter", name)
		}
		if _, ok := store.(corecache.Refresher[string]); !ok {
			t.Errorf("%s: no Refresher", name)
		}
	}
}

// TestExpireRemovesEveryDueEntry is #1059's acceptance on the memory store:
// after the clock passes N deadlines, Expire returns N and the entries are
// gone, with no Fetch of theirs.
//
// A stripe holds MaxEntries / Stripes entries and a key's stripe follows the
// store's own random hash seed, so each stripe is sized to hold every key the
// test sets: at sixteen a stripe, about one store in a thousand drew a seed
// that sent seventeen of the thirty keys to one stripe, which evicted an
// undated entry before the sweep (#1405) — the test then measured the seed,
// not Expire.
func TestExpireRemovesEveryDueEntry(t *testing.T) {
	t.Parallel()
	const keys, stripes = 30, 4
	forEachPolicy(t, func(t *testing.T) {
		manual := clock.NewManualClock(time.Unix(0, 0))
		store := newStore(t, svccache.MemoryConfig{MaxEntries: keys * stripes, Stripes: stripes, Clock: manual})
		ctx := t.Context()
		for i := range keys {
			ttl := time.Second
			if i%3 == 0 {
				ttl = corecache.NoExpiry
			}
			if err := store.Set(ctx, strconv.Itoa(i), entry("v", ttl, "all")); err != nil {
				t.Fatal(err)
			}
		}
		manual.Advance(2 * time.Second)
		removed, err := store.(corecache.Expirer).Expire(ctx)
		if err != nil || removed != 20 {
			t.Fatalf("Expire = (%d, %v), want (20, nil)", removed, err)
		}
		if dropped, _ := store.(corecache.Tagger).InvalidateTag(ctx, "all"); dropped != 10 {
			t.Errorf("InvalidateTag after the sweep dropped %d, want the 10 undated entries", dropped)
		}
	})
}

// TestAFullStoreEvictsItsDeadEntryFirst is #1059's eviction refinement on the
// memory store: the most recently set entry has expired, so the next Set
// evicts it rather than the live least-recently-used one.
func TestAFullStoreEvictsItsDeadEntryFirst(t *testing.T) {
	t.Parallel()
	forEachPolicy(t, func(t *testing.T) {
		manual := clock.NewManualClock(time.Unix(0, 0))
		store := newStore(t, svccache.MemoryConfig{MaxEntries: 3, Clock: manual})
		ctx := t.Context()
		for _, key := range []string{"oldest", "older"} {
			if err := store.Set(ctx, key, entry("v", corecache.NoExpiry)); err != nil {
				t.Fatal(err)
			}
		}
		if err := store.Set(ctx, "dead", entry("v", time.Second)); err != nil {
			t.Fatal(err)
		}
		manual.Advance(time.Minute)
		if err := store.Set(ctx, "new", entry("v", corecache.NoExpiry)); err != nil {
			t.Fatal(err)
		}
		for _, key := range []string{"oldest", "older", "new"} {
			if _, ok, _ := store.Fetch(ctx, key); !ok {
				t.Errorf("%q was evicted while a dead entry held a slot", key)
			}
		}
	})
}

// TestChainExpireSweepsEveryTier counts the removals of both tiers.
func TestChainExpireSweepsEveryTier(t *testing.T) {
	t.Parallel()
	forEachPolicy(t, func(t *testing.T) {
		manual := clock.NewManualClock(time.Unix(0, 0))
		near := newStore(t, svccache.MemoryConfig{MaxEntries: 8, Clock: manual})
		far := newStore(t, svccache.MemoryConfig{MaxEntries: 8, Clock: manual})
		chain, err := svccache.NewChain(svccache.ChainConfig{}, near, far)
		if err != nil {
			t.Fatal(err)
		}
		if err := chain.Set(t.Context(), "k", entry("v", time.Second)); err != nil {
			t.Fatal(err)
		}
		manual.Advance(time.Minute)
		if removed, err := chain.(corecache.Expirer).Expire(t.Context()); err != nil || removed != 2 {
			t.Fatalf("Expire = (%d, %v), want (2, nil): one removal per tier", removed, err)
		}
	})
}

// TestFetchManyAfterSetManyReturnsEveryKey is #840's conformance on both
// stores, striped and not: after a SetMany, a FetchMany answers every
// position of its batch in order — each stored key with its value, a missing
// key with the zero value and false, a repeated key at each of its positions
// —, after whatever the slices it was handed already held. The long batch
// takes the striped store's scratch past the stack's.
func TestFetchManyAfterSetManyReturnsEveryKey(t *testing.T) {
	t.Parallel()
	type tc struct {
		name   string
		build  func(t *testing.T) corecache.Store[string]
		stored int
	}
	memory := func(stripes int) func(t *testing.T) corecache.Store[string] {
		return func(t *testing.T) corecache.Store[string] {
			t.Helper()
			return newStore(t, svccache.MemoryConfig{MaxEntries: 256, Stripes: stripes})
		}
	}
	chain := func(t *testing.T) corecache.Store[string] {
		t.Helper()
		near := newStore(t, svccache.MemoryConfig{MaxEntries: 256, Stripes: 8})
		far := newStore(t, svccache.MemoryConfig{MaxEntries: 256})
		built, err := svccache.NewChain(svccache.ChainConfig{}, near, far)
		if err != nil {
			t.Fatal(err)
		}
		return built
	}
	tests := []tc{
		{"memory", memory(1), 20},
		{"memory/striped", memory(8), 20},
		{"memory/striped/long", memory(8), 100},
		{"chain", chain, 20},
		{"chain/long", chain, 100},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		store := c.build(t)
		batch := map[string]corecache.EntryValue[string]{}
		for i := range c.stored {
			batch["k"+strconv.Itoa(i)] = entry("v"+strconv.Itoa(i), corecache.NoExpiry)
		}
		if err := store.(corecache.BulkSetter[string]).SetMany(t.Context(), batch); err != nil {
			t.Fatalf("SetMany: %v", err)
		}
		keys := []string{"k0", "absent", "k0"}
		for i := range c.stored {
			keys = append(keys, "k"+strconv.Itoa(i))
		}
		values, found, err := store.(corecache.BulkFetcher[string]).FetchMany(t.Context(), keys, []string{"held"}, []bool{true})
		if err != nil {
			t.Fatalf("FetchMany: %v", err)
		}
		if len(values) != 1+len(keys) || len(found) != 1+len(keys) || values[0] != "held" || !found[0] {
			t.Fatalf("FetchMany = %d values, %d found, first (%q, %v); want %d each after the one held", len(values), len(found), values[0], found[0], 1+len(keys))
		}
		for at, key := range keys {
			want, stored := batch[key]
			if got, ok := values[1+at], found[1+at]; ok != stored || got != want.Value {
				t.Errorf("position %d (%q) = (%q, %v), want (%q, %v)", at, key, got, ok, want.Value, stored)
			}
		}
	}
	forEachPolicy(t, func(t *testing.T) {
		for _, c := range tests {
			t.Run(c.name, func(t *testing.T) {
				t.Parallel()
				runCase(t, c)
			})
		}
	})
}

// TestFetchManyReportsANegativeEntryAsAMiss pins #838's rule through the
// batch (#840): a key whose fill answered Absent holds no value, so its
// position answers the zero value and false, as Fetch reports a miss.
func TestFetchManyReportsANegativeEntryAsAMiss(t *testing.T) {
	t.Parallel()
	forEachPolicy(t, func(t *testing.T) {
		store := newStore(t, svccache.MemoryConfig{MaxEntries: 8})
		ctx := t.Context()
		absent := func(context.Context) (corecache.EntryValue[string], error) {
			return corecache.EntryValue[string]{}, corecache.Absent(sql.ErrNoRows, time.Minute)
		}
		if _, err := store.(corecache.Loader[string]).Load(ctx, "missing", absent); !errors.Is(err, sql.ErrNoRows) {
			t.Fatalf("Load = %v, want sql.ErrNoRows", err)
		}
		if err := store.Set(ctx, "here", entry("v", time.Minute)); err != nil {
			t.Fatal(err)
		}
		values, found, err := store.(corecache.BulkFetcher[string]).FetchMany(ctx, []string{"missing", "here"}, nil, nil)
		if err != nil || found[0] || values[0] != "" || !found[1] || values[1] != "v" {
			t.Fatalf("FetchMany = (%q, %v, %v), want the negative entry a miss and here a hit", values, found, err)
		}
	})
}

// TestAChainFetchManyThatFailsAppendsNothing pins the batch's error path: a
// broken tier stops the walk, as it stops Fetch's, and the slices come back as
// they were handed — the nearer tier's hits are not served beside the
// failure.
func TestAChainFetchManyThatFailsAppendsNothing(t *testing.T) {
	t.Parallel()
	type tc struct {
		name   string
		broken int
	}
	tests := []tc{
		{name: "the near tier", broken: 0},
		{name: "the far tier", broken: 1},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		tiers := []*flakyStore{newFlaky(t), newFlaky(t)}
		chain, err := svccache.NewChain[string](svccache.ChainConfig{}, tiers[0], tiers[1])
		if err != nil {
			t.Fatal(err)
		}
		ctx := t.Context()
		if err := tiers[0].Set(ctx, "near", entry("n", corecache.NoExpiry)); err != nil {
			t.Fatal(err)
		}
		if err := tiers[1].Set(ctx, "far", entry("f", corecache.NoExpiry)); err != nil {
			t.Fatal(err)
		}
		tiers[c.broken].fails = failGet
		values, found, err := chain.(corecache.BulkFetcher[string]).FetchMany(ctx, []string{"near", "far"}, []string{"held"}, []bool{true})
		if !errs.HasCode(err, corecache.CodeCacheTierFailed) {
			t.Fatalf("FetchMany = %v, want CACHE_TIER_FAILED", err)
		}
		if len(values) != 1 || len(found) != 1 || values[0] != "held" || !found[0] {
			t.Fatalf("FetchMany appended to a failed batch: %q, %v", values, found)
		}
	}
	forEachPolicy(t, func(t *testing.T) {
		for _, c := range tests {
			t.Run(c.name, func(t *testing.T) {
				t.Parallel()
				runCase(t, c)
			})
		}
	})
}

// TestFetchManyReusesTheSlicesItIsHanded pins the contract a zero-allocation
// batch rests on (#840), on both stores: slices with the room for the batch
// are written in place, and grow only when their capacity is short.
func TestFetchManyReusesTheSlicesItIsHanded(t *testing.T) {
	t.Parallel()
	type tc struct {
		name    string
		keys    int
		inPlace bool
	}
	tests := []tc{
		{name: "the room for every key", keys: 16, inPlace: true},
		{name: "the room for some", keys: 17, inPlace: false},
	}
	runCase := func(t *testing.T, c tc, store corecache.Store[string]) {
		t.Helper()
		keys := make([]string, c.keys)
		for at := range keys {
			keys[at] = "k" + strconv.Itoa(at)
		}
		values, found := make([]string, 0, 16), make([]bool, 0, 16)
		gotValues, gotFound, err := store.(corecache.BulkFetcher[string]).FetchMany(t.Context(), keys, values, found)
		if err != nil || len(gotValues) != c.keys || len(gotFound) != c.keys {
			t.Fatalf("FetchMany = (%d values, %d found, %v), want %d each", len(gotValues), len(gotFound), err, c.keys)
		}
		inPlace := &gotValues[0] == &values[:1][0] && &gotFound[0] == &found[:1][0]
		if inPlace != c.inPlace {
			t.Fatalf("written in place = %v, want %v", inPlace, c.inPlace)
		}
	}
	forEachPolicy(t, func(t *testing.T) {
		for _, c := range tests {
			for name, store := range map[string]corecache.Store[string]{
				"memory":         newStore(t, svccache.MemoryConfig{MaxEntries: 64}),
				"memory/striped": newStore(t, svccache.MemoryConfig{MaxEntries: 64, Stripes: 8}),
				"chain":          newChainOfTwo(t),
			} {
				t.Run(c.name+"/"+name, func(t *testing.T) {
					t.Parallel()
					runCase(t, c, store)
				})
			}
		}
	})
}

// newChainOfTwo is a chain of two memory tiers.
func newChainOfTwo(t *testing.T) corecache.Store[string] {
	t.Helper()
	chain, err := svccache.NewChain(svccache.ChainConfig{}, newStore(t, svccache.MemoryConfig{MaxEntries: 64}), newStore(t, svccache.MemoryConfig{MaxEntries: 64}))
	if err != nil {
		t.Fatal(err)
	}
	return chain
}

// TestSetManyStoresNothingWhenOneEntryIsRefused pins all-or-nothing.
func TestSetManyStoresNothingWhenOneEntryIsRefused(t *testing.T) {
	t.Parallel()
	forEachPolicy(t, func(t *testing.T) {
		near := newStore(t, svccache.MemoryConfig{MaxEntries: 16})
		far := newStore(t, svccache.MemoryConfig{MaxEntries: 16})
		chain, err := svccache.NewChain(svccache.ChainConfig{}, near, far)
		if err != nil {
			t.Fatal(err)
		}
		for name, store := range map[string]corecache.Store[string]{"memory": near, "chain": chain} {
			batch := map[string]corecache.EntryValue[string]{
				"good":  entry("v", corecache.NoExpiry),
				"empty": entry("v", corecache.NoExpiry, ""),
			}
			err := store.(corecache.BulkSetter[string]).SetMany(t.Context(), batch)
			if !errs.HasCode(err, corecache.CodeCacheEntryRejected) {
				t.Fatalf("%s: SetMany = %v, want CACHE_ENTRY_REJECTED", name, err)
			}
			if _, ok, _ := store.Fetch(t.Context(), "good"); ok {
				t.Errorf("%s: the valid entry of a refused batch was stored", name)
			}
		}
	})
}

// countingTier counts the FetchEntry calls a tier answers.
type countingTier struct {
	corecache.Store[string]
	fetches atomic.Int64
}

func (c *countingTier) FetchEntry(ctx context.Context, key string) (corecache.EntryValue[string], bool, error) {
	c.fetches.Add(1)
	return c.Store.(corecache.EntryFetcher[string]).FetchEntry(ctx, key)
}

func (c *countingTier) InvalidateTag(ctx context.Context, tag string) (int, error) {
	return c.Store.(corecache.Tagger).InvalidateTag(ctx, tag)
}

// TestChainFetchManyMakesOnePassPerTier is #840's chain acceptance: each tier
// is asked only for the keys every nearer tier missed, once each — a key the
// batch names twice included —, and the far tier's hits are promoted so the
// next batch is answered near.
func TestChainFetchManyMakesOnePassPerTier(t *testing.T) {
	t.Parallel()
	forEachPolicy(t, func(t *testing.T) {
		near := &countingTier{Store: newStore(t, svccache.MemoryConfig{MaxEntries: 64})}
		far := &countingTier{Store: newStore(t, svccache.MemoryConfig{MaxEntries: 64})}
		chain, err := svccache.NewChain(svccache.ChainConfig{}, near, far)
		if err != nil {
			t.Fatal(err)
		}
		ctx := t.Context()
		for i := range 8 {
			if err := far.Set(ctx, strconv.Itoa(i), entry("v", corecache.NoExpiry)); err != nil {
				t.Fatal(err)
			}
		}
		if err := near.Set(ctx, "0", entry("v", corecache.NoExpiry)); err != nil {
			t.Fatal(err)
		}
		keys := []string{"0", "1", "2", "3", "4", "5", "6", "7", "absent", "3"}
		_, found, err := chain.(corecache.BulkFetcher[string]).FetchMany(ctx, keys, nil, nil)
		if err != nil || hitsOf(found) != 9 {
			t.Fatalf("FetchMany = (%d hits, %v), want 9", hitsOf(found), err)
		}
		if n, f := near.fetches.Load(), far.fetches.Load(); n != 9 || f != 8 {
			t.Fatalf("near answered %d FetchEntry, far %d; want 9 and 8 — one pass each, far only for the near misses, a repeat read once", n, f)
		}
		near.fetches.Store(0)
		far.fetches.Store(0)
		if _, _, err := chain.(corecache.BulkFetcher[string]).FetchMany(ctx, keys[:8], nil, nil); err != nil {
			t.Fatal(err)
		}
		if f := far.fetches.Load(); f != 0 {
			t.Errorf("after the batched promotion the far tier answered %d reads, want 0", f)
		}
	})
}

// hitsOf counts the positions a FetchMany found.
func hitsOf(found []bool) int {
	hits := 0
	for _, ok := range found {
		if ok {
			hits++
		}
	}
	return hits
}

// TestLoadRefreshingServesTheOldValueWhileOneFillReplacesIt is #838's first
// acceptance: inside the window a hit returns at once, with the fill blocked,
// and the entry is replaced once the fill returns; outside it, no fill.
func TestLoadRefreshingServesTheOldValueWhileOneFillReplacesIt(t *testing.T) {
	t.Parallel()
	forEachPolicy(t, func(t *testing.T) {
		manual := clock.NewManualClock(time.Unix(0, 0))
		near := newStore(t, svccache.MemoryConfig{MaxEntries: 8, Clock: manual})
		far := newStore(t, svccache.MemoryConfig{MaxEntries: 8, Clock: manual})
		chain, err := svccache.NewChain(svccache.ChainConfig{}, near, far)
		if err != nil {
			t.Fatal(err)
		}
		for name, store := range map[string]corecache.Store[string]{"memory": newStore(t, svccache.MemoryConfig{MaxEntries: 8, Clock: manual}), "chain": chain} {
			refresher := store.(corecache.Refresher[string])
			ctx := t.Context()
			if err := store.Set(ctx, "k", entry("old", time.Minute)); err != nil {
				t.Fatal(err)
			}
			var fills atomic.Int64
			release := make(chan struct{})
			done := make(chan struct{})
			fill := func(context.Context) (corecache.EntryValue[string], error) {
				fills.Add(1)
				<-release
				defer close(done)
				return entry("new", time.Minute), nil
			}
			if got, err := refresher.LoadRefreshing(ctx, "k", fill, 10*time.Second); err != nil || got != "old" {
				t.Fatalf("%s: outside the window = (%q, %v)", name, got, err)
			}
			if fills.Load() != 0 {
				t.Fatalf("%s: a hit outside the window started a fill", name)
			}
			manual.Advance(55 * time.Second)
			for range 3 {
				if got, err := refresher.LoadRefreshing(ctx, "k", fill, 10*time.Second); err != nil || got != "old" {
					t.Fatalf("%s: inside the window = (%q, %v), want the old value at once", name, got, err)
				}
			}
			close(release)
			<-done
			deadline := time.Now().Add(5 * time.Second)
			for {
				if got, _, _ := store.Fetch(ctx, "k"); got == "new" {
					break
				}
				if time.Now().After(deadline) {
					t.Fatalf("%s: the refreshed value never replaced the entry", name)
				}
				time.Sleep(time.Millisecond)
			}
			if got := fills.Load(); got != 1 {
				t.Errorf("%s: %d fills for three hits in the window, want 1", name, got)
			}
			if _, err := refresher.LoadRefreshing(ctx, "k", fill, 0); !errs.HasCode(err, corecache.CodeCacheMisconfigured) {
				t.Errorf("%s: a zero window = %v, want CACHE_MISCONFIGURED", name, err)
			}
		}
	})
}

// TestAnAbsentAnswerIsCachedForItsLifetime is #838's second acceptance: a
// fill answering Absent(sql.ErrNoRows, ttl) runs once per ttl however often
// the key is read, every read answering errors.Is(err, sql.ErrNoRows).
func TestAnAbsentAnswerIsCachedForItsLifetime(t *testing.T) {
	t.Parallel()
	forEachPolicy(t, func(t *testing.T) {
		manual := clock.NewManualClock(time.Unix(0, 0))
		store := newStore(t, svccache.MemoryConfig{MaxEntries: 8, Clock: manual})
		loader := store.(corecache.Loader[string])
		var fills atomic.Int64
		fill := func(context.Context) (corecache.EntryValue[string], error) {
			fills.Add(1)
			return corecache.EntryValue[string]{}, corecache.Absent(sql.ErrNoRows, time.Minute)
		}
		for range 1000 {
			if _, err := loader.Load(t.Context(), "missing", fill); !errors.Is(err, sql.ErrNoRows) {
				t.Fatalf("Load = %v, want sql.ErrNoRows", err)
			}
		}
		if got := fills.Load(); got != 1 {
			t.Fatalf("%d fills for 1000 reads within the lifetime, want 1", got)
		}
		if _, ok, _ := store.Fetch(t.Context(), "missing"); ok {
			t.Error("Fetch reported a hit on a negative entry")
		}
		manual.Advance(2 * time.Minute)
		if _, err := loader.Load(t.Context(), "missing", fill); !errors.Is(err, sql.ErrNoRows) {
			t.Fatalf("Load after the lifetime = %v", err)
		}
		if got := fills.Load(); got != 2 {
			t.Errorf("%d fills after the lifetime, want 2", got)
		}
		if err := store.Set(t.Context(), "missing", entry("found", time.Minute)); err != nil {
			t.Fatal(err)
		}
		if got, err := loader.Load(t.Context(), "missing", fill); err != nil || got != "found" {
			t.Errorf("Load after a Set over the negative entry = (%q, %v)", got, err)
		}
	})
}

// TestStripesAreRefusedWhenNegative pins the ADR 0031 refusal of the new
// options, and NewWeighedMemory's two half-configurations.
func TestStripesAreRefusedWhenNegative(t *testing.T) {
	t.Parallel()
	byLen := func(value string) int64 { return int64(len(value)) }
	for name, build := range map[string]func() error{
		"negative stripes": func() error {
			_, err := svccache.NewMemory[string](svccache.MemoryConfig{MaxEntries: 8, Stripes: -1})
			return err
		},
		"MaxBytes without a weigher": func() error {
			_, err := svccache.NewMemory[string](svccache.MemoryConfig{MaxEntries: 8, MaxBytes: 1024})
			return err
		},
		"a weigher without MaxBytes": func() error {
			_, err := svccache.NewWeighedMemory(svccache.MemoryConfig{MaxEntries: 8}, byLen)
			return err
		},
		"a nil weigher": func() error {
			_, err := svccache.NewWeighedMemory[string](svccache.MemoryConfig{MaxEntries: 8, MaxBytes: 1024}, nil)
			return err
		},
	} {
		if err := build(); !errs.HasCode(err, corecache.CodeCacheMisconfigured) {
			t.Errorf("%s: %v, want CACHE_MISCONFIGURED", name, err)
		}
	}
}

// TestAnEntryHeavierThanTheByteBoundIsRefused pins #839's domain refusal.
func TestAnEntryHeavierThanTheByteBoundIsRefused(t *testing.T) {
	t.Parallel()
	store, err := svccache.NewWeighedMemory(svccache.MemoryConfig{MaxEntries: 8, MaxBytes: 100}, func(value string) int64 { return int64(len(value)) })
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Set(t.Context(), "big", entry(string(make([]byte, 101)), corecache.NoExpiry)); !errs.HasCode(err, corecache.CodeCacheEntryRejected) {
		t.Fatalf("Set of a 101-byte value under a 100-byte bound = %v, want CACHE_ENTRY_REJECTED", err)
	}
	if err := store.Set(t.Context(), "fits", entry(string(make([]byte, 100)), corecache.NoExpiry)); err != nil {
		t.Fatalf("Set of a value exactly at the bound = %v", err)
	}
}
