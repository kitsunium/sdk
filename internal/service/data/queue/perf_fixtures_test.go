//go:build !race

// The fixture of the queue's performance contract (design/sdk.yaml,
// budgets; issue #1048): it sets one call up and returns it, and the
// gen_perf_test.go kit gen writes beside this file counts its allocations
// against its budget. Race off, since the race detector allocates on its own.
package queue_test

import (
	"context"
	"testing"
	"time"

	corequeue "github.com/kitsunium/sdk/internal/core/data/queue"
	svcqueue "github.com/kitsunium/sdk/internal/service/data/queue"
)

// perfConsumeRoundTrip is Consume's fixture: one message published to the
// memory broker and handled by a running Consume — the wake, the handler's
// goroutine and its watch, and the acknowledgement included, which is what
// BenchmarkConsume/mem/plain measures. Consume runs for the life of the
// test and is stopped at its end.
func perfConsumeRoundTrip(tb testing.TB) func() {
	broker, err := svcqueue.NewMemory(svcqueue.MemoryConfig{Policy: benchPolicy()})
	if err != nil {
		tb.Fatalf("NewMemory() = %v", err)
	}
	handled := make(chan struct{}, 1)
	cfg := svcqueue.ConsumerConfig{
		Handler: func(context.Context, corequeue.DeliveryValue) error {
			handled <- struct{}{}
			return nil
		},
		HandlerIsIdempotent: true, PollInterval: time.Hour,
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- svcqueue.Consume(ctx, broker, cfg) }()
	tb.Cleanup(func() {
		cancel()
		if consumeErr := <-done; consumeErr != nil {
			tb.Errorf("Consume() = %v", consumeErr)
		}
	})
	payload := make([]byte, 64)
	return func() {
		if _, publishErr := broker.Publish(ctx, payload); publishErr != nil {
			tb.Fatalf("Publish() = %v", publishErr)
		}
		<-handled
	}
}
