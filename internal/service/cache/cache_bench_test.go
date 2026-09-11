package cache_test

import (
	"context"
	"strconv"
	"testing"
	"time"

	corecache "github.com/kitsunium/sdk/internal/core/cache"
	svccache "github.com/kitsunium/sdk/internal/service/cache"
)

// benchStore builds a store or fails the benchmark.
func benchStore(b *testing.B, maxEntries int) corecache.Store[string] {
	b.Helper()
	store, err := svccache.NewMemory[string](svccache.MemoryConfig{MaxEntries: maxEntries})
	if err != nil {
		b.Fatal(err)
	}
	return store
}

// BenchmarkSet_Untagged is the baseline: what a Set costs when the tag index
// is never touched.
func BenchmarkSet_Untagged(b *testing.B) {
	store := benchStore(b, 4096)
	ctx := b.Context()
	b.ReportAllocs()
	b.ResetTimer()
	for i := range b.N {
		if err := store.Set(ctx, strconv.Itoa(i), corecache.EntryValue[string]{Value: "v"}); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkSet_OneTag / _ThreeTags are the same Set with the index engaged.
// The DIFFERENCE against the untagged baseline is the measured per-entry cost
// of tagging — the number internal/service/cache/CLAUDE.md quotes instead of
// estimating from struct sizes.
func BenchmarkSet_OneTag(b *testing.B) {
	benchmarkTaggedSet(b, 1)
}

func BenchmarkSet_ThreeTags(b *testing.B) {
	benchmarkTaggedSet(b, 3)
}

func benchmarkTaggedSet(b *testing.B, tagCount int) {
	b.Helper()
	store := benchStore(b, 4096)
	ctx := b.Context()
	tags := make([]string, tagCount)
	for t := range tagCount {
		//: a small tag alphabet, as in production: many keys, few tags.
		tags[t] = "tag:" + strconv.Itoa(t)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := range b.N {
		if err := store.Set(ctx, strconv.Itoa(i), corecache.EntryValue[string]{Value: "v", Tags: tags}); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkInvalidateTag_In1k and _In100k are THE measurement this domain
// makes a claim about: invalidation costs O(k) in the entries carrying the
// tag, not O(N) in the store.
//
// Both invalidate a tag carried by exactly 100 entries. The only thing that
// differs is how many OTHER entries the store holds — 900 versus 99 900. If
// the two numbers are close, the reverse index is doing its job; if the 100k
// case is ~100× slower, the implementation is scanning.
func BenchmarkInvalidateTag_In1k(b *testing.B) {
	benchmarkInvalidateTag(b, 1_000, 100)
}

func BenchmarkInvalidateTag_In100k(b *testing.B) {
	benchmarkInvalidateTag(b, 100_000, 100)
}

func benchmarkInvalidateTag(b *testing.B, storeSize, tagged int) {
	b.Helper()
	//: capacity above storeSize so nothing evicts mid-run and the store size
	//: under test stays the one named in the benchmark's name.
	store := benchStore(b, storeSize*2)
	tagger, ok := store.(corecache.Tagger)
	if !ok {
		b.Fatal("the store does not implement Tagger")
	}
	ctx := b.Context()
	//: the OTHER entries carry tags of their own, so the reverse index is
	//: genuinely populated — a store whose only tag is the one under test
	//: would flatter the result by leaving byTag nearly empty.
	for i := tagged; i < storeSize; i++ {
		if err := store.Set(ctx, strconv.Itoa(i), corecache.EntryValue[string]{
			Value: "v", Tags: []string{"bulk:" + strconv.Itoa(i%64)},
		}); err != nil {
			b.Fatal(err)
		}
	}
	//: only the hot keys are re-seeded per iteration; the other storeSize-100
	//: entries are untouched by the invalidation and stay put. Re-seeding the
	//: whole store instead would make the benchmark quadratic in b.N.
	seedHot := func() {
		for i := range tagged {
			if err := store.Set(ctx, strconv.Itoa(i), corecache.EntryValue[string]{
				Value: "v", Tags: []string{"hot"},
			}); err != nil {
				b.Fatal(err)
			}
		}
	}
	seedHot()
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		removed, err := tagger.InvalidateTag(ctx, "hot")
		if err != nil {
			b.Fatal(err)
		}
		if removed != tagged {
			b.Fatalf("removed %d, want %d", removed, tagged)
		}
		//: re-seeding is setup, not measurement.
		b.StopTimer()
		seedHot()
		b.StartTimer()
	}
}

// BenchmarkFetch_Hit is the read path, for scale: everything above is measured
// against what a plain hit costs.
func BenchmarkFetch_Hit(b *testing.B) {
	store := benchStore(b, 1<<10)
	ctx := b.Context()
	if err := store.Set(ctx, "k", corecache.EntryValue[string]{Value: "v", TTL: time.Hour}); err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		if _, found, err := store.Fetch(ctx, "k"); err != nil || !found {
			b.Fatal("miss")
		}
	}
}

// BenchmarkLoad_Hit proves the claim in Load's comment: a hit does not enter
// the singleflight group at all, so it costs a Fetch and nothing more.
func BenchmarkLoad_Hit(b *testing.B) {
	store := benchStore(b, 1<<10)
	loader, ok := store.(corecache.Loader[string])
	if !ok {
		b.Fatal("the store does not implement Loader")
	}
	ctx := b.Context()
	if err := store.Set(ctx, "k", corecache.EntryValue[string]{Value: "v", TTL: time.Hour}); err != nil {
		b.Fatal(err)
	}
	fill := func(context.Context) (corecache.EntryValue[string], error) {
		b.Fatal("the fill ran on a hit")
		return corecache.EntryValue[string]{}, nil
	}
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		if _, err := loader.Load(ctx, "k", fill); err != nil {
			b.Fatal(err)
		}
	}
}
