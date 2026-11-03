//go:build integration

// Package sql_test — what the SQL queue broker's three verbs cost on each
// real engine (issue #1048, ADR 0151's deferral): a Publish, a Receive of
// one due message, and an Ack. BENCH.md in this directory has the numbers
// and how to refresh them.
package sql_test

import (
	"testing"
	"time"

	"github.com/kitsunium/sdk/pkg/v1/data/queue"
)

// BenchmarkSQLQueue measures each verb once per iteration on each engine:
// Publish (one INSERT), Receive of one message from a backlog of b.N (the
// probe, then the lease's transaction — one UPDATE … RETURNING on
// PostgreSQL), and Ack (one DELETE). The backlog and the leases are made off
// the clock.
func BenchmarkSQLQueue(b *testing.B) {
	for name, open := range benchEngines {
		b.Run(name, func(b *testing.B) {
			e := open(b)
			policy := queue.Policy{VisibilityTimeout: time.Hour, MaxDeliveries: 1_000_000}
			payload := make([]byte, 64)
			b.Run("Publish", func(b *testing.B) {
				fx := openQueue(b, e, queue.SQLConfig{Policy: policy})
				b.ReportAllocs()
				for b.Loop() {
					if _, err := fx.broker.Publish(b.Context(), payload); err != nil {
						b.Fatal(err)
					}
				}
			})
			b.Run("Receive", func(b *testing.B) {
				fx := openQueue(b, e, queue.SQLConfig{Policy: policy})
				primeQueue(b, fx.broker, b.N, payload)
				b.ReportAllocs()
				b.ResetTimer()
				for range b.N {
					if batch, err := fx.broker.Receive(b.Context(), 1); err != nil || len(batch) != 1 {
						b.Fatalf("Receive(1) = %d, %v", len(batch), err)
					}
				}
			})
			b.Run("Ack", func(b *testing.B) {
				fx := openQueue(b, e, queue.SQLConfig{Policy: policy})
				primeQueue(b, fx.broker, b.N, payload)
				receipts := make([]queue.Receipt, 0, b.N)
				for len(receipts) < b.N {
					batch, err := fx.broker.Receive(b.Context(), b.N-len(receipts))
					if err != nil || len(batch) == 0 {
						b.Fatalf("Receive() = %d, %v", len(batch), err)
					}
					for _, delivery := range batch {
						receipts = append(receipts, delivery.Lease.Receipt)
					}
				}
				b.ReportAllocs()
				b.ResetTimer()
				for index := range b.N {
					if err := fx.broker.Ack(b.Context(), receipts[index]); err != nil {
						b.Fatal(err)
					}
				}
			})
		})
	}
}

// primeQueue publishes count messages in batches, off the clock.
func primeQueue(b *testing.B, broker queue.Broker, count int, payload []byte) {
	b.Helper()
	b.StopTimer()
	defer b.StartTimer()
	batch := make([][]byte, 0, 100)
	for published := 0; published < count; published += len(batch) {
		batch = batch[:0]
		for range min(100, count-published) {
			batch = append(batch, payload)
		}
		if _, err := broker.(queue.BatchPublisher).PublishMany(b.Context(), batch); err != nil {
			b.Fatal(err)
		}
	}
}
