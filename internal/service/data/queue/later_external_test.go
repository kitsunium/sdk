package queue_test

import (
	"errors"
	"math"
	"testing"
	"time"

	"github.com/kitsunium/sdk/internal/kernel/clock"
	"github.com/kitsunium/sdk/internal/kernel/errs"

	corequeue "github.com/kitsunium/sdk/internal/core/data/queue"
)

// The three siblings ADR 0183 added, on every broker: a nack that names its
// own delay, a message held back until an instant, and a publication made at
// most once per key over a window. Every case runs on a ManualClock and none
// sleeps.

// delayedNacker asserts the DelayedNacker capability and returns it.
func delayedNacker(t *testing.T, broker corequeue.Broker) corequeue.DelayedNacker {
	t.Helper()
	capability, ok := broker.(corequeue.DelayedNacker)
	if !ok {
		t.Fatalf("%T does not implement queue.DelayedNacker", broker)
	}
	return capability
}

// delayedPublisher asserts the DelayedPublisher capability and returns it.
func delayedPublisher(t *testing.T, broker corequeue.Broker) corequeue.DelayedPublisher {
	t.Helper()
	capability, ok := broker.(corequeue.DelayedPublisher)
	if !ok {
		t.Fatalf("%T does not implement queue.DelayedPublisher", broker)
	}
	return capability
}

// oncePublisher asserts the OncePublisher capability and returns it.
func oncePublisher(t *testing.T, broker corequeue.Broker) corequeue.OncePublisher {
	t.Helper()
	capability, ok := broker.(corequeue.OncePublisher)
	if !ok {
		t.Fatalf("%T does not implement queue.OncePublisher", broker)
	}
	return capability
}

// publishOnce is the assertive form of OncePublisher.PublishOnce.
func publishOnce(t *testing.T, broker corequeue.Broker, key, payload string, window time.Duration) (corequeue.MessageValue, bool) {
	t.Helper()
	message, published, err := oncePublisher(t, broker).PublishOnce(t.Context(), key, []byte(payload), window)
	if err != nil {
		t.Fatalf("PublishOnce(%q) = %v, want nil", key, err)
	}
	return message, published
}

// TestANackAfterHandsTheMessageBackAfterTheDelayItNames is the DelayedNacker
// contract: the message comes back delay after the nack — not after the
// policy's RetryDelay — with its delivery counted, as a nack counts it.
func TestANackAfterHandsTheMessageBackAfterTheDelayItNames(t *testing.T) {
	t.Parallel()
	const asked = 7 * time.Second
	for _, factory := range everyBroker() {
		t.Run(factory.name, func(t *testing.T) {
			t.Parallel()
			clk := clock.NewManualClock(epoch)
			broker := factory.make(t, clk, defaultPolicy())
			published := publish(t, broker, "work")
			delivery := receiveOne(t, broker)
			verdict, err := delayedNacker(t, broker).NackAfter(t.Context(), delivery.Lease.Receipt, errors.New("throttled"), asked)
			if err != nil {
				t.Fatalf("NackAfter() = %v, want nil", err)
			}
			if verdict.DeadLettered || verdict.Deliveries != 1 || !verdict.VisibleAt.Equal(epoch.Add(asked)) {
				t.Fatalf("NackAfter() = %+v, want delivery 1 visible at %v", verdict, epoch.Add(asked))
			}
			//: past the policy's delay, short of the one asked for.
			clk.Advance(testRetryDelay)
			receiveNone(t, broker)
			clk.Advance(asked - testRetryDelay)
			again := receiveOne(t, broker)
			if again.Message.ID != published.ID || again.Deliveries != 2 {
				t.Fatalf("redelivered %q as delivery %d, want %q as delivery 2", again.Message.ID, again.Deliveries, published.ID)
			}
		})
	}
}

// TestANackAfterOnTheLastAttemptDeadLettersWithItsCause pins that the delay
// does not buy an attempt: a message that has used MaxDeliveries is
// dead-lettered with the cause, as Nack would, and the delay goes unused.
func TestANackAfterOnTheLastAttemptDeadLettersWithItsCause(t *testing.T) {
	t.Parallel()
	for _, factory := range everyBroker() {
		t.Run(factory.name, func(t *testing.T) {
			t.Parallel()
			clk := clock.NewManualClock(epoch)
			policy := defaultPolicy()
			policy.MaxDeliveries = 1
			broker := factory.make(t, clk, policy)
			publish(t, broker, "work")
			delivery := receiveOne(t, broker)
			verdict, err := delayedNacker(t, broker).NackAfter(t.Context(), delivery.Lease.Receipt,
				corequeue.RetryAfter(nil, time.Hour), time.Hour)
			if err != nil || !verdict.DeadLettered || verdict.Deliveries != 1 || !verdict.VisibleAt.IsZero() {
				t.Fatalf("NackAfter(last attempt) = (%+v, %v), want dead-lettered", verdict, err)
			}
			dead := deadLetters(t, broker, 10)
			if len(dead) != 1 || dead[0].Reason != "RETRY_REQUESTED" || dead[0].Code != uint32(corequeue.CodeRetryRequested) {
				t.Fatalf("dead letters = %+v, want one recording RETRY_REQUESTED", dead)
			}
			clk.Advance(2 * time.Hour)
			receiveNone(t, broker)
		})
	}
}

// TestANackAfterReadsANegativeDelayAsNowAndRefusesOneNoBrokerRecords pins the
// two readings of an argument: a negative delay has the one reading "now", and
// a delay past what a durable broker records is QUEUE_MISCONFIGURED, refused
// before anything moves — the lease still holds, and acknowledges.
func TestANackAfterReadsANegativeDelayAsNowAndRefusesOneNoBrokerRecords(t *testing.T) {
	t.Parallel()
	for _, factory := range everyBroker() {
		t.Run(factory.name, func(t *testing.T) {
			t.Parallel()
			clk := clock.NewManualClock(epoch)
			broker := factory.make(t, clk, defaultPolicy())
			publish(t, broker, "work")
			delivery := receiveOne(t, broker)
			_, err := delayedNacker(t, broker).NackAfter(t.Context(), delivery.Lease.Receipt, nil, math.MaxInt64)
			if !errs.HasCode(err, corequeue.CodeQueueMisconfigured) {
				t.Fatalf("NackAfter(past 2262) = %v, want QUEUE_MISCONFIGURED", err)
			}
			verdict, err := delayedNacker(t, broker).NackAfter(t.Context(), delivery.Lease.Receipt, nil, -time.Hour)
			if err != nil || !verdict.VisibleAt.Equal(epoch) {
				t.Fatalf("NackAfter(-1h) = (%+v, %v), want visible now", verdict, err)
			}
			again := receiveOne(t, broker)
			if again.Deliveries != 2 {
				t.Fatalf("redelivered as delivery %d, want 2", again.Deliveries)
			}
			if err := broker.Ack(t.Context(), again.Lease.Receipt); err != nil {
				t.Fatalf("Ack() = %v, want nil", err)
			}
		})
	}
}

// TestANackAfterRefusesALapsedOrUnknownReceiptAndWakesAnIdleConsumer pins the
// refusals NackAfter shares with Nack, and that handing a message back closes
// the signal an idle consumer waits on.
func TestANackAfterRefusesALapsedOrUnknownReceiptAndWakesAnIdleConsumer(t *testing.T) {
	t.Parallel()
	for _, factory := range everyBroker() {
		t.Run(factory.name, func(t *testing.T) {
			t.Parallel()
			clk := clock.NewManualClock(epoch)
			broker := factory.make(t, clk, defaultPolicy())
			publish(t, broker, "work")
			delivery := receiveOne(t, broker)
			if _, err := delayedNacker(t, broker).NackAfter(t.Context(), "not-a-receipt", nil, time.Second); !errs.HasCode(err, corequeue.CodeUnknownReceipt) {
				t.Fatalf("NackAfter(forged) = %v, want UNKNOWN_RECEIPT", err)
			}
			signal := broker.(corequeue.Waker).Wake().Signal
			if _, err := delayedNacker(t, broker).NackAfter(t.Context(), delivery.Lease.Receipt, nil, time.Second); err != nil {
				t.Fatalf("NackAfter() = %v, want nil", err)
			}
			assertClosed(t, signal, "NackAfter")
			clk.Advance(time.Second)
			lapsed := receiveOne(t, broker)
			clk.Advance(testVisibility)
			if _, err := delayedNacker(t, broker).NackAfter(t.Context(), lapsed.Lease.Receipt, nil, time.Second); !errs.HasCode(err, corequeue.CodeLeaseExpired) {
				t.Fatalf("NackAfter(lapsed) = %v, want LEASE_EXPIRED", err)
			}
		})
	}
}

// TestAMessagePublishedAtAnInstantIsReceivedNoSooner is the DelayedPublisher
// contract: no consumer receives the message before its instant, an idle
// consumer is told how long to wait for it, a message visible earlier is
// delivered first, and its enqueue instant is the publication's.
func TestAMessagePublishedAtAnInstantIsReceivedNoSooner(t *testing.T) {
	t.Parallel()
	const later = time.Hour
	for _, factory := range everyBroker() {
		t.Run(factory.name, func(t *testing.T) {
			t.Parallel()
			clk := clock.NewManualClock(epoch)
			broker := factory.make(t, clk, defaultPolicy())
			signal := broker.(corequeue.Waker).Wake().Signal
			deferred, err := delayedPublisher(t, broker).PublishAt(t.Context(), []byte("later"), epoch.Add(later))
			if err != nil {
				t.Fatalf("PublishAt() = %v, want nil", err)
			}
			if !deferred.EnqueuedAt.Equal(epoch) || string(deferred.Payload) != "later" {
				t.Fatalf("PublishAt() = %+v, want enqueued at %v", deferred, epoch)
			}
			assertClosed(t, signal, "PublishAt")
			receiveNone(t, broker)
			if wake := broker.(corequeue.Waker).Wake(); !wake.Scheduled || wake.In != later {
				t.Fatalf("Wake() = %+v, want scheduled in %v", wake, later)
			}
			live, err := messageReader(t, broker).Messages(t.Context(), "", 10)
			if err != nil || len(live) != 1 || !live[0].VisibleAt.Equal(epoch.Add(later)) || live[0].Leased {
				t.Fatalf("Messages() = (%+v, %v), want one waiting until %v", live, err, epoch.Add(later))
			}
			clk.Advance(time.Minute)
			now := publish(t, broker, "now")
			clk.Advance(later)
			batch := receive(t, broker, 10)
			if len(batch) != 2 || batch[0].Message.ID != now.ID || batch[1].Message.ID != deferred.ID || batch[1].Deliveries != 1 {
				t.Fatalf("received %+v, want %q then %q as its first delivery", batch, now.ID, deferred.ID)
			}
			if !batch[1].Message.EnqueuedAt.Equal(epoch) {
				t.Fatalf("the deferred message's EnqueuedAt = %v, want %v", batch[1].Message.EnqueuedAt, epoch)
			}
		})
	}
}

// TestAnInstantAlreadyPastIsNowAndOneNoBrokerRecordsIsRefused pins the
// readings of PublishAt's instant: a past one — the zero time.Time included —
// is now, and one past 2262 is QUEUE_MISCONFIGURED with nothing published. A
// payload above the bound is refused as Publish refuses it.
func TestAnInstantAlreadyPastIsNowAndOneNoBrokerRecordsIsRefused(t *testing.T) {
	t.Parallel()
	for _, factory := range everyBroker() {
		t.Run(factory.name, func(t *testing.T) {
			t.Parallel()
			clk := clock.NewManualClock(epoch)
			policy := defaultPolicy()
			policy.MaxMessageBytes = 4
			broker := factory.make(t, clk, policy)
			for _, at := range []time.Time{{}, epoch.Add(-time.Hour), epoch} {
				if _, err := delayedPublisher(t, broker).PublishAt(t.Context(), []byte("now"), at); err != nil {
					t.Fatalf("PublishAt(%v) = %v, want nil", at, err)
				}
			}
			if batch := receive(t, broker, 10); len(batch) != 3 {
				t.Fatalf("received %d messages published for a past instant, want 3", len(batch))
			}
			if _, err := delayedPublisher(t, broker).PublishAt(t.Context(), []byte("x"), time.Unix(0, math.MaxInt64).Add(time.Nanosecond)); !errs.HasCode(err, corequeue.CodeQueueMisconfigured) {
				t.Fatalf("PublishAt(past 2262) = %v, want QUEUE_MISCONFIGURED", err)
			}
			if _, err := delayedPublisher(t, broker).PublishAt(t.Context(), []byte("too large"), epoch); !errs.HasCode(err, corequeue.CodeMessageTooLarge) {
				t.Fatalf("PublishAt(oversized) = %v, want MESSAGE_TOO_LARGE", err)
			}
			if live, err := messageReader(t, broker).Messages(t.Context(), "", 10); err != nil || len(live) != 3 {
				t.Fatalf("Messages() = (%d, %v), want the 3 leased and nothing refused", len(live), err)
			}
		})
	}
}

// TestAKeyIsPublishedOncePerWindow is the OncePublisher contract: within the
// window a second publication under the key publishes nothing and says so —
// even once the first message is acknowledged —, another key is untouched,
// and after the window the key publishes again and is reserved anew.
func TestAKeyIsPublishedOncePerWindow(t *testing.T) {
	t.Parallel()
	const window = 2 * time.Minute
	for _, factory := range everyBroker() {
		t.Run(factory.name, func(t *testing.T) {
			t.Parallel()
			clk := clock.NewManualClock(epoch)
			broker := factory.make(t, clk, defaultPolicy())
			first, published := publishOnce(t, broker, "order-1", "paid", window)
			if !published || first.ID == "" || !first.EnqueuedAt.Equal(epoch) || string(first.Payload) != "paid" {
				t.Fatalf("PublishOnce(first) = (%+v, %v), want a publication", first, published)
			}
			if duplicate, published := publishOnce(t, broker, "order-1", "paid again", window); published || duplicate.ID != "" {
				t.Fatalf("PublishOnce(inside the window) = (%+v, %v), want nothing published", duplicate, published)
			}
			if _, published := publishOnce(t, broker, "order-2", "other", window); !published {
				t.Fatal("PublishOnce(another key) published nothing")
			}
			//: the two publications, in either order — one instant, two keys.
			batch := receive(t, broker, 10)
			if len(batch) != 2 || (batch[0].Message.ID != first.ID && batch[1].Message.ID != first.ID) {
				t.Fatalf("received %+v, want order-1's first publication %q and order-2's", batch, first.ID)
			}
			for _, delivery := range batch {
				if err := broker.Ack(t.Context(), delivery.Lease.Receipt); err != nil {
					t.Fatalf("Ack() = %v", err)
				}
			}
			clk.Advance(window - time.Nanosecond)
			if _, published := publishOnce(t, broker, "order-1", "acknowledged, window running", window); published {
				t.Fatal("PublishOnce(acknowledged, inside the window) published again")
			}
			clk.Advance(time.Nanosecond)
			again, published := publishOnce(t, broker, "order-1", "after", window)
			if !published || again.ID == first.ID {
				t.Fatalf("PublishOnce(after the window) = (%+v, %v), want a new publication", again, published)
			}
			if _, published := publishOnce(t, broker, "order-1", "reserved anew", window); published {
				t.Fatal("PublishOnce(inside the new window) published again")
			}
			if last := receiveOne(t, broker); last.Message.ID != again.ID {
				t.Fatalf("received %q, want order-1's second publication %q", last.Message.ID, again.ID)
			}
			receiveNone(t, broker)
		})
	}
}

// TestProducersRacingOnOneKeyPublishOnce pins the exclusion: producers
// publishing under one key at once — goroutines here, processes the same way
// on the durable brokers — publish exactly one message between them, and every
// other one is told it was a duplicate.
func TestProducersRacingOnOneKeyPublishOnce(t *testing.T) {
	t.Parallel()
	const producers = 8
	for _, factory := range everyBroker() {
		t.Run(factory.name, func(t *testing.T) {
			t.Parallel()
			clk := clock.NewManualClock(epoch)
			broker := factory.make(t, clk, defaultPolicy())
			once := oncePublisher(t, broker)
			results := make(chan bool, producers)
			failures := make(chan error, producers)
			for range producers {
				go func() {
					_, published, err := once.PublishOnce(t.Context(), "k", []byte("x"), time.Minute)
					if err != nil {
						failures <- err
						return
					}
					results <- published
				}()
			}
			count := 0
			for range producers {
				select {
				case err := <-failures:
					t.Fatalf("PublishOnce() = %v, want nil", err)
				case published := <-results:
					if published {
						count++
					}
				}
			}
			if count != 1 {
				t.Fatalf("%d producers published, want exactly 1", count)
			}
			if batch := receive(t, broker, 10); len(batch) != 1 {
				t.Fatalf("the queue holds %d messages, want 1", len(batch))
			}
		})
	}
}

// TestPublishOnceRefusesWhatNoBrokerHonours pins the refusals every broker
// makes alike, each publishing nothing: an empty key, a window that is not
// positive — whose zero has opposite readings —, one whose end no durable
// broker records, and a payload above the bound.
func TestPublishOnceRefusesWhatNoBrokerHonours(t *testing.T) {
	t.Parallel()
	for _, factory := range everyBroker() {
		t.Run(factory.name, func(t *testing.T) {
			t.Parallel()
			clk := clock.NewManualClock(epoch)
			policy := defaultPolicy()
			policy.MaxMessageBytes = 4
			broker := factory.make(t, clk, policy)
			once := oncePublisher(t, broker)
			for name, call := range map[string]func() error{
				"empty key":   func() error { _, _, err := once.PublishOnce(t.Context(), "", []byte("x"), time.Minute); return err },
				"zero window": func() error { _, _, err := once.PublishOnce(t.Context(), "k", []byte("x"), 0); return err },
				"negative":    func() error { _, _, err := once.PublishOnce(t.Context(), "k", []byte("x"), -time.Minute); return err },
				"past 2262":   func() error { _, _, err := once.PublishOnce(t.Context(), "k", []byte("x"), math.MaxInt64); return err },
				"payload bound": func() error {
					_, _, err := once.PublishOnce(t.Context(), "k", []byte("too large"), time.Minute)
					return err
				},
			} {
				err := call()
				want := corequeue.CodeQueueMisconfigured
				if name == "payload bound" {
					want = corequeue.CodeMessageTooLarge
				}
				if !errs.HasCode(err, want) {
					t.Errorf("PublishOnce(%s) = %v, want %v", name, err, want)
				}
			}
			receiveNone(t, broker)
			//: and the key a refusal named is not reserved.
			if _, published := publishOnce(t, broker, "k", "x", time.Minute); !published {
				t.Fatal("a refused PublishOnce reserved its key")
			}
		})
	}
}
