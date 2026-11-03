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
func TestExpireRemovesEveryDueEntry(t *testing.T) {
	t.Parallel()
	forEachPolicy(t, func(t *testing.T) {
		manual := clock.NewManualClock(time.Unix(0, 0))
		store := newStore(t, svccache.MemoryConfig{MaxEntries: 64, Stripes: 4, Clock: manual})
		ctx := t.Context()
		for i := range 30 {
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
// stores, striped and not: every stored key comes back, a missing key has no
// entry, and a duplicate is answered once.
func TestFetchManyAfterSetManyReturnsEveryKey(t *testing.T) {
	t.Parallel()
	forEachPolicy(t, func(t *testing.T) {
		near := newStore(t, svccache.MemoryConfig{MaxEntries: 64, Stripes: 8})
		far := newStore(t, svccache.MemoryConfig{MaxEntries: 64})
		chain, err := svccache.NewChain(svccache.ChainConfig{}, near, far)
		if err != nil {
			t.Fatal(err)
		}
		for name, store := range map[string]corecache.Store[string]{"memory": far, "chain": chain} {
			batch := map[string]corecache.EntryValue[string]{}
			for i := range 20 {
				batch[name+strconv.Itoa(i)] = entry("v"+strconv.Itoa(i), corecache.NoExpiry)
			}
			if err := store.(corecache.BulkSetter[string]).SetMany(t.Context(), batch); err != nil {
				t.Fatalf("%s: SetMany: %v", name, err)
			}
			keys := []string{name + "0", name + "0", "absent"}
			for i := range 20 {
				keys = append(keys, name+strconv.Itoa(i))
			}
			got, err := store.(corecache.BulkFetcher[string]).FetchMany(t.Context(), keys)
			if err != nil {
				t.Fatalf("%s: FetchMany: %v", name, err)
			}
			if len(got) != 20 {
				t.Fatalf("%s: FetchMany found %d keys, want 20", name, len(got))
			}
			for key, want := range batch {
				if got[key] != want.Value {
					t.Errorf("%s: %q = %q, want %q", name, key, got[key], want.Value)
				}
			}
		}
	})
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
// is asked only for the keys every nearer tier missed, once each, and the far
// tier's hits are promoted so the next batch is answered near.
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
		keys := []string{"0", "1", "2", "3", "4", "5", "6", "7", "absent"}
		got, err := chain.(corecache.BulkFetcher[string]).FetchMany(ctx, keys)
		if err != nil || len(got) != 8 {
			t.Fatalf("FetchMany = (%d keys, %v), want 8", len(got), err)
		}
		if n, f := near.fetches.Load(), far.fetches.Load(); n != 9 || f != 8 {
			t.Fatalf("near answered %d FetchEntry, far %d; want 9 and 8 — one pass each, far only for the near misses", n, f)
		}
		near.fetches.Store(0)
		far.fetches.Store(0)
		if _, err := chain.(corecache.BulkFetcher[string]).FetchMany(ctx, keys[:8]); err != nil {
			t.Fatal(err)
		}
		if f := far.fetches.Load(); f != 0 {
			t.Errorf("after the batched promotion the far tier answered %d reads, want 0", f)
		}
	})
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
