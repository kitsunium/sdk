package queue_test

import (
	"context"
	"errors"
	"strconv"
	"testing"
	"time"

	corequeue "github.com/kitsunium/sdk/internal/core/queue"
	svcqueue "github.com/kitsunium/sdk/internal/service/queue"
)

// benchPolicy is deliberately generous: nothing in these benchmarks should
// hit a visibility timeout or a dead-letter path by accident, because that
// would measure the recovery machinery instead of the verb under test.
func benchPolicy() corequeue.PolicyValue {
	return corequeue.PolicyValue{
		VisibilityTimeout: time.Hour, MaxDeliveries: 1_000_000, MaxMessageBytes: 1 << 24,
	}
}

// benchSizes are the payloads every size-sensitive benchmark runs over. They
// answer the one question that decides how a caller uses a durable queue:
// does the cost track the payload, or is it fixed?
var benchSizes = []int{64, 4096, 65536}

// benchBroker builds one broker of the named kind on the REAL clock.
func benchBroker(b *testing.B, kind string) corequeue.Broker {
	b.Helper()
	if kind == "file" {
		broker, err := svcqueue.NewFile(svcqueue.FileConfig{Dir: b.TempDir(), Policy: benchPolicy()})
		if err != nil {
			b.Fatalf("NewFile() = %v", err)
		}
		return broker
	}
	broker, err := svcqueue.NewMemory(svcqueue.MemoryConfig{Policy: benchPolicy()})
	if err != nil {
		b.Fatalf("NewMemory() = %v", err)
	}
	return broker
}

// BenchmarkPublish measures the enqueue, which is where a durable queue spends
// its guarantee: two device flushes on the file broker, one map insert on the
// memory one.
func BenchmarkPublish(b *testing.B) {
	for _, kind := range []string{"mem", "file"} {
		for _, size := range benchSizes {
			b.Run(kind+"/"+strconv.Itoa(size), func(b *testing.B) {
				broker := benchBroker(b, kind)
				payload := make([]byte, size)
				ctx := context.Background()
				b.SetBytes(int64(size))
				b.ReportAllocs()
				b.ResetTimer()
				for range b.N {
					if _, err := broker.Publish(ctx, payload); err != nil {
						b.Fatalf("Publish() = %v", err)
					}
				}
			})
		}
	}
}

// BenchmarkReceiveBatch measures how much a BATCH amortises. Both brokers
// hold a constant backlog, and each iteration leases a batch and hands it
// straight back, so the queue's depth is the same on the last iteration as on
// the first.
//
// The Nack is INSIDE the timed region on purpose. Stopping the timer around
// it was tried and rejected: b.StopTimer/StartTimer cost more per iteration
// than a whole in-memory Receive, so the memory numbers measured the
// instrumentation rather than the verb. BenchmarkNack reports the nack's own
// cost, and subtracting it is the honest way to read this.
func BenchmarkReceiveBatch(b *testing.B) {
	const backlog int = 256
	for _, kind := range []string{"mem", "file"} {
		for _, batch := range []int{1, 16} {
			b.Run(kind+"/batch"+strconv.Itoa(batch), func(b *testing.B) {
				broker := benchBroker(b, kind)
				ctx := context.Background()
				prime(b, broker, backlog, 64)
				b.ReportAllocs()
				b.ResetTimer()
				for range b.N {
					leaseAndReturn(ctx, b, broker, batch)
				}
			})
		}
	}
}

// leaseAndReturn leases up to max messages and immediately hands them back,
// so the caller's backlog is unchanged.
func leaseAndReturn(ctx context.Context, b *testing.B, broker corequeue.Broker, max int) {
	b.Helper()
	deliveries, err := broker.Receive(ctx, max)
	if err != nil {
		b.Fatalf("Receive() = %v", err)
	}
	for _, delivery := range deliveries {
		if _, nackErr := broker.Nack(ctx, delivery.Lease.Receipt, nil); nackErr != nil {
			b.Fatalf("Nack() = %v", nackErr)
		}
	}
}

// BenchmarkAck measures the acknowledgement on its own: one unlink against
// one map delete, and deliberately no flush on either.
func BenchmarkAck(b *testing.B) {
	for _, kind := range []string{"mem", "file"} {
		b.Run(kind, func(b *testing.B) {
			broker := benchBroker(b, kind)
			ctx := context.Background()
			prime(b, broker, b.N, 64)
			receipts := leaseAll(b, broker, b.N)
			b.ReportAllocs()
			b.ResetTimer()
			for index := range b.N {
				if err := broker.Ack(ctx, receipts[index]); err != nil {
					b.Fatalf("Ack() = %v", err)
				}
			}
		})
	}
}

// BenchmarkRoundTrip is the actionable end-to-end number: one message
// published, leased and acknowledged. It is what a caller sizing a worker
// pool should read, because it is the only figure that contains every syscall
// a message actually costs.
func BenchmarkRoundTrip(b *testing.B) {
	for _, kind := range []string{"mem", "file"} {
		b.Run(kind, func(b *testing.B) {
			broker := benchBroker(b, kind)
			ctx := context.Background()
			payload := make([]byte, 64)
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				if _, err := broker.Publish(ctx, payload); err != nil {
					b.Fatalf("Publish() = %v", err)
				}
				batch, err := broker.Receive(ctx, 1)
				if err != nil || len(batch) != 1 {
					b.Fatalf("Receive() = %d, %v", len(batch), err)
				}
				if ackErr := broker.Ack(ctx, batch[0].Lease.Receipt); ackErr != nil {
					b.Fatalf("Ack() = %v", ackErr)
				}
			}
		})
	}
}

// BenchmarkNack measures handing a message back for a retry: one rename
// against one slice insert.
func BenchmarkNack(b *testing.B) {
	cause := errors.New("bench failure") //nolint:err113
	for _, kind := range []string{"mem", "file"} {
		b.Run(kind, func(b *testing.B) {
			broker := benchBroker(b, kind)
			ctx := context.Background()
			prime(b, broker, b.N, 64)
			receipts := leaseAll(b, broker, b.N)
			b.ReportAllocs()
			b.ResetTimer()
			for index := range b.N {
				if _, err := broker.Nack(ctx, receipts[index], cause); err != nil {
					b.Fatalf("Nack() = %v", err)
				}
			}
		})
	}
}

// BenchmarkReceiveBacklog is the durable broker's KNOWN SCALING LIMIT,
// measured rather than asserted: a Receive reads and sorts the WHOLE queued
// directory, so a deep backlog costs more per lease than a shallow one.
//
// The memory broker is measured beside it as the control. Its ready list is
// ordered at insertion and its lease deadlines are in a min-heap, so it does
// not scale with depth at all — which is exactly the shape of the difference
// a reader sizing a durable queue needs to see.
//
// As in BenchmarkReceiveBatch, the Nack that restores the backlog is inside
// the timed region and its own cost is reported separately.
func BenchmarkReceiveBacklog(b *testing.B) {
	for _, kind := range []string{"mem", "file"} {
		for _, depth := range []int{10, 100, 1000} {
			b.Run(kind+"/depth"+strconv.Itoa(depth), func(b *testing.B) {
				broker := benchBroker(b, kind)
				ctx := context.Background()
				prime(b, broker, depth, 64)
				b.ReportAllocs()
				b.ResetTimer()
				for range b.N {
					leaseAndReturn(ctx, b, broker, 1)
				}
			})
		}
	}
}

// prime publishes count messages of the given size, off the clock.
func prime(b *testing.B, broker corequeue.Broker, count, size int) {
	b.Helper()
	b.StopTimer()
	payload := make([]byte, size)
	ctx := context.Background()
	for range count {
		if _, err := broker.Publish(ctx, payload); err != nil {
			b.Fatalf("Publish() = %v", err)
		}
	}
	b.StartTimer()
}

// leaseAll leases count messages and returns their receipts, off the clock.
func leaseAll(b *testing.B, broker corequeue.Broker, count int) []corequeue.ReceiptValue {
	b.Helper()
	b.StopTimer()
	ctx := context.Background()
	receipts := make([]corequeue.ReceiptValue, 0, count)
	for len(receipts) < count {
		batch, err := broker.Receive(ctx, count-len(receipts))
		if err != nil {
			b.Fatalf("Receive() = %v", err)
		}
		if len(batch) == 0 {
			b.Fatalf("primed %d messages but could only lease %d", count, len(receipts))
		}
		for _, delivery := range batch {
			receipts = append(receipts, delivery.Lease.Receipt)
		}
	}
	b.StartTimer()
	return receipts
}
