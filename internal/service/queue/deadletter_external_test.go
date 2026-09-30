package queue_test

import (
	"context"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kitsunium/sdk/internal/kernel/clock"
	"github.com/kitsunium/sdk/internal/kernel/errs"

	corequeue "github.com/kitsunium/sdk/internal/core/queue"
	svcqueue "github.com/kitsunium/sdk/internal/service/queue"
)

// growingPolicy doubles the retry delay from testRetryDelay up to three times
// it, over enough attempts to reach the ceiling twice.
func growingPolicy() corequeue.PolicyValue {
	return corequeue.PolicyValue{
		VisibilityTimeout: testVisibility, RetryDelay: testRetryDelay,
		MaxRetryDelay: 3 * testRetryDelay, MaxDeliveries: 5,
	}
}

// TestAGrowingRetryDelayFollowsTheBackoffCurve pins MaxRetryDelay on every
// broker (ADR 0151): the message nacked on its n-th delivery waits
// RetryDelay × 2^(n−1), held at the ceiling — 2 s, 4 s, then 6 s twice — and
// is receivable at exactly that instant and not a nanosecond before.
func TestAGrowingRetryDelayFollowsTheBackoffCurve(t *testing.T) {
	t.Parallel()
	want := []time.Duration{testRetryDelay, 2 * testRetryDelay, 3 * testRetryDelay, 3 * testRetryDelay}
	for _, factory := range everyBroker() {
		t.Run(factory.name, func(t *testing.T) {
			t.Parallel()
			clk := clock.NewManualClock(epoch)
			broker := factory.make(t, clk, growingPolicy())
			publish(t, broker, "flaky downstream")
			for attempt, delay := range want {
				delivery := receiveOne(t, broker)
				if delivery.Deliveries != attempt+1 {
					t.Fatalf("delivery %d counted %d", attempt+1, delivery.Deliveries)
				}
				verdict := nack(t, broker, delivery.Lease.Receipt, nil)
				if got := verdict.VisibleAt.Sub(clk.Now()); got != delay {
					t.Fatalf("after failure %d VisibleAt is now+%v, want now+%v", attempt+1, got, delay)
				}
				clk.Advance(delay - time.Nanosecond)
				receiveNone(t, broker)
				clk.Advance(time.Nanosecond)
			}
			if last := receiveOne(t, broker); last.Deliveries != len(want)+1 {
				t.Fatalf("the last delivery counted %d, want %d", last.Deliveries, len(want)+1)
			}
		})
	}
}

// TestARejectedMessageIsDeadLetteredAtOnceWithItsCause pins the Rejecter
// sibling on every broker: on its FIRST delivery, far from MaxDeliveries, the
// message goes to the dead-letter store with the handler's own reason, public
// words and code, at the count it had — and is never delivered again.
func TestARejectedMessageIsDeadLetteredAtOnceWithItsCause(t *testing.T) {
	t.Parallel()
	cause := errs.Define(0x00_03_35_FE, "ORDER_UNDECODABLE", "the order does not decode", "private half")
	for _, factory := range everyBroker() {
		t.Run(factory.name, func(t *testing.T) {
			t.Parallel()
			clk := clock.NewManualClock(epoch)
			broker := factory.make(t, clk, defaultPolicy())
			published := publish(t, broker, "not json")
			delivery := receiveOne(t, broker)

			if err := rejecter(t, broker).Reject(t.Context(), delivery.Lease.Receipt, corequeue.DoNotRetry(cause)); err != nil {
				t.Fatalf("Reject() = %v, want nil", err)
			}
			clk.Advance(testVisibility * 10)
			receiveNone(t, broker)

			dead := deadLetters(t, broker, 10)
			if len(dead) != 1 {
				t.Fatalf("DeadLetters() returned %d records, want 1", len(dead))
			}
			got := dead[0]
			if got.Message.ID != published.ID || string(got.Message.Payload) != "not json" {
				t.Fatalf("dead letter = %+v, want the published message", got.Message)
			}
			if got.Deliveries != 1 {
				t.Errorf("Deliveries = %d, want the 1 it had, below MaxDeliveries", got.Deliveries)
			}
			if got.Reason != "ORDER_UNDECODABLE" || got.Cause != "the order does not decode" || got.Code != 0x00_03_35_FE {
				t.Errorf("dead letter cause = %q %q %#x, want the handler's own — the mark must not displace it",
					got.Reason, got.Cause, got.Code)
			}
		})
	}
}

// TestRejectRefusesWhatAckRefuses pins the sibling's refusals on every broker:
// a lapsed lease is LEASE_EXPIRED and a receipt the broker never issued is
// UNKNOWN_RECEIPT, and neither moves the message.
func TestRejectRefusesWhatAckRefuses(t *testing.T) {
	t.Parallel()
	for _, factory := range everyBroker() {
		t.Run(factory.name, func(t *testing.T) {
			t.Parallel()
			clk := clock.NewManualClock(epoch)
			broker := factory.make(t, clk, defaultPolicy())
			publish(t, broker, "slow")
			stale := receiveOne(t, broker)
			clk.Advance(testVisibility)

			err := rejecter(t, broker).Reject(t.Context(), stale.Lease.Receipt, nil)
			if !errs.HasCode(err, corequeue.CodeLeaseExpired) {
				t.Fatalf("Reject(lapsed) = %v, want CodeLeaseExpired", err)
			}
			if forged := rejecter(t, broker).Reject(t.Context(), "nonsense", nil); !errs.HasCode(forged, corequeue.CodeUnknownReceipt) {
				t.Fatalf("Reject(forged) = %v, want CodeUnknownReceipt", forged)
			}
			//: nothing moved: the message is receivable, its count moved on.
			if again := receiveOne(t, broker); again.Deliveries != 2 {
				t.Fatalf("redelivery counted %d, want 2", again.Deliveries)
			}
			if dead := deadLetters(t, broker, 10); len(dead) != 0 {
				t.Fatalf("a refused Reject left %d dead letters", len(dead))
			}
		})
	}
}

// TestAReplayedDeadLetterComesBackAsItsFirstDelivery pins ReplayDeadLetter on
// every broker: the dead letter leaves the store, and the message is
// receivable at once under its own identifier, payload and enqueue instant,
// its count reset — so it has its whole attempt budget again.
func TestAReplayedDeadLetterComesBackAsItsFirstDelivery(t *testing.T) {
	t.Parallel()
	for _, factory := range everyBroker() {
		t.Run(factory.name, func(t *testing.T) {
			t.Parallel()
			clk := clock.NewManualClock(epoch)
			broker := factory.make(t, clk, corequeue.PolicyValue{VisibilityTimeout: testVisibility, MaxDeliveries: 2})
			published := publish(t, broker, "fixed downstream")
			for range 2 {
				nack(t, broker, receiveOne(t, broker).Lease.Receipt, nil)
			}
			dead := deadLetters(t, broker, 10)
			if len(dead) != 1 {
				t.Fatalf("DeadLetters() returned %d records, want 1", len(dead))
			}
			clk.Advance(time.Minute)

			if err := manager(t, broker).ReplayDeadLetter(t.Context(), dead[0].Message.ID); err != nil {
				t.Fatalf("ReplayDeadLetter() = %v, want nil", err)
			}
			if left := deadLetters(t, broker, 10); len(left) != 0 {
				t.Fatalf("the replayed dead letter is still in the store: %+v", left)
			}
			again := receiveOne(t, broker)
			if again.Deliveries != 1 {
				t.Errorf("the replayed message counted %d, want 1 — its count is reset", again.Deliveries)
			}
			if again.Message.ID != published.ID || string(again.Message.Payload) != "fixed downstream" {
				t.Errorf("replayed %+v, want the published message under its own ID", again.Message)
			}
			if !again.Message.EnqueuedAt.Equal(published.EnqueuedAt) {
				t.Errorf("EnqueuedAt = %v, want the original %v", again.Message.EnqueuedAt, published.EnqueuedAt)
			}
			//: the whole budget again: one failure is a retry, not a death.
			if verdict := nack(t, broker, again.Lease.Receipt, nil); verdict.DeadLettered {
				t.Error("the replayed message was dead-lettered on its first failure; its budget was not reset")
			}
		})
	}
}

// TestADeletedDeadLetterIsGoneAndASecondDecisionIsRefused pins
// DeleteDeadLetter on every broker, and DEAD_LETTER_NOT_FOUND for whatever
// comes after it — a second deletion, a replay, or an identifier nobody
// minted.
func TestADeletedDeadLetterIsGoneAndASecondDecisionIsRefused(t *testing.T) {
	t.Parallel()
	for _, factory := range everyBroker() {
		t.Run(factory.name, func(t *testing.T) {
			t.Parallel()
			clk := clock.NewManualClock(epoch)
			broker := factory.make(t, clk, corequeue.PolicyValue{VisibilityTimeout: testVisibility, MaxDeliveries: 1})
			publish(t, broker, "obsolete")
			nack(t, broker, receiveOne(t, broker).Lease.Receipt, nil)
			id := deadLetters(t, broker, 10)[0].Message.ID

			if err := manager(t, broker).DeleteDeadLetter(t.Context(), id); err != nil {
				t.Fatalf("DeleteDeadLetter() = %v, want nil", err)
			}
			if left := deadLetters(t, broker, 10); len(left) != 0 {
				t.Fatalf("the deleted dead letter is still in the store: %+v", left)
			}
			for name, decide := range map[string]func(context.Context, string) error{
				"a second DeleteDeadLetter": manager(t, broker).DeleteDeadLetter,
				"a ReplayDeadLetter":        manager(t, broker).ReplayDeadLetter,
			} {
				if err := decide(t.Context(), id); !errs.HasCode(err, corequeue.CodeDeadLetterNotFound) {
					t.Errorf("%s = %v, want CodeDeadLetterNotFound", name, err)
				}
			}
			for _, forged := range []string{"", "nonsense", "../../etc/passwd", "0000000000000000001-zz"} {
				if err := manager(t, broker).ReplayDeadLetter(t.Context(), forged); !errs.HasCode(err, corequeue.CodeDeadLetterNotFound) {
					t.Errorf("ReplayDeadLetter(%q) = %v, want CodeDeadLetterNotFound", forged, err)
				}
			}
			receiveNone(t, broker)
		})
	}
}

// TestAReplayWakesAnIdleConsumer pins that a replay is an event a consumer
// asleep on its Waker hears, on every broker: the clock never moves and the
// poll is an hour, so only the wake can deliver the replayed message.
func TestAReplayWakesAnIdleConsumer(t *testing.T) {
	t.Parallel()
	for _, factory := range everyBroker() {
		t.Run(factory.name, func(t *testing.T) {
			t.Parallel()
			clk := clock.NewManualClock(epoch)
			broker := factory.make(t, clk, corequeue.PolicyValue{VisibilityTimeout: testVisibility, MaxDeliveries: 1})
			publish(t, broker, "replay me")
			nack(t, broker, receiveOne(t, broker).Lease.Receipt, nil)
			id := deadLetters(t, broker, 10)[0].Message.ID

			seen := consumer(t, broker, clk, accept)
			clk.BlockUntil(1)
			if err := manager(t, broker).ReplayDeadLetter(t.Context(), id); err != nil {
				t.Fatalf("ReplayDeadLetter() = %v, want nil", err)
			}
			if got := next(t, seen); got.Message.ID != id || got.Deliveries != 1 {
				t.Errorf("delivered %+v, want the replayed message on its first delivery", got)
			}
		})
	}
}

// TestAHandlerThatSaysDoNotRetryIsDeadLetteredOnItsFirstFailure drives the
// whole path through Consume on every broker: the handler's DoNotRetry is
// recognised, the broker's Rejecter is reached, and the message is in the
// dead-letter store after ONE attempt of the five the policy allows — with the
// handler's own reason.
func TestAHandlerThatSaysDoNotRetryIsDeadLetteredOnItsFirstFailure(t *testing.T) {
	t.Parallel()
	cause := errs.Define(0x00_03_35_FD, "PAYLOAD_REFUSED", "the payload is refused", "private half")
	for _, factory := range everyBroker() {
		t.Run(factory.name, func(t *testing.T) {
			t.Parallel()
			clk := clock.NewManualClock(epoch)
			broker := factory.make(t, clk, growingPolicy())
			var attempts atomic.Int64
			consumer(t, broker, clk, func(context.Context, corequeue.DeliveryValue) error {
				attempts.Add(1)
				return corequeue.DoNotRetry(cause)
			})
			publish(t, broker, "poison")

			dead := awaitDeadLetter(t, broker)
			if dead.Reason != "PAYLOAD_REFUSED" || dead.Deliveries != 1 {
				t.Errorf("dead letter = %q after %d deliveries, want PAYLOAD_REFUSED after 1", dead.Reason, dead.Deliveries)
			}
			if got := attempts.Load(); got != 1 {
				t.Errorf("the handler ran %d times, want once", got)
			}
		})
	}
}

// TestABrokerThatCannotRejectRetriesADoNotRetryFailure pins the fallback: a
// broker without the Rejecter sibling — a connector of the caller's making —
// is nacked, so the message is retried until MaxDeliveries and dead-lettered
// with the same cause. The shortcut is lost, never the message.
func TestABrokerThatCannotRejectRetriesADoNotRetryFailure(t *testing.T) {
	t.Parallel()
	inner := makeMemory(t, clock.System, corequeue.PolicyValue{VisibilityTimeout: time.Minute, MaxDeliveries: testDeliveries})
	broker := portOnly{Broker: inner}
	if _, rejects := corequeue.Broker(broker).(corequeue.Rejecter); rejects {
		t.Fatal("the wrapper still exposes Rejecter; the fallback is not being tested")
	}
	publish(t, broker, "poison")
	var attempts atomic.Int64
	runEngine(t, broker, svcqueue.ConsumerConfig{
		Handler: func(context.Context, corequeue.DeliveryValue) error {
			attempts.Add(1)
			return corequeue.DoNotRetry(nil)
		},
		HandlerIsIdempotent: true, PollInterval: fastPoll,
	}, func() bool { return attempts.Load() == int64(testDeliveries) })

	dead := deadLetters(t, inner, 10)
	if len(dead) != 1 || dead[0].Reason != "NOT_RETRYABLE" || dead[0].Deliveries != testDeliveries {
		t.Fatalf("dead letters = %+v, want one NOT_RETRYABLE after %d deliveries", dead, testDeliveries)
	}
}

// portOnly exposes a broker's four frozen methods and none of its siblings.
type portOnly struct{ corequeue.Broker }

// awaitDeadLetter waits, on the wall clock, for the broker's one dead letter.
func awaitDeadLetter(t *testing.T, broker corequeue.Broker) corequeue.DeadLetterValue {
	t.Helper()
	deadline := time.Now().Add(engineBudget)
	for time.Now().Before(deadline) {
		if dead := deadLetters(t, broker, 10); len(dead) > 0 {
			return dead[0]
		}
		time.Sleep(fastPoll)
	}
	t.Fatalf("no dead letter within %v", engineBudget)
	return corequeue.DeadLetterValue{}
}

// rejecter asserts the Rejecter capability and returns it.
func rejecter(t *testing.T, broker corequeue.Broker) corequeue.Rejecter {
	t.Helper()
	capability, ok := broker.(corequeue.Rejecter)
	if !ok {
		t.Fatalf("%T does not implement queue.Rejecter", broker)
	}
	return capability
}

// manager asserts the DeadLetterManager capability and returns it.
func manager(t *testing.T, broker corequeue.Broker) corequeue.DeadLetterManager {
	t.Helper()
	capability, ok := broker.(corequeue.DeadLetterManager)
	if !ok {
		t.Fatalf("%T does not implement queue.DeadLetterManager", broker)
	}
	return capability
}

// TestTheFileBrokerNeverReplaysARecordItCannotVouchFor pins the file broker's
// replay against a record whose file name is a dead letter's and whose content
// is not the message it names — truncated by a filesystem that lost it, or
// planted: nothing is queued, and the identifier is refused as unknown.
func TestTheFileBrokerNeverReplaysARecordItCannotVouchFor(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	broker := newFileBroker(t, dir, defaultPolicy())
	const id = "0000000000000000001-0123456789abcdef"
	record := filepath.Join(dir, "dead", "0000000000000000001.0123456789abcdef.003.dead")
	if err := os.WriteFile(record, []byte("ktnq/1\n\n"), 0o600); err != nil {
		t.Fatalf("WriteFile() = %v", err)
	}
	if err := manager(t, broker).ReplayDeadLetter(t.Context(), id); !errs.HasCode(err, corequeue.CodeDeadLetterNotFound) {
		t.Fatalf("ReplayDeadLetter(a record naming no message) = %v, want CodeDeadLetterNotFound", err)
	}
	receiveNone(t, broker)
}
