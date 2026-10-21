package queue_test

import (
	"testing"
	"time"

	"github.com/kitsunium/sdk/internal/kernel/clock"
	"github.com/kitsunium/sdk/internal/kernel/errs"

	corequeue "github.com/kitsunium/sdk/internal/core/data/queue"
)

// releaser asserts the Releaser capability and returns it.
func releaser(t *testing.T, broker corequeue.Broker) corequeue.Releaser {
	t.Helper()
	capability, ok := broker.(corequeue.Releaser)
	if !ok {
		t.Fatalf("%T does not implement queue.Releaser", broker)
	}
	return capability
}

// TestAReleasedMessageIsReceivableAtOnceWithItsCountGivenBack is the Releaser
// contract on every broker (kitsunium/sdk#474): the message is receivable the
// instant it is released — no retry delay, no lease to wait out — and its next
// delivery carries the count this one did, so a lease the consumer never used
// costs the message nothing. On its last attempt it is still not
// dead-lettered.
func TestAReleasedMessageIsReceivableAtOnceWithItsCountGivenBack(t *testing.T) {
	t.Parallel()
	for _, factory := range everyBroker() {
		t.Run(factory.name, func(t *testing.T) {
			t.Parallel()
			clk := clock.NewManualClock(epoch)
			policy := defaultPolicy()
			policy.MaxDeliveries = 1
			broker := factory.make(t, clk, policy)
			published := publish(t, broker, "work")
			for round := range 3 {
				delivery := receiveOne(t, broker)
				if delivery.Deliveries != 1 || delivery.Message.ID != published.ID || string(delivery.Message.Payload) != "work" {
					t.Fatalf("round %d delivered %+v as delivery %d, want %q as delivery 1",
						round, delivery.Message, delivery.Deliveries, published.ID)
				}
				if err := releaser(t, broker).Release(t.Context(), delivery.Lease.Receipt); err != nil {
					t.Fatalf("round %d: Release() = %v, want nil", round, err)
				}
			}
			if dead := deadLetters(t, broker, 10); len(dead) != 0 {
				t.Fatalf("a message released on its last attempt was dead-lettered: %+v", dead)
			}
			//: and it is still there to be acknowledged.
			delivery := receiveOne(t, broker)
			if err := broker.Ack(t.Context(), delivery.Lease.Receipt); err != nil {
				t.Fatalf("Ack() after the releases = %v, want nil", err)
			}
			receiveNone(t, broker)
		})
	}
}

// TestAReleasedReceiptIsVoid pins that Release ends the lease it names: the
// same receipt then acknowledges, releases and renews nothing, and is refused
// LEASE_EXPIRED as any receipt whose lease is gone.
func TestAReleasedReceiptIsVoid(t *testing.T) {
	t.Parallel()
	for _, factory := range everyBroker() {
		t.Run(factory.name, func(t *testing.T) {
			t.Parallel()
			clk := clock.NewManualClock(epoch)
			broker := factory.make(t, clk, defaultPolicy())
			publish(t, broker, "work")
			delivery := receiveOne(t, broker)
			if err := releaser(t, broker).Release(t.Context(), delivery.Lease.Receipt); err != nil {
				t.Fatalf("Release() = %v, want nil", err)
			}
			if err := broker.Ack(t.Context(), delivery.Lease.Receipt); !errs.HasCode(err, corequeue.CodeLeaseExpired) {
				t.Errorf("Ack(released receipt) = %v, want LEASE_EXPIRED", err)
			}
			if err := releaser(t, broker).Release(t.Context(), delivery.Lease.Receipt); !errs.HasCode(err, corequeue.CodeLeaseExpired) {
				t.Errorf("Release(released receipt) = %v, want LEASE_EXPIRED", err)
			}
			if _, err := extender(t, broker).Extend(t.Context(), delivery.Lease.Receipt, time.Minute); !errs.HasCode(err, corequeue.CodeLeaseExpired) {
				t.Errorf("Extend(released receipt) = %v, want LEASE_EXPIRED", err)
			}
			//: released once, so receivable once, as its first delivery.
			if again := receiveOne(t, broker); again.Deliveries != 1 {
				t.Fatalf("after the refusals, the message came back as delivery %d, want 1", again.Deliveries)
			}
		})
	}
}

// TestReleaseRefusesALapsedOrUnknownReceiptAndMovesNothing pins the two
// refusals Release shares with Ack: a lapsed lease is LEASE_EXPIRED and gives
// nothing back — the lapse already counted the delivery, and the message
// comes back with it — and a receipt the broker never minted is
// UNKNOWN_RECEIPT.
func TestReleaseRefusesALapsedOrUnknownReceiptAndMovesNothing(t *testing.T) {
	t.Parallel()
	for _, factory := range everyBroker() {
		t.Run(factory.name, func(t *testing.T) {
			t.Parallel()
			clk := clock.NewManualClock(epoch)
			broker := factory.make(t, clk, defaultPolicy())
			publish(t, broker, "work")
			delivery := receiveOne(t, broker)
			clk.Advance(testVisibility)
			if err := releaser(t, broker).Release(t.Context(), delivery.Lease.Receipt); !errs.HasCode(err, corequeue.CodeLeaseExpired) {
				t.Fatalf("Release(lapsed) = %v, want LEASE_EXPIRED", err)
			}
			if again := receiveOne(t, broker); again.Deliveries != 2 {
				t.Fatalf("after a lapsed lease the message came back as delivery %d, want 2", again.Deliveries)
			}
			if err := releaser(t, broker).Release(t.Context(), "not-a-receipt"); !errs.HasCode(err, corequeue.CodeUnknownReceipt) {
				t.Fatalf("Release(forged) = %v, want UNKNOWN_RECEIPT", err)
			}
		})
	}
}

// TestReleaseWakesAnIdleConsumer pins that a release closes the signal an idle
// consumer waits on, as a nack that hands a message back does: the message is
// receivable now, and nobody should poll to find out.
func TestReleaseWakesAnIdleConsumer(t *testing.T) {
	t.Parallel()
	for _, factory := range everyBroker() {
		t.Run(factory.name, func(t *testing.T) {
			t.Parallel()
			clk := clock.NewManualClock(epoch)
			broker := factory.make(t, clk, defaultPolicy())
			publish(t, broker, "work")
			delivery := receiveOne(t, broker)
			waker, ok := broker.(corequeue.Waker)
			if !ok {
				t.Fatalf("%T does not implement queue.Waker", broker)
			}
			signal := waker.Wake().Signal
			if err := releaser(t, broker).Release(t.Context(), delivery.Lease.Receipt); err != nil {
				t.Fatalf("Release() = %v, want nil", err)
			}
			select {
			case <-signal:
			default:
				t.Fatal("Release did not wake an idle consumer")
			}
		})
	}
}
