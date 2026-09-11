package cache

import (
	"strconv"
	"testing"
	"time"

	corecache "github.com/kitsunium/sdk/internal/core/cache"
	"github.com/kitsunium/sdk/internal/kernel/clock"
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
	manual := clock.NewManualClock(time.Unix(0, 0))
	store := concrete(t, MemoryConfig{MaxEntries: 16, Clock: manual})
	ctx := t.Context()

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
		//: touch every key so lazy expiry actually fires.
		for probe := range 40 {
			if _, _, fetchErr := store.Fetch(ctx, strconv.Itoa(probe)); fetchErr != nil {
				t.Fatalf("Fetch: %v", fetchErr)
			}
		}
	}

	store.mu.Lock()
	defer store.mu.Unlock()
	if indexed, held := len(store.tags.byKey), store.lru.Len(); indexed > held {
		t.Fatalf("the index names %d keys but the store holds %d — a removal path left the reverse index behind", indexed, held)
	}
	for key := range store.tags.byKey {
		if _, ok := store.lru.Fetch(key); !ok {
			t.Fatalf("the index names %q, which the store no longer holds", key)
		}
	}
	//: no empty bucket may survive, or the outer map grows with every tag the
	//: store has EVER seen rather than with the tags it currently carries.
	for tag, bucket := range store.tags.byTag {
		if len(bucket) == 0 {
			t.Fatalf("tag %q kept an empty bucket", tag)
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
	store := concrete(t, MemoryConfig{MaxEntries: 2})
	for i := range 500 {
		if err := store.Set(t.Context(), strconv.Itoa(i), corecache.EntryValue[string]{
			Value: "v", Tags: []string{"t"},
		}); err != nil {
			t.Fatalf("Set: %v", err)
		}
	}
	if got := store.lru.Len(); got != 2 {
		t.Fatalf("the store holds %d entries, want 2", got)
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
	store.mu.Lock()
	owned := len(store.tags.byKey["k"])
	bucket := len(store.tags.byTag["t"])
	store.mu.Unlock()
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
