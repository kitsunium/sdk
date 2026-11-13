package stream_test

import (
	"context"
	"strconv"
	"testing"

	corestream "github.com/kitsunium/sdk/internal/core/data/stream"
	svcstream "github.com/kitsunium/sdk/internal/service/data/stream"
)

// BenchmarkSubscribe measures what the subscriber adds to a Read per record
// delivered on the memory engine: b.N records of 64 B are appended first,
// then one group reads them all in batches of 1, 16 or 256, each batch
// committed after its handler (CommitAfterHandler). The engine's own Read and
// Commit are inside the figure; the Append is not.
func BenchmarkSubscribe(b *testing.B) {
	for _, batch := range []int{1, 16, 256} {
		b.Run("batch="+strconv.Itoa(batch), func(b *testing.B) {
			s := benchStream(b, corestream.PolicyValue{})
			records := benchBatch(256, 64)
			for appended := 0; appended < b.N; appended += len(records) {
				if _, err := s.Append(b.Context(), records[:min(len(records), b.N-appended)]); err != nil {
					b.Fatalf("Append() = %v", err)
				}
			}
			ctx, cancel := context.WithCancel(b.Context())
			defer cancel()
			delivered := 0
			handler := func(_ context.Context, entries []corestream.EntryValue) error {
				delivered += len(entries)
				if delivered >= b.N {
					cancel()
				}
				return nil
			}
			b.SetBytes(64)
			b.ReportAllocs()
			b.ResetTimer()
			err := svcstream.Subscribe(ctx, s, svcstream.SubscriberConfig{
				Group: "bench", Handler: handler, HandlerIsIdempotent: true, BatchSize: batch,
			})
			b.StopTimer()
			if err != nil || delivered < b.N {
				b.Fatalf("Subscribe() = %v after %d of %d records", err, delivered, b.N)
			}
		})
	}
}
