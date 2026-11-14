package singleflight

import (
	"context"
	"strconv"
	"sync/atomic"
	"testing"
)

// BenchmarkDo_DistinctKeysParallel_OneShard is Do_DistinctKeysParallel on ONE
// shard, driven through join and await as Do drives the shard a key hashes
// to, with the key's hash Do computes: #1035's step 1 alone — one blocking
// acquisition a call, a publication that does not wait for the lock —
// without step 2's sixteen shards. The same calls under the same shared context as
// Do_DistinctKeysParallel, so the lane judges it against the base's row of
// that name (a `cross` rule in bench-ab.conf).
func BenchmarkDo_DistinctKeysParallel_OneShard(b *testing.B) {
	var held shard[string, int]
	var next atomic.Int64
	ctx := b.Context()
	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			key := strconv.FormatInt(next.Add(1), 10)
			entry, joined := held.join(ctx, key, hashOf(key), func(context.Context) (int, error) { return 1, nil })
			if _, _, err := held.await(ctx, entry, joined); err != nil {
				b.Fatal(err)
			}
		}
	})
}
