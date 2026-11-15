package cache_test

import (
	"slices"
	"testing"

	corecache "github.com/kitsunium/sdk/internal/core/data/cache"
)

// BenchmarkFetchMany_16_1M is FetchMany_16 at the scale of Fetch_Hit_1M
// (#840): sixteen keys a call out of the same million-entry store, drawn from
// the same order computed beforehand, sixteen consecutive indexes a call, so
// each key costs the cache misses Fetch_Hit_1M's one key costs, and the
// lane's rule reads this row's ns/op over sixteen of that one's. A row of the
// bench A/B lane's scale mode, a few hundred megabytes: never on a shared
// machine. It lives apart from scale_bench_test.go, whose rows the base the
// lane compares against compiles too: FetchMany's shape is newer than that
// base, and a file the base cannot compile is left out of its binary whole.
func BenchmarkFetchMany_16_1M(b *testing.B) {
	store, keys, order := scaleStore(b)
	bulk, ok := store.(corecache.BulkFetcher[string])
	if !ok {
		b.Fatal("the store does not implement BulkFetcher")
	}
	ctx := b.Context()
	batch := make([]string, 16)
	values, found := make([]string, 0, 16), make([]bool, 0, 16)
	b.ReportAllocs()
	b.ResetTimer()
	i := 0
	for b.Loop() {
		//: the batch's keys, read as Fetch_Hit_1M reads its one.
		for at := range batch {
			batch[at] = keys[order[(i+at)&(scaleOrder-1)]]
		}
		var err error
		values, found, err = bulk.FetchMany(ctx, batch, values[:0], found[:0])
		if err != nil || len(found) != 16 || slices.Contains(found, false) {
			b.Fatalf("FetchMany = (%d answers, %v)", len(found), err)
		}
		scaleSink = values[0]
		i += 16
	}
	b.ReportMetric(float64(b.Elapsed().Nanoseconds())/float64(b.N*16), "ns/key")
}
