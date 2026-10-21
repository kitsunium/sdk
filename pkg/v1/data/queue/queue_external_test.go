package queue_test

import (
	"context"
	"errors"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kitsunium/sdk/pkg/v1/data/queue"
	"github.com/kitsunium/sdk/pkg/v1/errs"
	"github.com/kitsunium/sdk/pkg/v1/proc"
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

// TestBothFacadeBrokersCarryTheCapabilitySiblings pins the ADR 0039 shape
// through the aliases: the port is frozen at four methods and the extras are
// reached by type assertion.
func TestBothFacadeBrokersCarryTheCapabilitySiblings(t *testing.T) {
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
			if _, ok := broker.(queue.Waker); !ok {
				t.Fatalf("%T does not implement queue.Waker", broker)
			}
		})
	}
}

// TestTheFacadeWakesAnIdleConsumerOnAPublication drives the engine through the
// public surface with a poll interval no test would sit out: only the wake can
// deliver the message in time.
//
// GOROUTINE LIFECYCLE: one goroutine runs Consume; it is ended by the cancel
// and joined by running.Wait before the test returns.
func TestTheFacadeWakesAnIdleConsumerOnAPublication(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"memory", "file"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			broker := facadeBroker(t, name)
			ctx, cancel := context.WithCancel(t.Context())
			handled := make(chan string, 1)
			var running sync.WaitGroup
			var result error
			running.Go(func() {
				result = queue.Consume(ctx, broker, queue.ConsumerConfig{
					Handler: func(_ context.Context, delivery queue.Delivery) error {
						handled <- string(delivery.Message.Payload)
						return nil
					},
					HandlerIsIdempotent: true, PollInterval: time.Hour,
				})
			})
			defer func() {
				cancel()
				running.Wait()
				if result != nil {
					t.Errorf("Consume() = %v, want nil for a cancelled consumer", result)
				}
			}()
			//: whether the consumer is already asleep or not, the message must
			//: arrive long before the hour.
			if _, err := broker.Publish(t.Context(), []byte("now")); err != nil {
				t.Fatalf("Publish() = %v, want nil", err)
			}
			select {
			case got := <-handled:
				if got != "now" {
					t.Errorf("handled %q, want the publication", got)
				}
			case <-time.After(10 * time.Second):
				t.Fatal("the idle consumer was not woken by the publication")
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

// TestTheFacadeHandsBackWhatAStopInterrupts spells kitsunium/sdk#474's names
// through the public surface alone: Releaser by type assertion on both
// brokers, DefaultSettleTimeout, and ConsumerConfig's SettleTimeout,
// DrainTimeout and AutoExtend on a consumer whose stop lands in the middle of
// its batch — the message it never started is receivable at once, uncounted.
func TestTheFacadeHandsBackWhatAStopInterrupts(t *testing.T) {
	t.Parallel()
	if queue.DefaultSettleTimeout != 5*time.Second {
		t.Fatalf("DefaultSettleTimeout = %v, want 5s", queue.DefaultSettleTimeout)
	}
	for _, name := range []string{"memory", "file"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			broker := facadeBroker(t, name)
			if _, ok := broker.(queue.Releaser); !ok {
				t.Fatalf("%T does not implement queue.Releaser", broker)
			}
			for _, payload := range []string{"first", "second"} {
				if _, err := broker.Publish(t.Context(), []byte(payload)); err != nil {
					t.Fatalf("Publish() = %v, want nil", err)
				}
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			err := queue.Consume(ctx, broker, queue.ConsumerConfig{
				Handler: func(context.Context, queue.Delivery) error {
					cancel()
					return nil
				},
				HandlerIsIdempotent: true, BatchSize: 2,
				SettleTimeout: time.Second, DrainTimeout: time.Second, AutoExtend: true,
			})
			if err != nil {
				t.Fatalf("Consume() = %v, want nil for a stopped consumer", err)
			}
			batch, err := broker.Receive(t.Context(), 10)
			if err != nil || len(batch) != 1 || batch[0].Deliveries != 1 {
				t.Fatalf("Receive() = %+v, %v; want the message never started, as its first delivery", batch, err)
			}
		})
	}
}

// TestTheFacadeSpellsTheSiblingsADR0183Added spells the three siblings and the
// mark through the public surface alone, on both brokers: a message published
// for later, a publication made once per key, and a nack after the delay a
// RetryAfter names.
func TestTheFacadeSpellsTheSiblingsADR0183Added(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"memory", "file"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			broker := facadeBroker(t, name)
			deferred, ok := broker.(queue.DelayedPublisher)
			if !ok {
				t.Fatalf("%T does not implement queue.DelayedPublisher", broker)
			}
			if _, err := deferred.PublishAt(t.Context(), []byte("later"), time.Now().Add(time.Hour)); err != nil {
				t.Fatalf("PublishAt() = %v, want nil", err)
			}
			if batch, err := broker.Receive(t.Context(), 10); err != nil || len(batch) != 0 {
				t.Fatalf("Receive() = (%d, %v), want nothing before its instant", len(batch), err)
			}
			once, ok := broker.(queue.OncePublisher)
			if !ok {
				t.Fatalf("%T does not implement queue.OncePublisher", broker)
			}
			for attempt, want := range []bool{true, false} {
				if _, published, err := once.PublishOnce(t.Context(), "k", []byte("now"), time.Minute); err != nil || published != want {
					t.Fatalf("PublishOnce() #%d = (%v, %v), want (%v, nil)", attempt+1, published, err, want)
				}
			}
			batch, err := broker.Receive(t.Context(), 10)
			if err != nil || len(batch) != 1 {
				t.Fatalf("Receive() = (%d, %v), want the one publication", len(batch), err)
			}
			marked := queue.RetryAfter(nil, time.Hour)
			if !errs.HasCode(marked, queue.CodeRetryRequested) || !errors.Is(marked, queue.RetryRequested) {
				t.Fatalf("RetryAfter(nil) = %v, want RETRY_REQUESTED", marked)
			}
			nacker, ok := broker.(queue.DelayedNacker)
			if !ok {
				t.Fatalf("%T does not implement queue.DelayedNacker", broker)
			}
			verdict, err := nacker.NackAfter(t.Context(), batch[0].Lease.Receipt, marked, time.Hour)
			if err != nil || verdict.DeadLettered || verdict.VisibleAt.Before(time.Now().Add(59*time.Minute)) {
				t.Fatalf("NackAfter() = (%+v, %v), want visible in an hour", verdict, err)
			}
		})
	}
}

// facadeBroker builds one of the two public brokers.
func facadeBroker(t *testing.T, kind string) queue.Broker {
	t.Helper()
	if kind == "file" {
		broker, err := queue.NewFile(queue.FileConfig{Dir: t.TempDir(), Policy: facadePolicy()})
		//: where vfs refuses the platform (Windows, illumos, Solaris) the
		//: durable broker's answer is that refusal, and a consumer matches it
		//: through the public names alone. Asserted, then skipped:
		//: the case needs a broker that does not exist there.
		if diskRefused() {
			//: the platform's refusal through the public names, and no broker.
			if !errors.Is(err, proc.UnsupportedPlatform) || broker != nil {
				t.Fatalf("NewFile on %s = (%v, %v), want (nil, proc.UnsupportedPlatform)", runtime.GOOS, broker, err)
			}
			t.Skipf("the durable broker is refused on %s by design (its atomic publisher, vfs, is Linux's, macOS's and the BSDs'); the refusal is asserted above", runtime.GOOS)
		}
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

// TestTheFacadeWritesADeadLetterAsItLies spells ADR 0178's names through the
// public surface alone: DeadLetterWriter by type assertion, a dead letter with
// no code written and read back as it was given, and InvalidDeadLetter, by its
// code, for one naming its own identifier.
func TestTheFacadeWritesADeadLetterAsItLies(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"memory", "file"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			broker := facadeBroker(t, name)
			writer, ok := broker.(queue.DeadLetterWriter)
			if !ok {
				t.Fatalf("%T does not implement queue.DeadLetterWriter", broker)
			}
			enqueued := time.Date(2026, time.October, 1, 9, 0, 0, 0, time.UTC)
			dead := queue.DeadLetter{
				Message:  queue.Message{Payload: []byte("moved"), EnqueuedAt: enqueued},
				FailedAt: enqueued.Add(time.Minute), Deliveries: 3, Reason: "LEASE_EXPIRED",
			}
			id, err := writer.PutDeadLetter(t.Context(), dead)
			if err != nil {
				t.Fatalf("PutDeadLetter() = %v, want nil", err)
			}
			read, readErr := broker.(queue.DeadLetterReader).DeadLetters(t.Context(), 10)
			if readErr != nil || len(read) != 1 {
				t.Fatalf("DeadLetters() = %d, %v; want the one written", len(read), readErr)
			}
			got := read[0]
			if got.Message.ID != id || string(got.Message.Payload) != "moved" || got.Deliveries != 3 ||
				got.Reason != "LEASE_EXPIRED" || got.Code != 0 || !got.FailedAt.Equal(dead.FailedAt) {
				t.Fatalf("read back %+v, want %+v under %q", got, dead, id)
			}
			dead.Message.ID = id
			if _, err := writer.PutDeadLetter(t.Context(), dead); !errs.HasCode(err, queue.CodeInvalidDeadLetter) {
				t.Fatalf("PutDeadLetter with an identifier = %v, want InvalidDeadLetter", err)
			}
		})
	}
}

// TestTheFacadeMovesALiveMessage spells ADR 0178's live-message names through
// the public surface alone: a message written with its count is listed by
// MessageReader as it was given, and InvalidMessage refuses one marked leased.
func TestTheFacadeMovesALiveMessage(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"memory", "file"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			broker := facadeBroker(t, name)
			writer, ok := broker.(queue.MessageWriter)
			if !ok {
				t.Fatalf("%T does not implement queue.MessageWriter", broker)
			}
			enqueued := time.Date(2026, time.October, 1, 9, 0, 0, 0, time.UTC)
			live := queue.LiveMessage{Message: queue.Message{Payload: []byte("moved"), EnqueuedAt: enqueued}, VisibleAt: enqueued, Deliveries: 2}
			id, err := writer.PutMessage(t.Context(), live)
			if err != nil {
				t.Fatalf("PutMessage() = %v, want nil", err)
			}
			listed, err := broker.(queue.MessageReader).Messages(t.Context(), "", 10)
			if err != nil || len(listed) != 1 || listed[0].Message.ID != id || listed[0].Deliveries != 2 || listed[0].Leased {
				t.Fatalf("Messages() = %+v, %v; want the one written under %q", listed, err, id)
			}
			live.Leased = true
			if _, err := writer.PutMessage(t.Context(), live); !errs.HasCode(err, queue.CodeInvalidMessage) || !errors.Is(err, queue.InvalidMessage) {
				t.Fatalf("PutMessage of a leased message = %v, want InvalidMessage", err)
			}
		})
	}
}

// diskRefused reports whether the durable broker's atomic publisher refuses
// this GOOS by design: every GOOS but Linux, macOS and the BSDs — Windows,
// illumos and Solaris among them. Spelled out rather than probed, so that a
// refusal on Linux, macOS or a BSD fails instead of skipping.
func diskRefused() bool {
	switch runtime.GOOS {
	case "linux", "android", "darwin", "ios", "freebsd", "openbsd", "netbsd", "dragonfly":
		return false
	}
	return true
}
