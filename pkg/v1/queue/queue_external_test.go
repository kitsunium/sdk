package queue_test

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kitsunium/sdk/pkg/v1/errs"
	"github.com/kitsunium/sdk/pkg/v1/queue"
)

// facadePolicy is a policy every case here can carry.
func facadePolicy() queue.Policy {
	return queue.Policy{VisibilityTimeout: 30 * time.Second, MaxDeliveries: 3}
}

// TestTheFacadePublishesLeasesAndAcknowledges is the consumer's whole journey
// through the public surface, with no internal import anywhere.
func TestTheFacadePublishesLeasesAndAcknowledges(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"memory", "file"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			broker := facadeBroker(t, name)
			published, err := broker.Publish(t.Context(), []byte("work"))
			if err != nil {
				t.Fatalf("Publish() = %v, want nil", err)
			}
			batch, receiveErr := broker.Receive(t.Context(), 10)
			if receiveErr != nil || len(batch) != 1 {
				t.Fatalf("Receive() = %d, %v; want one delivery", len(batch), receiveErr)
			}
			if batch[0].Message.ID != published.ID || batch[0].Deliveries != 1 {
				t.Fatalf("delivery = %+v, want the published message on its first delivery", batch[0])
			}
			if ackErr := broker.Ack(t.Context(), batch[0].Lease.Receipt); ackErr != nil {
				t.Fatalf("Ack() = %v, want nil", ackErr)
			}
		})
	}
}

// TestBothFacadeBrokersCarryTheTwoCapabilitySiblings pins the ADR 0039 shape
// through the aliases: the port is frozen at four methods and the extras are
// reached by type assertion.
func TestBothFacadeBrokersCarryTheTwoCapabilitySiblings(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"memory", "file"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			broker := facadeBroker(t, name)
			if _, ok := broker.(queue.DeadLetterReader); !ok {
				t.Fatalf("%T does not implement queue.DeadLetterReader", broker)
			}
			if _, ok := broker.(queue.LeaseExtender); !ok {
				t.Fatalf("%T does not implement queue.LeaseExtender", broker)
			}
		})
	}
}

// TestTheFacadeRefusesTheZerosThatHaveTwoOppositeReadings keeps ADR 0031's
// refusing half visible from the public surface, where a consumer meets it.
func TestTheFacadeRefusesTheZerosThatHaveTwoOppositeReadings(t *testing.T) {
	t.Parallel()
	_, err := queue.NewMemory(queue.MemoryConfig{})
	code, coded := errs.CodeOf(queue.QueueMisconfigured)
	if !coded || !errs.HasCode(err, code) {
		t.Fatalf("NewMemory(zero policy) = %v, want QueueMisconfigured", err)
	}
}

// TestTheFacadeRefusesAConsumerThatHasNotAssertedIdempotence keeps the
// domain's one piece of ceremony visible from the public surface too.
func TestTheFacadeRefusesAConsumerThatHasNotAssertedIdempotence(t *testing.T) {
	t.Parallel()
	broker := facadeBroker(t, "memory")
	err := queue.Consume(t.Context(), broker, queue.ConsumerConfig{
		Handler: func(context.Context, queue.Delivery) error { return nil },
	})
	if err == nil {
		t.Fatal("Consume() = nil, want ConsumerMisconfigured for an unasserted handler")
	}
}

// TestTheFacadeConsumesAndStops runs the engine end to end through the public
// surface and checks that a cancelled consumer is a stopped one.
//
// GOROUTINE LIFECYCLE: one goroutine runs Consume, which owns its own workers
// and joins them before returning. It is ended by the cancel below and joined
// by running.Wait before result is read, so nothing outlives the test.
func TestTheFacadeConsumesAndStops(t *testing.T) {
	t.Parallel()
	broker := facadeBroker(t, "memory")
	if _, err := broker.Publish(t.Context(), []byte("work")); err != nil {
		t.Fatalf("Publish() = %v, want nil", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	var handled atomic.Int64
	var running sync.WaitGroup
	var result error
	running.Go(func() {
		result = queue.Consume(ctx, broker, queue.ConsumerConfig{
			Handler: func(context.Context, queue.Delivery) error {
				handled.Add(1)
				return nil
			},
			HandlerIsIdempotent: true, PollInterval: time.Millisecond,
		})
	})
	deadline := time.Now().Add(10 * time.Second)
	for handled.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	cancel()
	running.Wait()
	if result != nil {
		t.Fatalf("Consume() = %v, want nil for a cancelled consumer", result)
	}
	if handled.Load() == 0 {
		t.Fatal("the handler never ran")
	}
}

// facadeBroker builds one of the two public brokers.
func facadeBroker(t *testing.T, kind string) queue.Broker {
	t.Helper()
	if kind == "file" {
		broker, err := queue.NewFile(queue.FileConfig{Dir: t.TempDir(), Policy: facadePolicy()})
		if err != nil {
			t.Fatalf("NewFile() = %v, want nil", err)
		}
		return broker
	}
	broker, err := queue.NewMemory(queue.MemoryConfig{Policy: facadePolicy()})
	if err != nil {
		t.Fatalf("NewMemory() = %v, want nil", err)
	}
	return broker
}
