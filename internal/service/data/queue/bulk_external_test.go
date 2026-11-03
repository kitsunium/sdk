// Package queue_test — the siblings that act on a whole queue (ADR 0054 and
// ADR 0151, amended 2026-11-02), run over every broker: the dead letters in
// one order and paged after a cursor, the backlog counted, the live messages
// purged, aged out or ranged over, the dead letters replayed and purged in
// bulk and kept for a retention, a batch published and acknowledged at once.
package queue_test

import (
	"context"
	"fmt"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	kbackoff "github.com/kitsunium/sdk/internal/kernel/backoff"
	"github.com/kitsunium/sdk/internal/kernel/clock"
	"github.com/kitsunium/sdk/internal/kernel/errs"

	corequeue "github.com/kitsunium/sdk/internal/core/data/queue"
	coresql "github.com/kitsunium/sdk/internal/core/data/sql"
	svcqueue "github.com/kitsunium/sdk/internal/service/data/queue"
)

// pager asserts the DeadLetterPager capability and returns it.
func pager(t *testing.T, broker corequeue.Broker) corequeue.DeadLetterPager {
	t.Helper()
	sibling, ok := broker.(corequeue.DeadLetterPager)
	if !ok {
		t.Fatalf("%T does not implement queue.DeadLetterPager", broker)
	}
	return sibling
}

// counter asserts the Counter capability and returns the broker's Stats.
func statsOf(t *testing.T, broker corequeue.Broker) corequeue.StatsValue {
	t.Helper()
	sibling, ok := broker.(corequeue.Counter)
	if !ok {
		t.Fatalf("%T does not implement queue.Counter", broker)
	}
	stats, err := sibling.Stats(t.Context())
	if err != nil {
		t.Fatalf("Stats() = %v, want nil", err)
	}
	return stats
}

// bulkManager asserts the DeadLetterBulkManager capability and returns it.
func bulkManager(t *testing.T, broker corequeue.Broker) corequeue.DeadLetterBulkManager {
	t.Helper()
	sibling, ok := broker.(corequeue.DeadLetterBulkManager)
	if !ok {
		t.Fatalf("%T does not implement queue.DeadLetterBulkManager", broker)
	}
	return sibling
}

// payloadsOf is the payloads of dead, as strings, in order.
func payloadsOf(dead []corequeue.DeadLetterValue) []string {
	out := make([]string, len(dead))
	for i, record := range dead {
		out[i] = string(record.Message.Payload)
	}
	return out
}

// buryStep is how far apart buryInOrder publishes, and buries: fifty steps
// stay inside one visibility timeout, so no lease lapses on the way.
const buryStep time.Duration = 100 * time.Millisecond

// buryInOrder publishes payloads one buryStep apart, then rejects them in the
// order die names — indexes into payloads —, one buryStep apart, so their
// enqueue order and their failure order differ.
func buryInOrder(t *testing.T, broker corequeue.Broker, clk *clock.ManualClock, payloads []string, die []int) {
	t.Helper()
	for _, payload := range payloads {
		publish(t, broker, payload)
		clk.Advance(buryStep)
	}
	deliveries := map[string]corequeue.DeliveryValue{}
	for _, delivery := range receive(t, broker, len(payloads)) {
		deliveries[string(delivery.Message.Payload)] = delivery
	}
	rejecter, _ := broker.(corequeue.Rejecter)
	for _, index := range die {
		if err := rejecter.Reject(t.Context(), deliveries[payloads[index]].Lease.Receipt, nil); err != nil {
			t.Fatalf("Reject(%s) = %v", payloads[index], err)
		}
		clk.Advance(buryStep)
	}
}

// TestTheDeadLettersAreListedInEnqueueOrderWhateverOrderTheyDied pins the
// one order of the dead-letter store on every broker (issue #1043): A
// enqueued first and buried last, B the other way, are listed A then B.
//
// Seen failing before the change, with this test:
//
//	sql/postgres: DeadLetters() = [B A], want [A B]
//	memory: DeadLetters() = [B A], want [A B]
//
// while the file broker, which lists by a name that opens with the enqueue
// instant, passed: the drift the shared suite exists to catch.
func TestTheDeadLettersAreListedInEnqueueOrderWhateverOrderTheyDied(t *testing.T) {
	t.Parallel()
	for _, factory := range everyBroker() {
		t.Run(factory.name, func(t *testing.T) {
			t.Parallel()
			clk := clock.NewManualClock(epoch)
			broker := factory.make(t, clk, defaultPolicy())
			buryInOrder(t, broker, clk, []string{"A", "B"}, []int{1, 0})
			if got := payloadsOf(deadLetters(t, broker, 10)); !slices.Equal(got, []string{"A", "B"}) {
				t.Fatalf("DeadLetters() = %v, want [A B]", got)
			}
		})
	}
}

// TestDeadLettersAfterResumesAfterAnIdentifier pages three dead letters one at
// a time, in the store's order, and refuses a non-positive page (issue #1043).
func TestDeadLettersAfterResumesAfterAnIdentifier(t *testing.T) {
	t.Parallel()
	for _, factory := range everyBroker() {
		t.Run(factory.name, func(t *testing.T) {
			t.Parallel()
			clk := clock.NewManualClock(epoch)
			broker := factory.make(t, clk, defaultPolicy())
			buryInOrder(t, broker, clk, []string{"A", "B", "C"}, []int{2, 0, 1})
			var got []string
			after := ""
			for range 4 {
				page, err := pager(t, broker).DeadLettersAfter(t.Context(), after, 1)
				if err != nil {
					t.Fatalf("DeadLettersAfter(%q, 1) = %v", after, err)
				}
				if len(page) == 0 {
					break
				}
				got = append(got, string(page[0].Message.Payload))
				after = page[0].Message.ID
			}
			if !slices.Equal(got, []string{"A", "B", "C"}) {
				t.Fatalf("paged one at a time = %v, want [A B C]", got)
			}
			if _, err := pager(t, broker).DeadLettersAfter(t.Context(), "", 0); !errs.HasCode(err, corequeue.CodeInvalidBatchSize) {
				t.Fatalf("DeadLettersAfter(max 0) = %v, want INVALID_BATCH_SIZE", err)
			}
		})
	}
}

// TestStatsCountsTheBacklogWithoutItsPayloads walks a queue through a
// publication, a lease, a nack and a rejection on a ManualClock and asserts
// the counts and the two instants at each step (issue #833).
func TestStatsCountsTheBacklogWithoutItsPayloads(t *testing.T) {
	t.Parallel()
	for _, factory := range everyBroker() {
		t.Run(factory.name, func(t *testing.T) {
			t.Parallel()
			clk := clock.NewManualClock(epoch)
			broker := factory.make(t, clk, defaultPolicy())
			if got := statsOf(t, broker); got != (corequeue.StatsValue{}) {
				t.Fatalf("Stats() of an empty queue = %+v, want the zero value", got)
			}
			first := publish(t, broker, "first")
			clk.Advance(time.Second)
			publish(t, broker, "second")
			clk.Advance(time.Second)
			publish(t, broker, "third")
			stats := statsOf(t, broker)
			if stats.Queued != 3 || stats.Leased != 0 || stats.Dead != 0 {
				t.Fatalf("after three publications Stats() = %+v, want 3 queued", stats)
			}
			if !stats.OldestEnqueuedAt.Equal(first.EnqueuedAt) || !stats.NextDueAt.Equal(first.EnqueuedAt) {
				t.Fatalf("Stats() oldest %v next %v, want both %v", stats.OldestEnqueuedAt, stats.NextDueAt, first.EnqueuedAt)
			}
			batch := receive(t, broker, 2)
			stats = statsOf(t, broker)
			if stats.Queued != 1 || stats.Leased != 2 || !stats.OldestEnqueuedAt.Equal(first.EnqueuedAt) {
				t.Fatalf("after leasing two Stats() = %+v, want 1 queued, 2 leased, the oldest still the first", stats)
			}
			verdict := nack(t, broker, batch[0].Lease.Receipt, nil)
			rejecter, _ := broker.(corequeue.Rejecter)
			if err := rejecter.Reject(t.Context(), batch[1].Lease.Receipt, nil); err != nil {
				t.Fatalf("Reject() = %v", err)
			}
			stats = statsOf(t, broker)
			if stats.Queued != 2 || stats.Leased != 0 || stats.Dead != 1 {
				t.Fatalf("after a nack and a rejection Stats() = %+v, want 2 queued, 1 dead", stats)
			}
			if !stats.NextDueAt.Equal(epoch.Add(2*time.Second)) || verdict.VisibleAt.Before(stats.NextDueAt) {
				t.Fatalf("Stats().NextDueAt = %v, want the third's %v, before the nacked one's %v",
					stats.NextDueAt, epoch.Add(2*time.Second), verdict.VisibleAt)
			}
		})
	}
}

// expiringPolicy is the default policy with a MaxAge of a minute.
func expiringPolicy() corequeue.PolicyValue {
	policy := defaultPolicy()
	policy.MaxAge = time.Minute
	return policy
}

// TestAMessagePastMaxAgeIsDeadLetteredUndelivered pins PolicyValue.MaxAge on
// every broker (issue #835): a message older than it is never received, and
// is in the dead-letter store with MESSAGE_EXPIRED and the count it had; a
// younger one is delivered; and a delivery already under way is not taken
// from its consumer.
func TestAMessagePastMaxAgeIsDeadLetteredUndelivered(t *testing.T) {
	t.Parallel()
	for _, factory := range everyBroker() {
		t.Run(factory.name, func(t *testing.T) {
			t.Parallel()
			clk := clock.NewManualClock(epoch)
			broker := factory.make(t, clk, expiringPolicy())
			stale := publish(t, broker, "stale")
			leased := publish(t, broker, "leased")
			clk.Advance(30 * time.Second)
			fresh := publish(t, broker, "fresh")
			held := receiveOne(t, broker)
			if held.Message.ID != stale.ID && held.Message.ID != leased.ID {
				t.Fatalf("the first Receive leased %q, want one of the two oldest", held.Message.Payload)
			}
			nack(t, broker, held.Lease.Receipt, nil)
			clk.Advance(31 * time.Second)
			delivery := receiveOne(t, broker)
			if delivery.Message.ID != fresh.ID {
				t.Fatalf("Receive past MaxAge delivered %q, want only %q", delivery.Message.Payload, "fresh")
			}
			receiveNone(t, broker)
			dead := deadLetters(t, broker, 10)
			if len(dead) != 2 {
				t.Fatalf("DeadLetters() = %d records, want the two expired", len(dead))
			}
			for _, record := range dead {
				if record.Reason != "MESSAGE_EXPIRED" || record.Code != uint32(corequeue.CodeMessageExpired) {
					t.Fatalf("an expired message's dead letter = %q %#x, want MESSAGE_EXPIRED", record.Reason, record.Code)
				}
				want := 0
				if record.Message.ID == held.Message.ID {
					want = 1
				}
				if record.Deliveries != want {
					t.Fatalf("the dead letter of %q counts %d deliveries, want %d", record.Message.Payload, record.Deliveries, want)
				}
			}
			publish(t, broker, "edge")
			clk.Advance(55 * time.Second)
			underWay := receiveOne(t, broker)
			clk.Advance(8 * time.Second)
			if err := broker.Ack(t.Context(), underWay.Lease.Receipt); err != nil {
				t.Fatalf("Ack() of %q past MaxAge but under its lease = %v, want nil", underWay.Message.Payload, err)
			}
		})
	}
}

// TestEveryBrokerRefusesALifetimeOrAJitterNoBrokerCanHonour pins the new
// fields' refusals, on every broker through the one guard (issues #835,
// #1044, #1120).
func TestEveryBrokerRefusesALifetimeOrAJitterNoBrokerCanHonour(t *testing.T) {
	t.Parallel()
	growing := defaultPolicy()
	growing.MaxRetryDelay = time.Minute
	cases := map[string]func(*corequeue.PolicyValue){
		"MaxAge negative":                   func(p *corequeue.PolicyValue) { p.MaxAge = -time.Second },
		"MaxAge past the ceiling":           func(p *corequeue.PolicyValue) { p.MaxAge = corequeue.MaxDeadlineOffset + 1 },
		"DeadLetterRetention negative":      func(p *corequeue.PolicyValue) { p.DeadLetterRetention = -time.Second },
		"RetryJitter above one":             func(p *corequeue.PolicyValue) { p.RetryJitter = 1.5 },
		"RetryJitter negative":              func(p *corequeue.PolicyValue) { p.RetryJitter = -0.1 },
		"RetryJitter with no MaxRetryDelay": func(p *corequeue.PolicyValue) { p.RetryJitter, p.MaxRetryDelay = 0.5, 0 },
		"RetryJitterMode with no MaxRetryDelay": func(p *corequeue.PolicyValue) {
			p.RetryJitterMode, p.MaxRetryDelay = 1, 0
		},
		"RetryJitterMode undefined":              func(p *corequeue.PolicyValue) { p.RetryJitterMode = 7 },
		"RetryJitter beside a mode that ignores": func(p *corequeue.PolicyValue) { p.RetryJitter, p.RetryJitterMode = 0.5, 1 },
	}
	for name, change := range cases {
		policy := growing
		change(&policy)
		for _, factory := range everyBroker() {
			_, err := newBrokerErr(t, factory.name, policy)
			if !errs.HasCode(err, corequeue.CodeQueueMisconfigured) {
				t.Errorf("%s on %s = %v, want QUEUE_MISCONFIGURED", name, factory.name, err)
			}
		}
	}
}

// TestPurgeEmptiesTheLiveMessagesAndKeepsTheDeadLetters pins the Purger on
// every broker (issue #835): waiting and leased messages go and are counted,
// a dead letter stays, and the purged lease's acknowledgement is refused.
func TestPurgeEmptiesTheLiveMessagesAndKeepsTheDeadLetters(t *testing.T) {
	t.Parallel()
	for _, factory := range everyBroker() {
		t.Run(factory.name, func(t *testing.T) {
			t.Parallel()
			clk := clock.NewManualClock(epoch)
			broker := factory.make(t, clk, defaultPolicy())
			buryInOrder(t, broker, clk, []string{"dead"}, []int{0})
			for _, payload := range []string{"a", "b", "c"} {
				publish(t, broker, payload)
			}
			held := receiveOne(t, broker)
			purger, ok := broker.(corequeue.Purger)
			if !ok {
				t.Fatalf("%T does not implement queue.Purger", broker)
			}
			purged, err := purger.Purge(t.Context())
			if err != nil || purged != (corequeue.PurgeValue{Queued: 2, Leased: 1}) {
				t.Fatalf("Purge() = %+v, %v, want 2 queued and 1 leased", purged, err)
			}
			if stats := statsOf(t, broker); stats.Queued != 0 || stats.Leased != 0 || stats.Dead != 1 {
				t.Fatalf("after Purge Stats() = %+v, want only the dead letter", stats)
			}
			receiveNone(t, broker)
			if err := broker.Ack(t.Context(), held.Lease.Receipt); !errs.HasCode(err, corequeue.CodeLeaseExpired) {
				t.Fatalf("Ack() of a purged message = %v, want LEASE_EXPIRED", err)
			}
		})
	}
}

// TestReplayDeadLettersReplaysTheOldestInBatches pins the bulk replay on
// every broker (issue #1120): fifty dead letters, twenty replayed per call,
// the oldest first, then the rest.
func TestReplayDeadLettersReplaysTheOldestInBatches(t *testing.T) {
	t.Parallel()
	for _, factory := range everyBroker() {
		t.Run(factory.name, func(t *testing.T) {
			t.Parallel()
			clk := clock.NewManualClock(epoch)
			broker := factory.make(t, clk, defaultPolicy())
			payloads := make([]string, 50)
			die := make([]int, 50)
			for i := range payloads {
				payloads[i] = fmt.Sprintf("m%02d", i)
				die[i] = 49 - i
			}
			buryInOrder(t, broker, clk, payloads, die)
			left := 50
			for _, want := range []int{20, 20, 10, 0} {
				replayed, err := bulkManager(t, broker).ReplayDeadLetters(t.Context(), 20)
				if err != nil || replayed != want {
					t.Fatalf("ReplayDeadLetters(20) = %d, %v, want %d", replayed, err, want)
				}
				left -= want
				if got := len(deadLetters(t, broker, 100)); got != left {
					t.Fatalf("after replaying %d the store holds %d, want %d", want, got, left)
				}
			}
			var got []string
			for _, delivery := range receive(t, broker, 100) {
				if delivery.Deliveries != 1 {
					t.Fatalf("a replayed message counts %d deliveries, want 1", delivery.Deliveries)
				}
				got = append(got, string(delivery.Message.Payload))
			}
			slices.Sort(got)
			if !slices.Equal(got, payloads) {
				t.Fatalf("replayed %v, want every dead letter", got)
			}
			if _, err := bulkManager(t, broker).ReplayDeadLetters(t.Context(), 0); !errs.HasCode(err, corequeue.CodeInvalidBatchSize) {
				t.Fatalf("ReplayDeadLetters(0) = %v, want INVALID_BATCH_SIZE", err)
			}
		})
	}
}

// TestReplayDeadLettersTakesTheOldestFirst pins the order of a partial batch:
// of three dead letters, a batch of two replays the two enqueued first, and
// the third is still dead.
func TestReplayDeadLettersTakesTheOldestFirst(t *testing.T) {
	t.Parallel()
	for _, factory := range everyBroker() {
		t.Run(factory.name, func(t *testing.T) {
			t.Parallel()
			clk := clock.NewManualClock(epoch)
			broker := factory.make(t, clk, defaultPolicy())
			buryInOrder(t, broker, clk, []string{"A", "B", "C"}, []int{0, 2, 1})
			if replayed, err := bulkManager(t, broker).ReplayDeadLetters(t.Context(), 2); err != nil || replayed != 2 {
				t.Fatalf("ReplayDeadLetters(2) = %d, %v, want 2", replayed, err)
			}
			if got := payloadsOf(deadLetters(t, broker, 10)); !slices.Equal(got, []string{"C"}) {
				t.Fatalf("still dead = %v, want [C]", got)
			}
		})
	}
}

// TestPurgeDeadLettersRemovesThoseThatFailedBefore pins the bulk deletion on
// every broker (issue #1120): only the dead letters that failed before the
// instant go, never a live message, at most max per call.
func TestPurgeDeadLettersRemovesThoseThatFailedBefore(t *testing.T) {
	t.Parallel()
	for _, factory := range everyBroker() {
		t.Run(factory.name, func(t *testing.T) {
			t.Parallel()
			clk := clock.NewManualClock(epoch)
			broker := factory.make(t, clk, defaultPolicy())
			buryInOrder(t, broker, clk, []string{"A", "B", "C", "D"}, []int{3, 2, 1, 0})
			publish(t, broker, "live")
			//: A..D were published at steps 0..3 and died at steps 4, 5, 6, 7
			//: in the order D, C, B, A: before step 6 are D and C.
			cutoff := epoch.Add(6 * buryStep)
			removed, err := bulkManager(t, broker).PurgeDeadLetters(t.Context(), cutoff, 1)
			if err != nil || removed != 1 {
				t.Fatalf("PurgeDeadLetters(max 1) = %d, %v, want 1", removed, err)
			}
			removed, err = bulkManager(t, broker).PurgeDeadLetters(t.Context(), cutoff, 10)
			if err != nil || removed != 1 {
				t.Fatalf("PurgeDeadLetters(max 10) = %d, %v, want the other 1", removed, err)
			}
			if got := payloadsOf(deadLetters(t, broker, 10)); !slices.Equal(got, []string{"A", "B"}) {
				t.Fatalf("still dead = %v, want [A B]", got)
			}
			if stats := statsOf(t, broker); stats.Queued != 1 {
				t.Fatalf("the live message was touched: Stats() = %+v", stats)
			}
		})
	}
}

// TestADeadLetterPastItsRetentionIsRemovedByAReceive pins
// PolicyValue.DeadLetterRetention on every broker (issue #1120): readable
// after 23h59m, gone after 24h01m, with no call but Receive; and a zero
// retention keeps it.
func TestADeadLetterPastItsRetentionIsRemovedByAReceive(t *testing.T) {
	t.Parallel()
	for _, factory := range everyBroker() {
		for _, retention := range []time.Duration{0, 24 * time.Hour} {
			t.Run(fmt.Sprintf("%s/%v", factory.name, retention), func(t *testing.T) {
				t.Parallel()
				clk := clock.NewManualClock(epoch)
				policy := defaultPolicy()
				policy.DeadLetterRetention = retention
				broker := factory.make(t, clk, policy)
				buryInOrder(t, broker, clk, []string{"A"}, []int{0})
				clk.Advance(23*time.Hour + 59*time.Minute - time.Second)
				receiveNone(t, broker)
				if got := len(deadLetters(t, broker, 10)); got != 1 {
					t.Fatalf("after 23h59m the store holds %d, want 1", got)
				}
				clk.Advance(2 * time.Minute)
				receiveNone(t, broker)
				want := 0
				if retention == 0 {
					want = 1
				}
				if got := len(deadLetters(t, broker, 10)); got != want {
					t.Fatalf("after 24h01m with retention %v the store holds %d, want %d", retention, got, want)
				}
			})
		}
	}
}

// TestPublishManyPublishesTheBatchInOrder pins the BatchPublisher on every
// broker (issue #827): one message per payload, in order, every one
// received; an oversized payload refuses the whole batch; an empty one
// publishes nothing.
func TestPublishManyPublishesTheBatchInOrder(t *testing.T) {
	t.Parallel()
	for _, factory := range everyBroker() {
		t.Run(factory.name, func(t *testing.T) {
			t.Parallel()
			clk := clock.NewManualClock(epoch)
			policy := defaultPolicy()
			policy.MaxMessageBytes = 8
			broker := factory.make(t, clk, policy)
			publisher, ok := broker.(corequeue.BatchPublisher)
			if !ok {
				t.Fatalf("%T does not implement queue.BatchPublisher", broker)
			}
			if _, err := publisher.PublishMany(t.Context(), [][]byte{[]byte("ok"), []byte("far too large")}); !errs.HasCode(err, corequeue.CodeMessageTooLarge) {
				t.Fatalf("PublishMany(one too large) = %v, want MESSAGE_TOO_LARGE", err)
			}
			if messages, err := publisher.PublishMany(t.Context(), nil); err != nil || len(messages) != 0 {
				t.Fatalf("PublishMany(nothing) = %v, %v, want nothing", messages, err)
			}
			receiveNone(t, broker)
			messages, err := publisher.PublishMany(t.Context(), [][]byte{[]byte("one"), []byte("two"), []byte("three")})
			if err != nil || len(messages) != 3 {
				t.Fatalf("PublishMany(three) = %d messages, %v", len(messages), err)
			}
			var got []string
			for _, delivery := range receive(t, broker, 10) {
				got = append(got, string(delivery.Message.Payload))
			}
			slices.Sort(got)
			if !slices.Equal(got, []string{"one", "three", "two"}) {
				t.Fatalf("received %v, want the batch", got)
			}
			for index, message := range messages {
				if want := []string{"one", "two", "three"}[index]; string(message.Payload) != want || message.ID == "" {
					t.Fatalf("message %d = %q %q, want %q with an identifier", index, message.ID, message.Payload, want)
				}
			}
		})
	}
}

// TestAckManyReportsAnOutcomePerReceipt pins the Acker on every broker
// (issue #1152): a mixed batch — two held leases, one lapsed and leased
// again, one receipt no broker issued — acknowledges the two, reports the
// other two, and removes only what it says it removed.
func TestAckManyReportsAnOutcomePerReceipt(t *testing.T) {
	t.Parallel()
	for _, factory := range everyBroker() {
		t.Run(factory.name, func(t *testing.T) {
			t.Parallel()
			clk := clock.NewManualClock(epoch)
			broker := factory.make(t, clk, defaultPolicy())
			publish(t, broker, "lapsed")
			stale := receiveOne(t, broker)
			clk.Advance(testVisibility)
			again := receiveOne(t, broker)
			publish(t, broker, "a")
			publish(t, broker, "b")
			held := receive(t, broker, 2)
			acker, ok := broker.(corequeue.Acker)
			if !ok {
				t.Fatalf("%T does not implement queue.Acker", broker)
			}
			receipts := []corequeue.ReceiptValue{held[0].Lease.Receipt, stale.Lease.Receipt, "not-a-receipt", held[1].Lease.Receipt}
			result, err := acker.AckMany(t.Context(), receipts)
			want := []corequeue.AckOutcome{
				corequeue.AckAcknowledged, corequeue.AckLeaseExpired, corequeue.AckUnknownReceipt, corequeue.AckAcknowledged,
			}
			if err != nil || !slices.Equal(result.Outcomes, want) {
				t.Fatalf("AckMany() = %v, %v, want %v", result.Outcomes, err, want)
			}
			if stats := statsOf(t, broker); stats.Queued != 0 || stats.Leased != 1 {
				t.Fatalf("after AckMany Stats() = %+v, want only the redelivered lease", stats)
			}
			if err := broker.Ack(t.Context(), again.Lease.Receipt); err != nil {
				t.Fatalf("Ack() of the lease AckMany refused to touch = %v", err)
			}
			if result, err := acker.AckMany(t.Context(), nil); err != nil || len(result.Outcomes) != 0 {
				t.Fatalf("AckMany(nothing) = %v, %v", result.Outcomes, err)
			}
		})
	}
}

// TestAllMessagesYieldsWhatMessagesPages pins the MessageLister on every
// broker (issue #816): every live message in identifier order — more than a
// page's worth —, resumable after a cursor, and a loop that breaks early.
func TestAllMessagesYieldsWhatMessagesPages(t *testing.T) {
	t.Parallel()
	for _, factory := range everyBroker() {
		t.Run(factory.name, func(t *testing.T) {
			t.Parallel()
			clk := clock.NewManualClock(epoch)
			broker := factory.make(t, clk, defaultPolicy())
			const total = 300
			for i := range total {
				publish(t, broker, fmt.Sprintf("m%03d", i))
				clk.Advance(time.Millisecond)
			}
			receive(t, broker, 5)
			lister, ok := broker.(corequeue.MessageLister)
			if !ok {
				t.Fatalf("%T does not implement queue.MessageLister", broker)
			}
			var ids []string
			leased := 0
			for live, err := range lister.AllMessages(t.Context(), "") {
				if err != nil {
					t.Fatalf("AllMessages() yielded %v", err)
				}
				ids = append(ids, live.Message.ID)
				if live.Leased {
					leased++
				}
			}
			if len(ids) != total || leased != 5 || !slices.IsSorted(ids) {
				t.Fatalf("AllMessages() yielded %d (%d leased, sorted %v), want %d with 5 leased in identifier order",
					len(ids), leased, slices.IsSorted(ids), total)
			}
			var rest int
			for _, err := range lister.AllMessages(t.Context(), ids[99]) {
				if err != nil {
					t.Fatalf("AllMessages(after) yielded %v", err)
				}
				rest++
			}
			if rest != total-100 {
				t.Fatalf("AllMessages(after the 100th) yielded %d, want %d", rest, total-100)
			}
			for range lister.AllMessages(t.Context(), "") {
				break
			}
			cancelled, cancel := context.WithCancel(t.Context())
			cancel()
			for _, err := range lister.AllMessages(cancelled, "") {
				if err == nil {
					t.Fatal("AllMessages(cancelled) yielded a message, want the context's error")
				}
				break
			}
		})
	}
}

// fixedRandom returns a Random that hands out draws in turn, round and round,
// safe for the concurrent use a broker makes of it.
func fixedRandom(draws ...float64) func() float64 {
	var next atomic.Int64
	return func() float64 {
		return draws[int(next.Add(1)-1)%len(draws)]
	}
}

// TestAJitteredRetryDelaySpreadsTheNacksOfOneInstant pins RetryJitter on
// every broker (issue #1044): ten messages nacked in one instant, with a
// fixed Random, come back at ten distinct instants inside [delay, delay ×
// (1 + RetryJitter)], and Wake reports the earliest.
func TestAJitteredRetryDelaySpreadsTheNacksOfOneInstant(t *testing.T) {
	t.Parallel()
	draws := []float64{0.05, 0.95, 0.15, 0.85, 0.25, 0.75, 0.35, 0.65, 0.45, 0.55}
	policy := defaultPolicy()
	policy.MaxRetryDelay, policy.RetryJitter, policy.MaxDeliveries = time.Minute, 0.5, 5
	for index := range everyBroker() {
		factory := everyJitteredBroker(fixedRandom(draws...))[index]
		t.Run(factory.name, func(t *testing.T) {
			t.Parallel()
			clk := clock.NewManualClock(epoch)
			broker := factory.make(t, clk, policy)
			for i := range 10 {
				publish(t, broker, fmt.Sprintf("m%d", i))
			}
			seen := map[time.Time]bool{}
			low, high := epoch.Add(testRetryDelay), epoch.Add(testRetryDelay+testRetryDelay/2)
			earliestAt := high
			for _, delivery := range receive(t, broker, 10) {
				visibleAt := nack(t, broker, delivery.Lease.Receipt, nil).VisibleAt
				if visibleAt.Before(low) || visibleAt.After(high) {
					t.Fatalf("a jittered retry is visible at %v, want within [%v, %v]", visibleAt, low, high)
				}
				seen[visibleAt] = true
				if visibleAt.Before(earliestAt) {
					earliestAt = visibleAt
				}
			}
			if len(seen) != 10 {
				t.Fatalf("ten nacks of one instant came back at %d distinct instants, want 10", len(seen))
			}
			//: a Receive is what reads the next due instant on a durable broker.
			receiveNone(t, broker)
			if wake := broker.(corequeue.Waker).Wake(); !wake.Scheduled || wake.In != earliestAt.Sub(epoch) {
				t.Fatalf("Wake() = %+v, want the earliest retry in %v", wake, earliestAt.Sub(epoch))
			}
		})
	}
}

// TestAZeroJitterRetriesAtTheCurvesInstant pins that the zeros keep the retry
// instant the curve always gave, and draw nothing (issue #1044).
func TestAZeroJitterRetriesAtTheCurvesInstant(t *testing.T) {
	t.Parallel()
	policy := defaultPolicy()
	policy.MaxRetryDelay = time.Minute
	for _, factory := range everyJitteredBroker(func() float64 { panic("a policy with no jitter drew") }) {
		t.Run(factory.name, func(t *testing.T) {
			t.Parallel()
			clk := clock.NewManualClock(epoch)
			broker := factory.make(t, clk, policy)
			publish(t, broker, "m")
			if got := nack(t, broker, receiveOne(t, broker).Lease.Receipt, nil).VisibleAt; !got.Equal(epoch.Add(testRetryDelay)) {
				t.Fatalf("a retry without jitter is visible at %v, want %v", got, epoch.Add(testRetryDelay))
			}
		})
	}
}

// TestTheFullAndDecorrelatedJittersStayInTheirBounds pins the two other
// modes against a draw at each end of [0, 1) (issue #1044).
func TestTheFullAndDecorrelatedJittersStayInTheirBounds(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name      string
		mode      uint8
		draw      float64
		low, high time.Duration
	}{
		{"full/low", 1, 0, 0, 0},
		{"full/high", 1, 0.999999999, testRetryDelay - 10, testRetryDelay},
		{"decorrelated/low", 2, 0, testRetryDelay, testRetryDelay},
		{"decorrelated/high", 2, 0.999999999, 3*testRetryDelay - 10, 3 * testRetryDelay},
	} {
		policy := defaultPolicy()
		policy.MaxRetryDelay, policy.RetryJitterMode = time.Minute, kbackoffMode(tc.mode)
		for _, factory := range everyJitteredBroker(fixedRandom(tc.draw)) {
			t.Run(tc.name+"/"+factory.name, func(t *testing.T) {
				t.Parallel()
				clk := clock.NewManualClock(epoch)
				broker := factory.make(t, clk, policy)
				publish(t, broker, "m")
				got := nack(t, broker, receiveOne(t, broker).Lease.Receipt, nil).VisibleAt.Sub(epoch)
				if got < tc.low || got > tc.high {
					t.Fatalf("the retry waits %v, want within [%v, %v]", got, tc.low, tc.high)
				}
			})
		}
	}
}

// newBrokerErr builds the broker factory name names over policy and returns
// what its constructor returned.
func newBrokerErr(t *testing.T, name string, policy corequeue.PolicyValue) (corequeue.Broker, error) {
	t.Helper()
	switch name {
	case "memory":
		return svcqueue.NewMemory(svcqueue.MemoryConfig{Policy: policy})
	case "file":
		return svcqueue.NewFile(svcqueue.FileConfig{Dir: t.TempDir(), Policy: policy})
	default:
		dialect := sqlDialectNamed(t, name)
		return svcqueue.NewSQL(svcqueue.SQLConfig{
			Transactor: sqlTransactor(t, dialect), Dialect: dialect, Table: jobsTable, Policy: policy,
		})
	}
}

// sqlDialectNamed is the dialect of the SQL factory name names.
func sqlDialectNamed(t *testing.T, name string) coresql.Dialect {
	t.Helper()
	for _, dialect := range sqlDialects {
		if name == "sql/"+dialect.String() {
			return dialect
		}
	}
	t.Fatalf("no SQL dialect is named %q", name)
	return coresql.DialectUnknown
}

// everyJitteredBroker is everyBroker with random as every broker's Random.
func everyJitteredBroker(random func() float64) []brokerFactory {
	var factories []brokerFactory
	for _, factory := range everyBroker() {
		name := factory.name
		factories = append(factories, brokerFactory{name: name, make: func(t *testing.T, clk clock.Clock, policy corequeue.PolicyValue) corequeue.Broker {
			t.Helper()
			switch name {
			case "memory":
				broker, err := svcqueue.NewMemory(svcqueue.MemoryConfig{Policy: policy, Clock: clk, Random: random})
				if err != nil {
					t.Fatalf("NewMemory() = %v", err)
				}
				return broker
			case "file":
				return requireFileBroker(t, svcqueue.FileConfig{Dir: t.TempDir(), Policy: policy, Clock: clk, Random: random})
			default:
				dialect := sqlDialectNamed(t, name)
				engine := newSQLEngine(dialect, jobsTable, true)
				broker, err := svcqueue.NewSQL(svcqueue.SQLConfig{
					Transactor: newSQLTransactor(t, engine, dialect), Dialect: dialect, Table: jobsTable,
					Policy: policy, Clock: clk, Trail: true, Random: random,
				})
				if err != nil {
					t.Fatalf("NewSQL() = %v", err)
				}
				return broker
			}
		}})
	}
	return factories
}

// kbackoffMode is a jitter mode by its number, as the policy carries it.
func kbackoffMode(mode uint8) kbackoff.JitterMode {
	return kbackoff.JitterMode(mode)
}
