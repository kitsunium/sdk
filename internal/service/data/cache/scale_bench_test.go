package cache_test

import (
	"context"
	"math/rand/v2"
	"strconv"
	"testing"
	"time"

	corecache "github.com/kitsunium/sdk/internal/core/data/cache"
	svccache "github.com/kitsunium/sdk/internal/service/data/cache"
)

// scaleHits is how many entries the 1M rows read from: 2^20 keys, each set
// with a TTL of an hour, in a store bounded at twice that so none is evicted.
const scaleHits int = 1 << 20

// scaleOrder is how many key indexes are drawn beforehand: 2^24, read in a
// cycle, so the loop computes nothing but a mask.
const scaleOrder int = 1 << 24

// scaleSink keeps the value a row reads alive, so no read is optimised away.
var scaleSink string

// BenchmarkFetch_Hit_1M is Fetch_Hit over a million entries read in a uniform
// random order computed beforehand (#1059): most nodes are cold, so a hit
// pays the cache lines its stripe, its node and its record span, which one
// key held in L1 never shows. A row of the bench A/B lane's scale mode
// (`-benchtime=10000000x`), a few hundred megabytes: never on a shared
// machine. It uses only what the store offered at the base the lane compares
// against, so the base runs it too.
func BenchmarkFetch_Hit_1M(b *testing.B) {
	store, keys, order := scaleStore(b)
	ctx := b.Context()
	b.ReportAllocs()
	b.ResetTimer()
	i := 0
	for b.Loop() {
		key := keys[order[i&(scaleOrder-1)]]
		value, found, err := store.Fetch(ctx, key)
		if err != nil || !found {
			b.Fatalf("miss on seeded key %q: %v", key, err)
		}
		scaleSink = value
		i++
	}
}

// BenchmarkLoad_Hit_1M is the same read through Load, whose hit must cost a
// Fetch and nothing more at scale too.
func BenchmarkLoad_Hit_1M(b *testing.B) {
	store, keys, order := scaleStore(b)
	loader, ok := store.(corecache.Loader[string])
	if !ok {
		b.Fatal("the store does not implement Loader")
	}
	fill := func(context.Context) (corecache.EntryValue[string], error) {
		b.Fatal("the fill ran on a hit")
		return corecache.EntryValue[string]{}, nil
	}
	ctx := b.Context()
	b.ReportAllocs()
	b.ResetTimer()
	i := 0
	for b.Loop() {
		key := keys[order[i&(scaleOrder-1)]]
		value, err := loader.Load(ctx, key, fill)
		if err != nil {
			b.Fatalf("Load(%q): %v", key, err)
		}
		scaleSink = value
		i++
	}
}

// scaleStore builds the 1M rows' store, its keys and the order they are read
// in, outside the timer.
func scaleStore(b *testing.B) (store corecache.Store[string], keys []string, order []int32) {
	b.Helper()
	store, err := svccache.NewMemory[string](svccache.MemoryConfig{MaxEntries: 2 * scaleHits})
	if err != nil {
		b.Fatal(err)
	}
	ctx := b.Context()
	keys = make([]string, scaleHits)
	for i := range keys {
		keys[i] = "key-" + strconv.Itoa(i)
		if err := store.Set(ctx, keys[i], corecache.EntryValue[string]{Value: "v", TTL: time.Hour}); err != nil {
			b.Fatal(err)
		}
	}
	rng := rand.New(rand.NewPCG(1059, 1))
	order = make([]int32, scaleOrder)
	for i := range order {
		order[i] = int32(rng.IntN(scaleHits))
	}
	return store, keys, order
}
