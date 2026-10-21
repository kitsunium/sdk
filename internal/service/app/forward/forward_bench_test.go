package forward_test

import (
	"context"
	"testing"

	corequeue "github.com/kitsunium/sdk/internal/core/data/queue"
	svcforward "github.com/kitsunium/sdk/internal/service/app/forward"
	svcqueue "github.com/kitsunium/sdk/internal/service/data/queue"
)

// benchBatch is the size of one batch a producer keeps: a few dozen spans
// encoded as OTLP/JSON.
const benchBatch int = 16 << 10

// BenchmarkEnqueue measures the producer's path — the bound, the policy and
// the publication — on the memory broker, which isolates the forwarder's own
// cost, and on the file broker, whose fsync is what a durable export pays
// instead of a round trip to the collector. Nothing consumes, so the bound is
// set above the run and every Enqueue publishes.
func BenchmarkEnqueue(b *testing.B) {
	batch := make([]byte, benchBatch)
	for _, broker := range []struct {
		name string
		make func(b *testing.B) corequeue.Broker
	}{
		{"memory", func(b *testing.B) corequeue.Broker {
			broker, err := svcqueue.NewMemory(svcqueue.MemoryConfig{Policy: testPolicy})
			if err != nil {
				b.Fatal(err)
			}
			return broker
		}},
		{"file", func(b *testing.B) corequeue.Broker {
			broker, err := svcqueue.NewFile(svcqueue.FileConfig{Dir: b.TempDir(), Policy: testPolicy})
			if err != nil {
				b.Fatal(err)
			}
			return broker
		}},
	} {
		b.Run(broker.name, func(b *testing.B) {
			forwarder, err := svcforward.New(context.Background(), svcforward.Config{
				Broker:     broker.make(b),
				Send:       func(context.Context, []byte) error { return nil },
				Retryable:  retryable,
				MaxBatches: 1 << 30,
				MaxBytes:   1 << 50,
			})
			if err != nil {
				b.Fatal(err)
			}
			ctx := context.Background()
			b.SetBytes(int64(benchBatch))
			b.ReportAllocs()
			for b.Loop() {
				if err := forwarder.Enqueue(ctx, batch); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// BenchmarkEnqueueDropOldest measures the full queue's path under the zero
// policy: every Enqueue takes the oldest batch out — a Receive and an Ack —
// before it publishes, on the memory broker.
func BenchmarkEnqueueDropOldest(b *testing.B) {
	broker, err := svcqueue.NewMemory(svcqueue.MemoryConfig{Policy: testPolicy})
	if err != nil {
		b.Fatal(err)
	}
	forwarder, err := svcforward.New(context.Background(), svcforward.Config{
		Broker:     broker,
		Send:       func(context.Context, []byte) error { return nil },
		Retryable:  retryable,
		MaxBatches: 64,
	})
	if err != nil {
		b.Fatal(err)
	}
	ctx := context.Background()
	batch := make([]byte, benchBatch)
	for range 64 {
		if err := forwarder.Enqueue(ctx, batch); err != nil {
			b.Fatal(err)
		}
	}
	b.SetBytes(int64(benchBatch))
	b.ReportAllocs()
	for b.Loop() {
		if err := forwarder.Enqueue(ctx, batch); err != nil {
			b.Fatal(err)
		}
	}
}
