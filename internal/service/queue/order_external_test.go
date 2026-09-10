package queue_test

import (
	"errors"
	"strconv"
	"testing"

	"github.com/kitsunium/sdk/internal/kernel/clock"

	corequeue "github.com/kitsunium/sdk/internal/core/queue"
)

// TestOneConsumerAndNoFailuresDeliversInPublicationOrder is the ordering
// promise in its entirety: one consumer, nothing fails, messages come out the
// way they went in. Everything below this test is a way to lose it.
func TestOneConsumerAndNoFailuresDeliversInPublicationOrder(t *testing.T) {
	t.Parallel()
	const count int = 25
	for _, factory := range bothBrokers() {
		t.Run(factory.name, func(t *testing.T) {
			t.Parallel()
			clk := clock.NewManualClock(epoch)
			broker := factory.make(t, clk, defaultPolicy())
			for index := range count {
				//: distinct instants, because two messages published in the
				//: same nanosecond have nothing to be ordered BY.
				clk.Advance(1)
				publish(t, broker, strconv.Itoa(index))
			}
			for index := range count {
				delivery := receiveOne(t, broker)
				if string(delivery.Message.Payload) != strconv.Itoa(index) {
					t.Fatalf("position %d delivered %q, want %q",
						index, delivery.Message.Payload, strconv.Itoa(index))
				}
				if ackErr := broker.Ack(t.Context(), delivery.Lease.Receipt); ackErr != nil {
					t.Fatalf("Ack() = %v, want nil", ackErr)
				}
			}
		})
	}
}

// TestASingleRetryReordersTheStream is the one everybody discovers in
// production, demonstrated here on purpose rather than left to be found.
//
// One consumer. No concurrency. Three messages published in order. The FIRST
// one fails once — and it is delivered LAST, because a nacked message becomes
// visible again after the messages that were published behind it.
//
// This is not a defect to be fixed. It is the arithmetic of a retry: a
// message that must be reprocessed can either wait (which stalls everything
// behind it — head-of-line blocking, a worse failure) or step aside. This
// domain steps aside and says so, in the ADR, in the package documentation
// and here.
func TestASingleRetryReordersTheStream(t *testing.T) {
	t.Parallel()
	for _, factory := range bothBrokers() {
		t.Run(factory.name, func(t *testing.T) {
			t.Parallel()
			clk := clock.NewManualClock(epoch)
			broker := factory.make(t, clk, defaultPolicy())
			for _, payload := range []string{"first", "second", "third"} {
				clk.Advance(1)
				publish(t, broker, payload)
			}

			//: "first" fails once and goes back.
			failed := receiveOne(t, broker)
			if string(failed.Message.Payload) != "first" {
				t.Fatalf("the first delivery was %q, want \"first\"", failed.Message.Payload)
			}
			nack(t, broker, failed.Lease.Receipt, errors.New("transient")) //nolint:err113
			clk.Advance(testRetryDelay)

			got := drainPayloads(t, broker)
			want := []string{"second", "third", "first"}
			if len(got) != len(want) {
				t.Fatalf("drained %v, want %v", got, want)
			}
			for index := range want {
				if got[index] != want[index] {
					t.Fatalf("drained %v, want %v — a single retry reorders the stream, and this "+
						"test exists to keep that documented rather than discovered", got, want)
				}
			}
		})
	}
}

// drainPayloads acknowledges everything currently visible, oldest first.
func drainPayloads(t *testing.T, broker corequeue.Broker) []string {
	t.Helper()
	var out []string
	for {
		batch := receive(t, broker, 1)
		if len(batch) == 0 {
			return out
		}
		out = append(out, string(batch[0].Message.Payload))
		if err := broker.Ack(t.Context(), batch[0].Lease.Receipt); err != nil {
			t.Fatalf("Ack() = %v, want nil", err)
		}
	}
}

// TestTwoConsumersNeverSeeTheSameMessage pins the exclusion. On the durable
// broker it is rename(2) and nothing else — no lock file, nothing a dead
// process could hold — and the loser of the race simply moves on.
func TestTwoConsumersNeverSeeTheSameMessage(t *testing.T) {
	t.Parallel()
	const count int = 40
	for _, factory := range bothBrokers() {
		t.Run(factory.name, func(t *testing.T) {
			t.Parallel()
			clk := clock.NewManualClock(epoch)
			broker := factory.make(t, clk, defaultPolicy())
			for index := range count {
				clk.Advance(1)
				publish(t, broker, strconv.Itoa(index))
			}
			seen := map[string]int{}
			for {
				batch := receive(t, broker, 7)
				if len(batch) == 0 {
					break
				}
				for _, delivery := range batch {
					seen[delivery.Message.ID]++
				}
			}
			if len(seen) != count {
				t.Fatalf("saw %d distinct messages, want %d", len(seen), count)
			}
			for id, times := range seen {
				if times != 1 {
					t.Fatalf("message %s was leased %d times concurrently, want once", id, times)
				}
			}
		})
	}
}
