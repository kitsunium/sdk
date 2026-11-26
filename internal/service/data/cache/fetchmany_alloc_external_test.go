//go:build !race

// The allocation gate of the batch read (kitsunium/sdk#840). Race off,
// because the race detector allocates on every memory access: the alloc lane
// (tools/alloc-lane-targets.txt) is its only gate.
package cache_test

import (
	"slices"
	"strconv"
	"testing"
	"time"

	corecache "github.com/kitsunium/sdk/internal/core/data/cache"
	svccache "github.com/kitsunium/sdk/internal/service/data/cache"
)

// fetchManyAllocRuns is how many batches a window counts.
const fetchManyAllocRuns int = 1000

// fetchManyWarmCalls is how many batches run before a window opens, so every
// runtime cache the path fills on first use is filled — kit's figure for its
// generated gates (ADR 0165).
const fetchManyWarmCalls int = 30000

// fetchManyAllocKeys is how many keys the gate's store holds, each with a
// deadline, so every batch reads the clock as the threshold's benchmark does.
const fetchManyAllocKeys int = 1024

// TestFetchManyAllocatesNothing is the claim FetchMany's shape was changed
// for (#840): a batch of at most 64 keys into the slices of the batch before,
// emptied, allocates nothing — on one stripe and on eight —, where the map it
// used to return cost four allocations a call and their collection. Each case
// walks the store's keys a batch at a time, every key a hit. Mutation-checked:
// the primitive's references taken from nil rather than from the stripe's
// stack scratch fails every case, one allocation a call.
func TestFetchManyAllocatesNothing(t *testing.T) {
	type tc struct {
		name    string
		stripes int
		batch   int
	}
	tests := []tc{
		{name: "one stripe, 16 keys", stripes: 1, batch: 16},
		{name: "one stripe, 64 keys", stripes: 1, batch: 64},
		{name: "eight stripes, 16 keys", stripes: 8, batch: 16},
		{name: "eight stripes, 64 keys", stripes: 8, batch: 64},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		store, err := svccache.NewMemory[string](svccache.MemoryConfig{MaxEntries: 4 * fetchManyAllocKeys, Stripes: c.stripes})
		if err != nil {
			t.Fatal(err)
		}
		ctx := t.Context()
		keys := make([]string, fetchManyAllocKeys)
		for at := range keys {
			keys[at] = "key-" + strconv.Itoa(at)
			if err := store.Set(ctx, keys[at], corecache.EntryValue[string]{Value: "v", TTL: time.Hour}); err != nil {
				t.Fatal(err)
			}
		}
		bulk := store.(corecache.BulkFetcher[string])
		values, found := make([]string, 0, c.batch), make([]bool, 0, c.batch)
		var fetchErr error
		next := 0
		call := func() {
			start := (next * c.batch) % fetchManyAllocKeys
			values, found, fetchErr = bulk.FetchMany(ctx, keys[start:start+c.batch], values[:0], found[:0])
			next++
		}
		for range fetchManyWarmCalls {
			call()
		}
		got := mallocsOver(fetchManyAllocRuns, call)
		if fetchErr != nil || len(found) != c.batch || slices.Contains(found, false) {
			t.Fatalf("FetchMany = (%d answers, %v), want %d hits", len(found), fetchErr, c.batch)
		}
		reportWindow(t, c.name, got)
		if got.own != 0 {
			t.Fatalf("%d batches of %d keys made %d allocations, want none", fetchManyAllocRuns, c.batch, got.own)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			runCase(t, c)
		})
	}
}
