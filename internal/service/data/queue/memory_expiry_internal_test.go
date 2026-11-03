package queue

import (
	"context"
	"errors"
	"testing"
	"time"

	corequeue "github.com/kitsunium/sdk/internal/core/data/queue"
)

// expiryBroker returns a memory broker holding one leased message, and the
// delivery that leased it.
func expiryBroker(t *testing.T) (*memoryBroker, corequeue.DeliveryValue) {
	t.Helper()
	ctx := context.Background()
	built, err := NewMemory(MemoryConfig{Policy: corequeue.PolicyValue{VisibilityTimeout: time.Minute, MaxDeliveries: 3}})
	if err != nil {
		t.Fatalf("NewMemory() = %v", err)
	}
	broker := built.(*memoryBroker)
	if _, err := broker.Publish(ctx, []byte("m")); err != nil {
		t.Fatalf("Publish() = %v", err)
	}
	batch, err := broker.Receive(ctx, 1)
	if err != nil || len(batch) != 1 {
		t.Fatalf("Receive() = %d deliveries, %v; want 1", len(batch), err)
	}
	return broker, batch[0]
}

// TestTheExpiryHeapHoldsOneEntryPerLease pins #1036: the memory broker's
// expiry heap holds exactly the leases in flight. An Extend re-keys the
// lease's entry instead of leaving the old one behind, and every call that
// ends a lease takes its entry out at once, rather than leaving a stale one
// for the reclaim scan to recognise by comparing deadlines.
func TestTheExpiryHeapHoldsOneEntryPerLease(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	cause := errors.New("handler failed")
	for name, end := range map[string]func(b *memoryBroker, receipt corequeue.ReceiptValue) error{
		"ack": func(b *memoryBroker, receipt corequeue.ReceiptValue) error { return b.Ack(ctx, receipt) },
		"nack": func(b *memoryBroker, receipt corequeue.ReceiptValue) error {
			_, err := b.Nack(ctx, receipt, cause)
			return err
		},
		"reject":  func(b *memoryBroker, receipt corequeue.ReceiptValue) error { return b.Reject(ctx, receipt, cause) },
		"release": func(b *memoryBroker, receipt corequeue.ReceiptValue) error { return b.Release(ctx, receipt) },
		"ack-many": func(b *memoryBroker, receipt corequeue.ReceiptValue) error {
			_, err := b.AckMany(ctx, []corequeue.ReceiptValue{receipt})
			return err
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			broker, delivery := expiryBroker(t)
			receipt := delivery.Lease.Receipt
			for range 100 {
				lease, err := broker.Extend(ctx, receipt, time.Minute)
				if err != nil {
					t.Fatalf("Extend() = %v", err)
				}
				receipt = lease.Receipt
			}
			if got := broker.expiry.Len(); got != 1 {
				t.Fatalf("after 100 extensions the expiry heap holds %d entries, want 1", got)
			}
			if err := end(broker, receipt); err != nil {
				t.Fatalf("%s = %v", name, err)
			}
			if got := broker.expiry.Len(); got != 0 {
				t.Fatalf("after %s the expiry heap holds %d entries, want 0", name, got)
			}
		})
	}
}

// TestPurgeLeavesNoDeadline pins Purge's half of #1036: no lease survives it,
// so no deadline does either.
func TestPurgeLeavesNoDeadline(t *testing.T) {
	t.Parallel()
	broker, _ := expiryBroker(t)
	if _, err := broker.Purge(context.Background()); err != nil {
		t.Fatalf("Purge() = %v", err)
	}
	if got := broker.expiry.Len(); got != 0 {
		t.Fatalf("after Purge the expiry heap holds %d entries, want 0", got)
	}
}
