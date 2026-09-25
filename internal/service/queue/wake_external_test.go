package queue_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kitsunium/sdk/internal/kernel/clock"

	corequeue "github.com/kitsunium/sdk/internal/core/queue"
	svcqueue "github.com/kitsunium/sdk/internal/service/queue"
)

// idlePoll is a poll interval no test in this file ever lets elapse: a
// consumer that only finds work by polling would sit out every one of them.
const idlePoll time.Duration = time.Hour

// consumer runs Consume on its own goroutine until the test ends, and returns
// the channel every delivery's payload and count arrive on.
func consumer(
	t *testing.T, broker corequeue.Broker, clk clock.Timed, handler corequeue.Handler,
) <-chan corequeue.DeliveryValue {
	t.Helper()
	seen := make(chan corequeue.DeliveryValue, 16)
	ctx, cancel := context.WithCancel(t.Context())
	var running sync.WaitGroup
	running.Go(func() {
		err := svcqueue.Consume(ctx, broker, svcqueue.ConsumerConfig{
			Handler: func(ctx context.Context, delivery corequeue.DeliveryValue) error {
				seen <- delivery
				return handler(ctx, delivery)
			},
			Clock: clk, PollInterval: idlePoll, HandlerIsIdempotent: true,
		})
		if err != nil {
			t.Errorf("Consume() = %v, want nil", err)
		}
	})
	t.Cleanup(func() {
		cancel()
		running.Wait()
	})
	return seen
}

// next waits, on the wall clock, for the next delivery.
func next(t *testing.T, seen <-chan corequeue.DeliveryValue) corequeue.DeliveryValue {
	t.Helper()
	select {
	case delivery := <-seen:
		return delivery
	case <-time.After(engineBudget):
		t.Fatalf("no delivery within %v: the consumer was not woken", engineBudget)
		return corequeue.DeliveryValue{}
	}
}

// accept is a handler that acknowledges everything.
func accept(context.Context, corequeue.DeliveryValue) error { return nil }

// TestAPublicationWakesAnIdleConsumer is the reason the capability exists: a
// consumer that found the queue empty sleeps for its whole poll interval
// unless something wakes it, so a short poll was the only way to get low
// latency and a dozen idle consumers polling every 50 ms cost a process ~3 %
// of a core doing nothing. Here the poll is an hour and the clock never
// moves, so only the wake can deliver the message.
func TestAPublicationWakesAnIdleConsumer(t *testing.T) {
	t.Parallel()
	for _, factory := range bothBrokers() {
		t.Run(factory.name, func(t *testing.T) {
			t.Parallel()
			clk := clock.NewManualClock(epoch)
			broker := factory.make(t, clk, defaultPolicy())
			seen := consumer(t, broker, clk, accept)
			//: the consumer found nothing and is asleep on its poll.
			clk.BlockUntil(1)

			publish(t, broker, "wake up")

			if got := string(next(t, seen).Message.Payload); got != "wake up" {
				t.Errorf("delivered %q, want the publication", got)
			}
			if clk.Now() != epoch {
				t.Errorf("the clock moved to %v; the delivery must not have needed it", clk.Now())
			}
		})
	}
}

// TestARetryIsDeliveredWhenItsDelayEndsNotAtTheNextPoll pins the half of
// "sleep long when idle" a publication signal alone would break: a nacked
// message becomes visible RetryDelay later, and nothing happens at that
// instant for a signal to report. The broker says when it will be due, and
// the consumer sleeps exactly that long rather than its hour.
func TestARetryIsDeliveredWhenItsDelayEndsNotAtTheNextPoll(t *testing.T) {
	t.Parallel()
	for _, factory := range bothBrokers() {
		t.Run(factory.name, func(t *testing.T) {
			t.Parallel()
			clk := clock.NewManualClock(epoch)
			broker := factory.make(t, clk, defaultPolicy())
			var calls atomic.Int32
			seen := consumer(t, broker, clk, func(context.Context, corequeue.DeliveryValue) error {
				//: the first delivery fails and is nacked; the second succeeds.
				if calls.Add(1) == 1 {
					return errors.New("transient")
				}
				return nil
			})
			publish(t, broker, "retried")
			if first := next(t, seen); first.Deliveries != 1 {
				t.Fatalf("first delivery counted %d, want 1", first.Deliveries)
			}
			//: nacked, and the consumer is asleep until the retry is due.
			clk.BlockUntil(1)
			clk.Advance(testRetryDelay)

			if second := next(t, seen); second.Deliveries != 2 {
				t.Errorf("second delivery counted %d, want 2", second.Deliveries)
			}
			if waited := clk.Now().Sub(epoch); waited != testRetryDelay {
				t.Errorf("the retry needed %v of clock, want exactly the retry delay %v", waited, testRetryDelay)
			}
		})
	}
}

// TestALapsedLeaseWakesTheConsumerWhenItLapses pins the other instant only the
// broker knows: a message whose consumer died is recovered when its lease
// lapses, and an idle consumer is woken for exactly that — not an hour later.
func TestALapsedLeaseWakesTheConsumerWhenItLapses(t *testing.T) {
	t.Parallel()
	for _, factory := range bothBrokers() {
		t.Run(factory.name, func(t *testing.T) {
			t.Parallel()
			clk := clock.NewManualClock(epoch)
			broker := factory.make(t, clk, defaultPolicy())
			publish(t, broker, "orphaned")
			//: a consumer that leases the message and then "dies": it never
			//: acknowledges and never hands it back.
			receiveOne(t, broker)

			seen := consumer(t, broker, clk, accept)
			clk.BlockUntil(1)
			clk.Advance(testVisibility)

			if recovered := next(t, seen); recovered.Deliveries != 2 {
				t.Errorf("the recovered delivery counted %d, want 2", recovered.Deliveries)
			}
		})
	}
}

// TestTwoDurableBrokersOverOneDirectoryWakeEachOther pins that the wake
// follows the QUEUE, not the value: two brokers over one directory are one
// queue, so a producer holding one and a consumer holding the other — the
// ordinary shape when the two are separate components — must still wake.
// A symbolic link to the directory is the same queue too, and on macOS the
// temporary directory itself is reached through one.
func TestTwoDurableBrokersOverOneDirectoryWakeEachOther(t *testing.T) {
	t.Parallel()
	clk := clock.NewManualClock(epoch)
	dir := t.TempDir()
	consuming, err := svcqueue.NewFile(svcqueue.FileConfig{Dir: dir, Policy: defaultPolicy(), Clock: clk})
	if err != nil {
		t.Fatalf("NewFile() = %v", err)
	}
	producing, err := svcqueue.NewFile(svcqueue.FileConfig{Dir: dir, Policy: defaultPolicy(), Clock: clk})
	if err != nil {
		t.Fatalf("NewFile() = %v", err)
	}
	seen := consumer(t, consuming, clk, accept)
	clk.BlockUntil(1)
	publish(t, producing, "through the other broker")
	if got := string(next(t, seen).Message.Payload); got != "through the other broker" {
		t.Errorf("delivered %q", got)
	}

	link := filepath.Join(t.TempDir(), "alias")
	//: a platform that will not make the link has nothing to test here.
	if linkErr := os.Symlink(dir, link); linkErr != nil {
		t.Skipf("symlink: %v", linkErr)
	}
	aliased, err := svcqueue.NewFile(svcqueue.FileConfig{Dir: link, Policy: defaultPolicy(), Clock: clk})
	if err != nil {
		t.Fatalf("NewFile(alias) = %v", err)
	}
	clk.BlockUntil(1)
	publish(t, aliased, "through the alias")
	if got := string(next(t, seen).Message.Payload); got != "through the alias" {
		t.Errorf("delivered %q", got)
	}
}

// TestWakeSaysWhatTheBrokerKnows pins the value itself, over both brokers: an
// empty broker schedules nothing, a message nacked with a delay is due after
// exactly that delay, and every Publish and Nack closes the signal handed out
// before it.
func TestWakeSaysWhatTheBrokerKnows(t *testing.T) {
	t.Parallel()
	for _, factory := range bothBrokers() {
		t.Run(factory.name, func(t *testing.T) {
			t.Parallel()
			clk := clock.NewManualClock(epoch)
			broker := factory.make(t, clk, defaultPolicy())
			waker, ok := broker.(corequeue.Waker)
			if !ok {
				t.Fatalf("%T does not implement queue.Waker", broker)
			}
			empty := waker.Wake()
			if empty.Signal == nil || empty.Scheduled {
				t.Fatalf("an empty broker's wake = %+v, want a signal and nothing scheduled", empty)
			}
			publish(t, broker, "m")
			assertClosed(t, empty.Signal, "Publish")

			beforeNack := waker.Wake()
			delivery := receiveOne(t, broker)
			nack(t, broker, delivery.Lease.Receipt, nil)
			assertClosed(t, beforeNack.Signal, "Nack")

			//: the file broker learns the instant from the Receive that reads
			//: the directory; the memory broker from its own list.
			receiveNone(t, broker)
			due := waker.Wake()
			if !due.Scheduled || due.In != testRetryDelay {
				t.Errorf("after a nack, Wake = %+v, want the retry due in %v", due, testRetryDelay)
			}
		})
	}
}

// assertClosed fails unless signal has been closed.
func assertClosed(t *testing.T, signal <-chan struct{}, by string) {
	t.Helper()
	select {
	case <-signal:
	default:
		t.Errorf("%s did not close the wake signal handed out before it", by)
	}
}
