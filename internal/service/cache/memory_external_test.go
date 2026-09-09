package cache_test

import (
	"context"
	"errors"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	corecache "github.com/kitsunium/sdk/internal/core/cache"
	"github.com/kitsunium/sdk/internal/kernel/clock"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	svccache "github.com/kitsunium/sdk/internal/service/cache"
)

// errOrigin is what a failing fill returns. A plain stdlib error is the point:
// it is exactly the untyped shape Load must normalise.
var errOrigin = errors.New("origin unreachable")

// newStore builds a memory store or fails the test.
func newStore(t *testing.T, cfg svccache.MemoryConfig) corecache.Store[string] {
	t.Helper()
	store, err := svccache.NewMemory[string](cfg)
	if err != nil {
		t.Fatalf("NewMemory: %v", err)
	}
	return store
}

// entry is a small constructor for the test's entries.
func entry(value string, ttl time.Duration, tags ...string) corecache.EntryValue[string] {
	return corecache.EntryValue[string]{Value: value, TTL: ttl, Tags: tags}
}

func TestMemoryStoreImplementsEverySibling(t *testing.T) {
	t.Parallel()
	store := newStore(t, svccache.MemoryConfig{MaxEntries: 8})
	if _, ok := store.(corecache.EntryFetcher[string]); !ok {
		t.Error("the memory store does not implement EntryFetcher")
	}
	if _, ok := store.(corecache.Tagger); !ok {
		t.Error("the memory store does not implement Tagger")
	}
	if _, ok := store.(corecache.Loader[string]); !ok {
		t.Error("the memory store does not implement Loader")
	}
}

func TestSetFetchDelete(t *testing.T) {
	t.Parallel()
	store := newStore(t, svccache.MemoryConfig{MaxEntries: 8})
	ctx := t.Context()

	if _, found, err := store.Fetch(ctx, "absent"); found || err != nil {
		t.Fatalf("a miss reported (%t, %v), want (false, nil) — a miss is not an error", found, err)
	}
	if err := store.Set(ctx, "k", entry("v", 0)); err != nil {
		t.Fatalf("Set: %v", err)
	}
	value, found, err := store.Fetch(ctx, "k")
	if err != nil || !found || value != "v" {
		t.Fatalf("Fetch = (%q, %t, %v), want (\"v\", true, nil)", value, found, err)
	}
	if err := store.Delete(ctx, "k"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, found := held(t, store, "k"); found {
		t.Fatal("the key survived Delete")
	}
	if err := store.Delete(ctx, "k"); err != nil {
		t.Fatalf("a second Delete returned %v, want nil — Delete is idempotent", err)
	}
}

// TestZeroConfigIsRefused is ADR 0031 on the constructor: the zero MemoryConfig
// must not build a cache that silently never evicts.
func TestZeroConfigIsRefused(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		cfg  svccache.MemoryConfig
	}{
		{"zero value", svccache.MemoryConfig{}},
		{"negative capacity", svccache.MemoryConfig{MaxEntries: -1}},
		{"negative default TTL", svccache.MemoryConfig{MaxEntries: 4, DefaultTTL: -time.Second}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			store, err := svccache.NewMemory[string](tc.cfg)
			if err == nil {
				t.Fatalf("NewMemory built a store from %+v, want a refusal", tc.cfg)
			}
			if store != nil {
				t.Fatal("NewMemory returned a store alongside its error")
			}
			if !errs.HasCode(err, corecache.CodeCacheMisconfigured) {
				t.Fatalf("NewMemory returned %v, want CACHE_MISCONFIGURED", err)
			}
		})
	}
}

func TestUnstorableEntriesAreRefused(t *testing.T) {
	t.Parallel()
	store := newStore(t, svccache.MemoryConfig{MaxEntries: 8})
	cases := []struct {
		name  string
		key   string
		entry corecache.EntryValue[string]
	}{
		{"empty key", "", entry("v", 0)},
		{"empty tag", "k", entry("v", 0, "good", "")},
		{"negative ttl that is not NoExpiry", "k", entry("v", -2*time.Second)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := store.Set(t.Context(), tc.key, tc.entry)
			if !errs.HasCode(err, corecache.CodeCacheEntryRejected) {
				t.Fatalf("Set returned %v, want CACHE_ENTRY_REJECTED", err)
			}
		})
	}
}

func TestTTLVocabulary(t *testing.T) {
	t.Parallel()
	manual := clock.NewManualClock(time.Unix(0, 0))
	store := newStore(t, svccache.MemoryConfig{MaxEntries: 8, DefaultTTL: time.Minute, Clock: manual})

	//: zero defers to the store's default (one minute).
	mustSet(t, store, "default", entry("v", 0))
	//: NoExpiry outlives every default.
	mustSet(t, store, "forever", entry("v", corecache.NoExpiry))
	//: an explicit lifetime overrides the default in both directions.
	mustSet(t, store, "short", entry("v", time.Second))

	manual.Advance(2 * time.Second)
	if _, found := held(t, store, "short"); found {
		t.Error("the one-second entry survived two seconds")
	}
	if _, found := held(t, store, "default"); !found {
		t.Error("the default-TTL entry expired after two seconds of a one-minute default")
	}

	manual.Advance(2 * time.Minute)
	if _, found := held(t, store, "default"); found {
		t.Error("the default-TTL entry survived past the default")
	}
	if _, found := held(t, store, "forever"); !found {
		t.Error("the NoExpiry entry expired — NoExpiry collapsed into the default")
	}
}

func TestFetchEntryReportsTheRemainingLifetimeNotTheOriginal(t *testing.T) {
	t.Parallel()
	manual := clock.NewManualClock(time.Unix(0, 0))
	store := newStore(t, svccache.MemoryConfig{MaxEntries: 8, Clock: manual})
	fetcher, _ := store.(corecache.EntryFetcher[string])
	mustSet(t, store, "k", entry("v", time.Minute, "t"))

	manual.Advance(40 * time.Second)
	got, found, err := fetcher.FetchEntry(t.Context(), "k")
	if err != nil || !found {
		t.Fatalf("FetchEntry = (%t, %v), want a hit", found, err)
	}
	if got.TTL != 20*time.Second {
		t.Fatalf("FetchEntry reported TTL %v, want 20s — promoting with the ORIGINAL TTL would refresh the entry on every move", got.TTL)
	}
	if len(got.Tags) != 1 || got.Tags[0] != "t" {
		t.Fatalf("FetchEntry reported tags %v, want [t] — a promoted copy without its tags is unreachable by InvalidateTag", got.Tags)
	}
	//: NoExpiry must survive the round trip, or a promoted eternal entry would
	//: acquire the near tier's default deadline.
	mustSet(t, store, "forever", entry("v", corecache.NoExpiry))
	eternal, _, eternalErr := fetcher.FetchEntry(t.Context(), "forever")
	if eternalErr != nil {
		t.Fatalf("FetchEntry(forever): %v", eternalErr)
	}
	if eternal.TTL != corecache.NoExpiry {
		t.Fatalf("FetchEntry reported %v for a NoExpiry entry, want NoExpiry", eternal.TTL)
	}
}

func TestInvalidateTagRemovesExactlyTheTaggedEntries(t *testing.T) {
	t.Parallel()
	store := newStore(t, svccache.MemoryConfig{MaxEntries: 32})
	tagger, _ := store.(corecache.Tagger)

	mustSet(t, store, "a", entry("1", 0, "user:42", "page"))
	mustSet(t, store, "b", entry("2", 0, "user:42"))
	mustSet(t, store, "c", entry("3", 0, "user:99"))
	mustSet(t, store, "d", entry("4", 0))

	removed := invalidated(t, tagger, "user:42")
	if removed != 2 {
		t.Fatalf("InvalidateTag removed %d, want 2", removed)
	}
	//: the two tagged entries must be gone.
	for _, key := range []string{"a", "b"} {
		if _, found := held(t, store, key); found {
			t.Errorf("%q survived the invalidation of its tag", key)
		}
	}
	//: and nothing else may have been touched.
	for _, key := range []string{"c", "d"} {
		if _, found := held(t, store, key); !found {
			t.Errorf("%q was removed by an unrelated tag", key)
		}
	}
	//: the entry carried TWO tags; the other bucket must not still name it.
	if again := invalidated(t, tagger, "page"); again != 0 {
		t.Fatalf("the second tag still named the removed entry (%d) — the reverse index leaked", again)
	}
	//: a tag naming nothing is not an error.
	if unknown := invalidated(t, tagger, "never-used"); unknown != 0 {
		t.Fatalf("an unknown tag removed %d, want 0", unknown)
	}
	if _, err := tagger.InvalidateTag(t.Context(), ""); !errs.HasCode(err, corecache.CodeCacheEntryRejected) {
		t.Fatalf("an empty tag returned %v, want CACHE_ENTRY_REJECTED", err)
	}
}

// TestOverwritingReplacesTheTags proves the index follows the entry rather
// than accumulating: a key re-Set with different tags must not stay reachable
// through the tags it no longer carries.
func TestOverwritingReplacesTheTags(t *testing.T) {
	t.Parallel()
	store := newStore(t, svccache.MemoryConfig{MaxEntries: 8})
	tagger, _ := store.(corecache.Tagger)

	mustSet(t, store, "k", entry("v1", 0, "old"))
	mustSet(t, store, "k", entry("v2", 0, "new"))

	if stale := invalidated(t, tagger, "old"); stale != 0 {
		t.Fatalf("the old tag still named the key (%d) — an overwrite left it indexed", stale)
	}
	if fresh := invalidated(t, tagger, "new"); fresh != 1 {
		t.Fatalf("the new tag removed %d, want 1", fresh)
	}
}

// TestLoadRunsTheFillOnceForConcurrentMisses is the anti-stampede claim.
func TestLoadRunsTheFillOnceForConcurrentMisses(t *testing.T) {
	t.Parallel()
	store := newStore(t, svccache.MemoryConfig{MaxEntries: 8})
	loader, _ := store.(corecache.Loader[string])
	var fills atomic.Int64
	release := make(chan struct{})
	const callers int = 24

	var wg sync.WaitGroup
	for range callers {
		wg.Go(func() {
			value, err := loader.Load(t.Context(), "hot", func(context.Context) (corecache.EntryValue[string], error) {
				fills.Add(1)
				<-release
				return entry("filled", time.Minute), nil
			})
			if err != nil || value != "filled" {
				t.Errorf("Load = (%q, %v), want (\"filled\", nil)", value, err)
			}
		})
	}
	//: hold the fill open until every caller has had the chance to collide;
	//: the count below is then a real measure of collapsing, not of timing.
	waitUntil(t, func() bool { return fills.Load() == 1 }, "the first fill to start")
	time.Sleep(20 * time.Millisecond)
	close(release)
	wg.Wait()

	if got := int(fills.Load()); got >= callers {
		t.Fatalf("the fill ran %d times for %d concurrent callers — nothing was collapsed", got, callers)
	}
	if _, found := held(t, store, "hot"); !found {
		t.Fatal("Load did not store what it filled")
	}
}

func TestLoadServesAHitWithoutFilling(t *testing.T) {
	t.Parallel()
	store := newStore(t, svccache.MemoryConfig{MaxEntries: 8})
	loader, _ := store.(corecache.Loader[string])
	mustSet(t, store, "k", entry("stored", time.Minute))

	value, err := loader.Load(t.Context(), "k", func(context.Context) (corecache.EntryValue[string], error) {
		t.Error("the fill ran for a key the cache holds")
		return entry("filled", 0), nil
	})
	if err != nil || value != "stored" {
		t.Fatalf("Load = (%q, %v), want (\"stored\", nil)", value, err)
	}
}

func TestAFailedFillStoresNothing(t *testing.T) {
	t.Parallel()
	store := newStore(t, svccache.MemoryConfig{MaxEntries: 8})
	loader, _ := store.(corecache.Loader[string])

	_, err := loader.Load(t.Context(), "k", func(context.Context) (corecache.EntryValue[string], error) {
		return corecache.EntryValue[string]{}, errOrigin
	})
	if !errs.HasCode(err, corecache.CodeCacheFillFailed) {
		t.Fatalf("Load returned %v, want CACHE_FILL_FAILED", err)
	}
	if _, found := held(t, store, "k"); found {
		t.Fatal("a failed fill was cached — one bad minute at the origin would become a whole TTL of wrong answers")
	}
}

// TestATypedFillErrorKeepsItsOwnCode is ADR 0005 §origin wins at the Load
// boundary: relabelling a typed error would replace the origin's diagnosis
// with a generic one.
func TestATypedFillErrorKeepsItsOwnCode(t *testing.T) {
	t.Parallel()
	store := newStore(t, svccache.MemoryConfig{MaxEntries: 8})
	loader, _ := store.(corecache.Loader[string])

	_, err := loader.Load(t.Context(), "k", func(context.Context) (corecache.EntryValue[string], error) {
		return corecache.EntryValue[string]{}, corecache.CacheBackendFailed
	})
	if errs.HasCode(err, corecache.CodeCacheFillFailed) {
		t.Fatal("a typed fill error was relabelled CACHE_FILL_FAILED")
	}
	if !errs.HasCode(err, corecache.CodeCacheBackendFailed) {
		t.Fatalf("Load returned %v, want the origin's own code", err)
	}
}

// TestEvictionUnlinksTheTagIndex is the leak this index would otherwise have:
// an entry dropped by capacity must leave its tag buckets, or InvalidateTag
// spends its time deleting keys that no longer exist and the buckets grow
// forever.
func TestEvictionUnlinksTheTagIndex(t *testing.T) {
	t.Parallel()
	store := newStore(t, svccache.MemoryConfig{MaxEntries: 4})
	tagger, _ := store.(corecache.Tagger)

	for i := range 64 {
		mustSet(t, store, strconv.Itoa(i), entry("v", 0, "shared"))
	}
	//: only the four surviving entries may still carry the tag.
	removed := invalidated(t, tagger, "shared")
	if removed != 4 {
		t.Fatalf("InvalidateTag removed %d, want 4 (the cache holds 4) — evicted keys stayed in the reverse index", removed)
	}
}

// held reports whether store holds key, failing the test on a backend error so
// no assertion below has to decide what a failure means.
func held(t *testing.T, store corecache.Store[string], key string) (string, bool) {
	t.Helper()
	value, found, err := store.Fetch(t.Context(), key)
	if err != nil {
		t.Fatalf("Fetch(%q): %v", key, err)
	}
	return value, found
}

// invalidated runs InvalidateTag and fails the test on error, returning the
// count the assertions are actually about.
func invalidated(t *testing.T, tagger corecache.Tagger, tag string) int {
	t.Helper()
	removed, err := tagger.InvalidateTag(t.Context(), tag)
	if err != nil {
		t.Fatalf("InvalidateTag(%q): %v", tag, err)
	}
	return removed
}

// mustSet stores entry or fails the test.
func mustSet(t *testing.T, store corecache.Store[string], key string, value corecache.EntryValue[string]) {
	t.Helper()
	if err := store.Set(t.Context(), key, value); err != nil {
		t.Fatalf("Set(%q): %v", key, err)
	}
}

// waitUntil spins until cond holds, so an assertion never encodes a guess
// about how long a goroutine takes to start.
func waitUntil(t *testing.T, cond func() bool, what string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(time.Millisecond)
	}
}
