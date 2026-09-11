package queue_test

import (
	"context"
	"testing"
	"time"

	"github.com/kitsunium/sdk/internal/kernel/clock"

	corequeue "github.com/kitsunium/sdk/internal/core/queue"
	svcqueue "github.com/kitsunium/sdk/internal/service/queue"
)

// The shared policy numbers. They are named rather than inlined so a case
// that changes one says which one it changed.
const (
	testVisibility time.Duration = 10 * time.Second
	testRetryDelay time.Duration = 2 * time.Second
	testDeliveries int           = 3
)

// epoch anchors every ManualClock in this suite, so an instant printed in a
// failure is readable rather than being 1970 plus some nanoseconds.
var epoch = time.Date(2026, time.September, 10, 12, 0, 0, 0, time.UTC)

// brokerFactory builds one broker over an injected clock.
//
// The suite is written against this rather than against a constructor so the
// SAME cases run over both implementations. That is the whole reason the
// memory broker is worth having: a double nobody checks against the real
// thing is a double that has already drifted.
type brokerFactory struct {
	name string
	make func(t *testing.T, clk clock.Clock, policy corequeue.PolicyValue) corequeue.Broker
}

// bothBrokers is the table every conformance case ranges over.
func bothBrokers() []brokerFactory {
	return []brokerFactory{
		{name: "memory", make: makeMemory},
		{name: "file", make: makeFile},
	}
}

// makeMemory builds the in-process double.
func makeMemory(t *testing.T, clk clock.Clock, policy corequeue.PolicyValue) corequeue.Broker {
	t.Helper()
	broker, err := svcqueue.NewMemory(svcqueue.MemoryConfig{Policy: policy, Clock: clk})
	if err != nil {
		t.Fatalf("NewMemory() = %v, want nil", err)
	}
	return broker
}

// makeFile builds the durable broker over a per-test temporary directory.
func makeFile(t *testing.T, clk clock.Clock, policy corequeue.PolicyValue) corequeue.Broker {
	t.Helper()
	broker, err := svcqueue.NewFile(svcqueue.FileConfig{Dir: t.TempDir(), Policy: policy, Clock: clk})
	if err != nil {
		t.Fatalf("NewFile() = %v, want nil", err)
	}
	return broker
}

// defaultPolicy is the policy every case starts from.
func defaultPolicy() corequeue.PolicyValue {
	return corequeue.PolicyValue{
		VisibilityTimeout: testVisibility,
		RetryDelay:        testRetryDelay,
		MaxDeliveries:     testDeliveries,
	}
}

// publish is the assertive form of Broker.Publish.
func publish(t *testing.T, broker corequeue.Broker, payload string) corequeue.MessageValue {
	t.Helper()
	msg, err := broker.Publish(t.Context(), []byte(payload))
	if err != nil {
		t.Fatalf("Publish(%q) = %v, want nil", payload, err)
	}
	return msg
}

// receiveOne leases exactly one message and fails when the queue hands back a
// different number.
func receiveOne(t *testing.T, broker corequeue.Broker) corequeue.DeliveryValue {
	t.Helper()
	batch := receive(t, broker, 1)
	if len(batch) != 1 {
		t.Fatalf("Receive(1) returned %d deliveries, want 1", len(batch))
	}
	return batch[0]
}

// receive leases up to max messages.
func receive(t *testing.T, broker corequeue.Broker, max int) []corequeue.DeliveryValue {
	t.Helper()
	batch, err := broker.Receive(t.Context(), max)
	if err != nil {
		t.Fatalf("Receive(%d) = %v, want nil", max, err)
	}
	return batch
}

// receiveNone asserts the queue has nothing visible.
func receiveNone(t *testing.T, broker corequeue.Broker) {
	t.Helper()
	batch := receive(t, broker, 10)
	if len(batch) != 0 {
		t.Fatalf("Receive(10) returned %d deliveries, want 0", len(batch))
	}
}

// deadLetters reads the dead-letter store through the ADR 0039 capability
// sibling, asserting on the way that the broker actually has it.
func deadLetters(t *testing.T, broker corequeue.Broker, max int) []corequeue.DeadLetterValue {
	t.Helper()
	reader, ok := broker.(corequeue.DeadLetterReader)
	if !ok {
		t.Fatalf("%T does not implement queue.DeadLetterReader", broker)
	}
	dead, err := reader.DeadLetters(t.Context(), max)
	if err != nil {
		t.Fatalf("DeadLetters(%d) = %v, want nil", max, err)
	}
	return dead
}

// extender asserts the LeaseExtender capability and returns it.
func extender(t *testing.T, broker corequeue.Broker) corequeue.LeaseExtender {
	t.Helper()
	renew, ok := broker.(corequeue.LeaseExtender)
	if !ok {
		t.Fatalf("%T does not implement queue.LeaseExtender", broker)
	}
	return renew
}

// nack hands a message back and returns the broker's verdict.
func nack(
	t *testing.T, broker corequeue.Broker, receipt corequeue.ReceiptValue, cause error,
) corequeue.NackValue {
	t.Helper()
	verdict, err := broker.Nack(t.Context(), receipt, cause)
	if err != nil {
		t.Fatalf("Nack() = %v, want nil", err)
	}
	return verdict
}

// discard is a background context for the rare case that must outlive the
// test's own.
func discard() context.Context {
	return context.Background()
}
