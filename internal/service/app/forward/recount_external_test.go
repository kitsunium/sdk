package forward_test

import (
	"context"
	"fmt"
	"runtime"
	"sync"
	"testing"
	"time"

	corequeue "github.com/kitsunium/sdk/internal/core/data/queue"
	svcforward "github.com/kitsunium/sdk/internal/service/app/forward"
)

// The backlog another process changes (issue #580): a dead letter replayed
// into the live queue, or a batch another consumer took, is not this
// forwarder's doing, and it must still hold the queue's backlog — its
// bounds, and what Stats says of them, the readiness and the runtime state
// of a product built on it. Every case runs on a ManualClock and never
// sleeps.

// pastRecount is an advance of the clock past any interval between two
// counts of the backlog, below testMaxAge and the long attempt bound the
// cases that hold an attempt configure.
const pastRecount time.Duration = time.Hour

// longAttempt bounds an attempt the cases hold, so no advance cancels it.
const longAttempt time.Duration = 100 * time.Hour

// deadLetterID is the identifier of the one dead letter the broker holds.
func deadLetterID(t *testing.T, broker corequeue.Broker) string {
	t.Helper()
	reader, ok := broker.(corequeue.DeadLetterReader)
	if !ok {
		t.Fatal("the broker reads no dead letters")
	}
	deadline := time.Now().Add(waitBudget)
	for time.Now().Before(deadline) {
		dead, err := reader.DeadLetters(context.Background(), 10)
		if err != nil {
			t.Fatalf("DeadLetters: %v", err)
		}
		if len(dead) == 1 {
			return dead[0].Message.ID
		}
	}
	t.Fatal("the broker never held one dead letter")
	return ""
}

// liveMessages counts what the broker holds live, queued or leased.
func liveMessages(t *testing.T, broker corequeue.Broker) (count int, bytes int64) {
	t.Helper()
	live, err := broker.(corequeue.MessageReader).Messages(context.Background(), "", 1000)
	if err != nil {
		t.Fatalf("Messages: %v", err)
	}
	for _, m := range live {
		bytes += int64(len(m.Message.Payload))
	}
	return len(live), bytes
}

// TestADeadLetterReplayedWhileRunningIsCounted pins issue #580's first
// point: a dead letter another process replays into the live queue — an
// operator's `otlp replay` while the product runs — is counted pending once
// the forwarder counts its backlog again, while the batch before it is
// still being retried; once both are sent the counts are back to zero,
// neither taken below what the queue holds.
func TestADeadLetterReplayedWhileRunningIsCounted(t *testing.T) {
	fx := newFixture(t, svcforward.Config{}, permanent, transient)
	fx.enqueue(t, "rejected")
	fx.run(t)
	fx.sender.awaitCall(t)
	fx.awaitStats(t, "dead-lettered", func(s svcforward.StatsValue) bool { return s.DeadLettered == 1 && s.Pending == 0 })
	id := deadLetterID(t, fx.broker)

	fx.enqueue(t, "waiting")
	fx.sender.awaitCall(t)
	fx.clock.awaitTimer(t, testBase)
	if err := fx.broker.(corequeue.DeadLetterManager).ReplayDeadLetter(context.Background(), id); err != nil {
		t.Fatalf("ReplayDeadLetter: %v", err)
	}
	// The collector is still down: the batch before the replayed one is
	// retried, and the replayed one waits behind it, uncounted until the
	// forwarder counts its queue again.
	fx.clock.Advance(pastRecount)
	fx.sender.awaitCall(t)
	want := int64(len("rejected") + len("waiting"))
	fx.awaitStats(t, "the replayed batch counted", func(s svcforward.StatsValue) bool {
		return s.Pending == 2 && s.PendingBytes == want
	})

	// The collector back: the retry after the third attempt sends both.
	fx.clock.awaitTimer(t, 2*testBase)
	fx.sender.mu.Lock()
	fx.sender.verdicts = []error{nil}
	fx.sender.mu.Unlock()
	fx.clock.Advance(2 * testBase)
	stats := fx.awaitStats(t, "both sent", func(s svcforward.StatsValue) bool { return s.Sent == 2 })
	if stats.Pending != 0 || stats.PendingBytes != 0 {
		t.Errorf("stats = %+v, want nothing pending once both are sent", stats)
	}
}

// TestABatchAnotherConsumerTookIsNoLongerCounted pins the other direction:
// a batch this forwarder queued and another process took out of the queue —
// a second instance of the product consuming the same table — stops being
// counted once the forwarder counts its queue again, so its bounds are not
// spent on batches it no longer holds.
func TestABatchAnotherConsumerTookIsNoLongerCounted(t *testing.T) {
	fx := newFixture(t, svcforward.Config{SendTimeout: longAttempt})
	fx.sender.hold = make(chan struct{})
	fx.enqueue(t, "held")
	fx.run(t)
	fx.sender.awaitCall(t)
	fx.enqueue(t, "taken")
	deliveries, err := fx.broker.Receive(context.Background(), 1)
	if err != nil || len(deliveries) != 1 || string(deliveries[0].Message.Payload) != "taken" {
		t.Fatalf("Receive = (%v, %v), want the second batch", deliveries, err)
	}
	if err := fx.broker.Ack(context.Background(), deliveries[0].Lease.Receipt); err != nil {
		t.Fatalf("Ack: %v", err)
	}
	fx.clock.Advance(pastRecount)
	fx.awaitStats(t, "the batch taken uncounted", func(s svcforward.StatsValue) bool {
		return s.Pending == 1 && s.PendingBytes == int64(len("held"))
	})
	close(fx.sender.hold)
	stats := fx.awaitStats(t, "the held batch sent", func(s svcforward.StatsValue) bool { return s.Sent == 1 })
	if stats.Pending != 0 || stats.PendingBytes != 0 {
		t.Errorf("stats = %+v, want nothing pending", stats)
	}
}

// TestABatchAnotherProcessQueuedIsCountedThenSent pins the foreign batch at
// its delivery: a batch another process published is counted from the
// first count after it, and its delivery takes nothing from the batches
// this forwarder holds — before #580 it took one, and a held batch went
// uncounted until the next start.
func TestABatchAnotherProcessQueuedIsCountedThenSent(t *testing.T) {
	fx := newFixture(t, svcforward.Config{SendTimeout: longAttempt})
	fx.sender.hold = make(chan struct{})
	if _, err := fx.broker.Publish(context.Background(), []byte("foreign")); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	fx.run(t)
	fx.sender.awaitCall(t)
	fx.enqueue(t, "own")
	fx.clock.Advance(pastRecount)
	fx.awaitStats(t, "both counted", func(s svcforward.StatsValue) bool {
		return s.Pending == 2 && s.PendingBytes == int64(len("foreign")+len("own"))
	})
	close(fx.sender.hold)
	stats := fx.awaitStats(t, "both sent", func(s svcforward.StatsValue) bool { return s.Sent == 2 })
	if stats.Pending != 0 || stats.PendingBytes != 0 {
		t.Errorf("stats = %+v, want nothing pending", stats)
	}
	awaitDrained(t, fx.broker)
}

// awaitDrained waits until the broker holds no live batch — the consumer
// acknowledges a batch after the forwarder settled it —, yielding the
// processor between reads; it never sleeps.
func awaitDrained(t *testing.T, broker corequeue.Broker) {
	t.Helper()
	deadline := time.Now().Add(waitBudget)
	for {
		count, _ := liveMessages(t, broker)
		if count == 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("the queue still holds %d batches", count)
		}
		runtime.Gosched()
	}
}

// TestTheRecountIntervalIsTheCallers pins Config.RecountInterval: a batch
// another process queued is counted at the first read of Stats once the
// interval the caller named has passed on the clock.
func TestTheRecountIntervalIsTheCallers(t *testing.T) {
	const every = 10 * time.Minute
	fx := newFixture(t, svcforward.Config{SendTimeout: longAttempt, RecountInterval: every})
	fx.sender.hold = make(chan struct{})
	fx.enqueue(t, "own")
	fx.run(t)
	fx.sender.awaitCall(t)
	if _, err := fx.broker.Publish(context.Background(), []byte("foreign")); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	fx.clock.Advance(every)
	fx.awaitStats(t, "the foreign batch counted", func(s svcforward.StatsValue) bool {
		return s.Pending == 2 && s.PendingBytes == int64(len("own")+len("foreign"))
	})
	close(fx.sender.hold)
	fx.awaitStats(t, "both sent", func(s svcforward.StatsValue) bool {
		return s.Sent == 2 && s.Pending == 0 && s.PendingBytes == 0
	})
	awaitDrained(t, fx.broker)
}

// TestTheCountsHoldUnderTrafficAndCounts pins the counts against the queue
// while everything moves at once: four producers, four consumers, and a
// count asked at every read of Stats and every batch leaving — an interval
// of one nanosecond on a clock the reads advance —, half the batches
// published by another process. Once every batch is sent, nothing is
// pending: a count that took a batch twice, or a batch leaving that took
// one it did not count, would leave a residue.
func TestTheCountsHoldUnderTrafficAndCounts(t *testing.T) {
	const producers, perProducer = 4, 24
	fx := newFixture(t, svcforward.Config{Parallelism: 4, RecountInterval: time.Nanosecond})
	fx.run(t)
	var wg sync.WaitGroup
	for p := range producers {
		wg.Go(func() {
			for i := range perProducer {
				batch := fmt.Appendf(nil, "p%d-%d", p, i)
				var err error
				if i%2 == 0 {
					err = fx.forwarder.Enqueue(context.Background(), batch)
				} else {
					_, err = fx.broker.Publish(context.Background(), batch)
				}
				if err != nil {
					t.Errorf("batch %s: %v", batch, err)
				}
				fx.clock.Advance(time.Nanosecond)
				fx.forwarder.Stats()
			}
		})
	}
	wg.Wait()
	fx.awaitStats(t, "every batch sent", func(s svcforward.StatsValue) bool {
		fx.clock.Advance(time.Nanosecond)
		return s.Sent == producers*perProducer
	})
	awaitDrained(t, fx.broker)
	stats := fx.awaitStats(t, "nothing pending", func(s svcforward.StatsValue) bool {
		fx.clock.Advance(time.Nanosecond)
		return s.Pending == 0 && s.PendingBytes == 0
	})
	if stats.Enqueued != producers*perProducer/2 {
		t.Errorf("stats = %+v, want %d enqueued", stats, producers*perProducer/2)
	}
}
