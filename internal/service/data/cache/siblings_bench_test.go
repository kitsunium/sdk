package cache_test

import (
	"context"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	corecache "github.com/kitsunium/sdk/internal/core/data/cache"
	"github.com/kitsunium/sdk/internal/kernel/clock"
	svccache "github.com/kitsunium/sdk/internal/service/data/cache"
)

// benchStripes are the stripe counts the parallel rows compare (#808).
var benchStripes = []int{1, 8, 64}

// stripedStore builds a store of the given stripe count, seeded with keys.
func stripedStore(b *testing.B, stripes int, keys []string) corecache.Store[string] {
	b.Helper()
	store, err := svccache.NewMemory[string](svccache.MemoryConfig{MaxEntries: 4 * len(keys), Stripes: stripes})
	if err != nil {
		b.Fatal(err)
	}
	for _, key := range keys {
		if err := store.Set(b.Context(), key, corecache.EntryValue[string]{Value: "v", TTL: time.Hour}); err != nil {
			b.Fatal(err)
		}
	}
	return store
}

// benchKeys are the keys every row reads, built outside the timed loops.
func benchKeys(n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = "key-" + strconv.Itoa(i)
	}
	return out
}

// BenchmarkFetch_Parallel reads seeded keys from every goroutine (run with
// -cpu=1,8): at one stripe every hit waits on the store's one lock, at 8 and
// 64 on its stripe's.
func BenchmarkFetch_Parallel(b *testing.B) {
	keys := benchKeys(1024)
	for _, stripes := range benchStripes {
		b.Run("stripes="+strconv.Itoa(stripes), func(b *testing.B) {
			store := stripedStore(b, stripes, keys)
			ctx := b.Context()
			b.ReportAllocs()
			b.ResetTimer()
			var offset atomic.Int64
			b.RunParallel(func(pb *testing.PB) {
				//: each goroutine starts at its own place in the key set.
				i := int(offset.Add(131))
				for pb.Next() {
					if _, ok, _ := store.Fetch(ctx, keys[i&1023]); !ok {
						b.Error("miss on a seeded key")
						return
					}
					i++
				}
			})
		})
	}
}

// BenchmarkLoad_Parallel is Fetch_Parallel through Load: every read a hit, so
// the singleflight group is never entered.
func BenchmarkLoad_Parallel(b *testing.B) {
	keys := benchKeys(1024)
	fill := func(context.Context) (corecache.EntryValue[string], error) {
		return corecache.EntryValue[string]{Value: "v"}, nil
	}
	for _, stripes := range benchStripes {
		b.Run("stripes="+strconv.Itoa(stripes), func(b *testing.B) {
			loader := stripedStore(b, stripes, keys).(corecache.Loader[string])
			ctx := b.Context()
			b.ReportAllocs()
			b.ResetTimer()
			var offset atomic.Int64
			b.RunParallel(func(pb *testing.PB) {
				//: each goroutine starts at its own place in the key set.
				i := int(offset.Add(131))
				for pb.Next() {
					if _, err := loader.Load(ctx, keys[i&1023], fill); err != nil {
						b.Error(err)
						return
					}
					i++
				}
			})
		})
	}
}

// BenchmarkLoad_MissParallel is a miss storm: every Load names a key never
// seen, so every call leads a fill through the store's singleflight group —
// the path #1035 took the group's lock off.
func BenchmarkLoad_MissParallel(b *testing.B) {
	var next atomic.Int64
	fill := func(context.Context) (corecache.EntryValue[string], error) {
		return corecache.EntryValue[string]{Value: "v"}, nil
	}
	store, err := svccache.NewMemory[string](svccache.MemoryConfig{MaxEntries: 1 << 16, Stripes: 8})
	if err != nil {
		b.Fatal(err)
	}
	loader := store.(corecache.Loader[string])
	ctx := b.Context()
	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			if _, err := loader.Load(ctx, strconv.FormatInt(next.Add(1), 10), fill); err != nil {
				b.Error(err)
				return
			}
		}
	})
}

// BenchmarkSet_Parallel overwrites seeded keys from every goroutine.
func BenchmarkSet_Parallel(b *testing.B) {
	keys := benchKeys(1024)
	for _, stripes := range benchStripes {
		b.Run("stripes="+strconv.Itoa(stripes), func(b *testing.B) {
			store := stripedStore(b, stripes, keys)
			ctx := b.Context()
			b.ReportAllocs()
			b.ResetTimer()
			var offset atomic.Int64
			b.RunParallel(func(pb *testing.PB) {
				//: each goroutine starts at its own place in the key set.
				i := int(offset.Add(131))
				for pb.Next() {
					if err := store.Set(ctx, keys[i&1023], corecache.EntryValue[string]{Value: "v"}); err != nil {
						b.Error(err)
						return
					}
					i++
				}
			})
		})
	}
}

// BenchmarkInvalidateTag_In100k_Striped is InvalidateTag_In100k at eight
// stripes: the walk takes eight locks in turn instead of one.
func BenchmarkInvalidateTag_In100k_Striped(b *testing.B) {
	store, err := svccache.NewMemory[string](svccache.MemoryConfig{MaxEntries: 200_000, Stripes: 8})
	if err != nil {
		b.Fatal(err)
	}
	ctx := b.Context()
	tagger := store.(corecache.Tagger)
	for i := 100; i < 100_000; i++ {
		if err := store.Set(ctx, strconv.Itoa(i), corecache.EntryValue[string]{Value: "v", Tags: []string{"bulk:" + strconv.Itoa(i%64)}}); err != nil {
			b.Fatal(err)
		}
	}
	seedHot := func() {
		for i := range 100 {
			if err := store.Set(ctx, strconv.Itoa(i), corecache.EntryValue[string]{Value: "v", Tags: []string{"hot"}}); err != nil {
				b.Fatal(err)
			}
		}
	}
	seedHot()
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		if removed, err := tagger.InvalidateTag(ctx, "hot"); err != nil || removed != 100 {
			b.Fatalf("InvalidateTag = (%d, %v)", removed, err)
		}
		b.StopTimer()
		seedHot()
		b.StartTimer()
	}
}

// BenchmarkFetchMany_16 reads 16 seeded keys in one call; ns/key is the
// per-key cost to set against Fetch_Hit (#840).
func BenchmarkFetchMany_16(b *testing.B) {
	keys := benchKeys(1024)
	store := stripedStore(b, 1, keys)
	bulk := store.(corecache.BulkFetcher[string])
	ctx := b.Context()
	b.ReportAllocs()
	b.ResetTimer()
	i := 0
	for b.Loop() {
		start := (i * 16) & 1023
		got, err := bulk.FetchMany(ctx, keys[start:start+16])
		if err != nil || len(got) != 16 {
			b.Fatalf("FetchMany = (%d keys, %v)", len(got), err)
		}
		i++
	}
	b.ReportMetric(float64(b.Elapsed().Nanoseconds())/float64(b.N*16), "ns/key")
}

// BenchmarkExpire_10kDeadIn100k sweeps a store of 100 000 entries of which
// 10 000 have passed their deadline (#1059); re-arming them is outside the
// timer.
func BenchmarkExpire_10kDeadIn100k(b *testing.B) {
	manual := clock.NewManualClock(time.Unix(0, 0))
	store, err := svccache.NewMemory[string](svccache.MemoryConfig{MaxEntries: 200_000, Clock: manual})
	if err != nil {
		b.Fatal(err)
	}
	ctx := b.Context()
	expirer := store.(corecache.Expirer)
	keys := benchKeys(100_000)
	for _, key := range keys[10_000:] {
		if err := store.Set(ctx, key, corecache.EntryValue[string]{Value: "v", TTL: 1000 * time.Hour}); err != nil {
			b.Fatal(err)
		}
	}
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		b.StopTimer()
		for _, key := range keys[:10_000] {
			if err := store.Set(ctx, key, corecache.EntryValue[string]{Value: "v", TTL: time.Second}); err != nil {
				b.Fatal(err)
			}
		}
		manual.Advance(2 * time.Second)
		b.StartTimer()
		if removed, err := expirer.Expire(ctx); err != nil || removed != 10_000 {
			b.Fatalf("Expire = (%d, %v)", removed, err)
		}
	}
}

// chainOfTwo builds a chain of two memory tiers, the configuration that
// isolates the chain's own cost — its promotion lock — from any tier's (#1121).
func chainOfTwo(b *testing.B) (chain, near, far corecache.Store[string]) {
	b.Helper()
	near, err := svccache.NewMemory[string](svccache.MemoryConfig{MaxEntries: 1 << 16})
	if err != nil {
		b.Fatal(err)
	}
	far, err = svccache.NewMemory[string](svccache.MemoryConfig{MaxEntries: 1 << 16})
	if err != nil {
		b.Fatal(err)
	}
	chain, err = svccache.NewChain(svccache.ChainConfig{}, near, far)
	if err != nil {
		b.Fatal(err)
	}
	return chain, near, far
}

// BenchmarkChain_NearHit is a hit in the near tier: the far tier and the
// promotion lock are never touched.
func BenchmarkChain_NearHit(b *testing.B) {
	chain, _, _ := chainOfTwo(b)
	ctx := b.Context()
	if err := chain.Set(ctx, "k", corecache.EntryValue[string]{Value: "v", TTL: time.Hour}); err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		if _, ok, _ := chain.Fetch(ctx, "k"); !ok {
			b.Fatal("miss")
		}
	}
}

// BenchmarkChain_FarHitPromote misses the near tier, hits the far one and
// promotes: the near entry is deleted outside the timer so every iteration
// pays the whole promotion.
func BenchmarkChain_FarHitPromote(b *testing.B) {
	chain, near, far := chainOfTwo(b)
	ctx := b.Context()
	if err := far.Set(ctx, "k", corecache.EntryValue[string]{Value: "v", TTL: time.Hour, Tags: []string{"t"}}); err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		b.StopTimer()
		_ = near.Delete(ctx, "k")
		b.StartTimer()
		if _, ok, _ := chain.Fetch(ctx, "k"); !ok {
			b.Fatal("miss")
		}
	}
}

// underPromotions runs write on every goroutine of the benchmark while one
// goroutine keeps promoting far hits, so every write announces itself against
// promotions in flight (#460's write lock).
func underPromotions(b *testing.B, write func(ctx context.Context, chain corecache.Store[string], key string) error) {
	b.Helper()
	chain, near, far := chainOfTwo(b)
	ctx := b.Context()
	keys := benchKeys(256)
	for _, key := range keys {
		if err := far.Set(ctx, key, corecache.EntryValue[string]{Value: "v", TTL: time.Hour}); err != nil {
			b.Fatal(err)
		}
	}
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			key := keys[i&255]
			_ = near.Delete(ctx, key)
			_, _, _ = chain.Fetch(ctx, key)
		}
	}()
	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		i := 0
		for pb.Next() {
			if err := write(ctx, chain, keys[i&255]); err != nil {
				b.Error(err)
				return
			}
			i++
		}
	})
	b.StopTimer()
	close(stop)
	<-done
}

// BenchmarkChain_SetUnderPromotions is a write racing far-hit readers: each
// Set takes the promotion lock for writing and waits out the promotions in
// flight. Run it with -cpu=1,8.
func BenchmarkChain_SetUnderPromotions(b *testing.B) {
	underPromotions(b, func(ctx context.Context, chain corecache.Store[string], key string) error {
		return chain.Set(ctx, key, corecache.EntryValue[string]{Value: "v", TTL: time.Hour})
	})
}

// BenchmarkChain_DeleteUnderPromotions is the same race for Delete.
func BenchmarkChain_DeleteUnderPromotions(b *testing.B) {
	underPromotions(b, func(ctx context.Context, chain corecache.Store[string], key string) error {
		return chain.Delete(ctx, key)
	})
}
