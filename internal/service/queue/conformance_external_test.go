package queue_test

import (
	"errors"
	"math"
	"sync"
	"testing"
	"time"

	"github.com/kitsunium/sdk/internal/kernel/clock"
	"github.com/kitsunium/sdk/internal/kernel/errs"

	corequeue "github.com/kitsunium/sdk/internal/core/queue"
	svcqueue "github.com/kitsunium/sdk/internal/service/queue"
)

// TestAMessageIsRemovedAtAcknowledgementAndNotAtRead is the domain's headline
// contract, asserted on both brokers: Receive LEASES, Ack REMOVES, and
// nothing in between removes anything.
func TestAMessageIsRemovedAtAcknowledgementAndNotAtRead(t *testing.T) {
	t.Parallel()
	for _, factory := range bothBrokers() {
		t.Run(factory.name, func(t *testing.T) {
			t.Parallel()
			clk := clock.NewManualClock(epoch)
			broker := factory.make(t, clk, defaultPolicy())
			published := publish(t, broker, "work")

			delivery := receiveOne(t, broker)
			if delivery.Deliveries != 1 {
				t.Fatalf("Deliveries = %d, want 1 on the first delivery", delivery.Deliveries)
			}
			if string(delivery.Message.Payload) != "work" {
				t.Fatalf("Payload = %q, want %q", delivery.Message.Payload, "work")
			}
			if delivery.Message.ID != published.ID {
				t.Fatalf("delivered ID %q, want the published %q", delivery.Message.ID, published.ID)
			}
			//: still leased, so nobody else sees it.
			receiveNone(t, broker)

			if err := broker.Ack(t.Context(), delivery.Lease.Receipt); err != nil {
				t.Fatalf("Ack() = %v, want nil", err)
			}
			//: and now it is gone for good — the visibility timeout elapsing
			//: must not bring an ACKNOWLEDGED message back.
			clk.Advance(testVisibility * 4)
			receiveNone(t, broker)
		})
	}
}

// TestALapsedLeaseRedeliversTheMessageWithTheCountIncremented is the
// at-least-once mechanism itself, expressed without killing anything: a
// consumer that neither acks nor nacks loses the message to the clock.
//
// It is NOT the consumer-death proof — that is
// TestAKilledConsumerLosesItsLeaseAndTheMessageComesBack, which uses a real
// subprocess and a real SIGKILL. This one pins the mechanism the death test
// then exercises for real.
func TestALapsedLeaseRedeliversTheMessageWithTheCountIncremented(t *testing.T) {
	t.Parallel()
	for _, factory := range bothBrokers() {
		t.Run(factory.name, func(t *testing.T) {
			t.Parallel()
			clk := clock.NewManualClock(epoch)
			broker := factory.make(t, clk, defaultPolicy())
			publish(t, broker, "work")

			first := receiveOne(t, broker)
			//: one nanosecond before the deadline, the lease still holds.
			clk.Advance(testVisibility - time.Nanosecond)
			receiveNone(t, broker)

			clk.Advance(time.Nanosecond)
			second := receiveOne(t, broker)
			if second.Deliveries != 2 {
				t.Fatalf("Deliveries = %d after a lapsed lease, want 2", second.Deliveries)
			}
			if second.Message.ID != first.Message.ID {
				t.Fatalf("redelivered %q, want the same message %q", second.Message.ID, first.Message.ID)
			}
			if second.Lease.Receipt == first.Lease.Receipt {
				t.Fatal("the redelivery reused the first receipt; two deliveries must carry two leases")
			}
		})
	}
}

// TestAnAcknowledgementFromALapsedLeaseIsRefusedAndRemovesNothing is the rule
// core/lock makes about releasing a lock one no longer holds, one domain
// over: a consumer that lost the race must not be able to end the new
// holder's turn, and must find out that it lost.
func TestAnAcknowledgementFromALapsedLeaseIsRefusedAndRemovesNothing(t *testing.T) {
	t.Parallel()
	for _, factory := range bothBrokers() {
		t.Run(factory.name, func(t *testing.T) {
			t.Parallel()
			clk := clock.NewManualClock(epoch)
			broker := factory.make(t, clk, defaultPolicy())
			publish(t, broker, "work")

			stale := receiveOne(t, broker)
			clk.Advance(testVisibility)
			//: somebody else now holds it.
			fresh := receiveOne(t, broker)

			err := broker.Ack(t.Context(), stale.Lease.Receipt)
			if !errs.HasCode(err, corequeue.CodeLeaseExpired) {
				t.Fatalf("Ack(stale) = %v, want CodeLeaseExpired", err)
			}
			//: and the new holder's message is untouched.
			if ackErr := broker.Ack(t.Context(), fresh.Lease.Receipt); ackErr != nil {
				t.Fatalf("Ack(fresh) = %v, want nil", ackErr)
			}
		})
	}
}

// TestAReceiptThisQueueNeverIssuedIsUnknownRatherThanExpired pins the two
// refusals apart. "You were too slow" and "this was never yours" are
// different bugs and collapsing them would send every reader looking for the
// wrong one half the time.
func TestAReceiptThisQueueNeverIssuedIsUnknownRatherThanExpired(t *testing.T) {
	t.Parallel()
	for _, factory := range bothBrokers() {
		t.Run(factory.name, func(t *testing.T) {
			t.Parallel()
			broker := factory.make(t, clock.NewManualClock(epoch), defaultPolicy())
			for _, forged := range []corequeue.ReceiptValue{"", "nonsense", "../../etc/passwd", "a.b.c.d.e"} {
				err := broker.Ack(t.Context(), forged)
				if !errs.HasCode(err, corequeue.CodeUnknownReceipt) {
					t.Fatalf("Ack(%q) = %v, want CodeUnknownReceipt", forged, err)
				}
			}
		})
	}
}

// TestANackRetriesUntilTheBudgetIsSpentAndThenDeadLetters walks the whole
// failure path and checks the two things a dead letter is worth having for:
// that it holds the message, and that it holds WHY.
func TestANackRetriesUntilTheBudgetIsSpentAndThenDeadLetters(t *testing.T) {
	t.Parallel()
	for _, factory := range bothBrokers() {
		t.Run(factory.name, func(t *testing.T) {
			t.Parallel()
			clk := clock.NewManualClock(epoch)
			broker := factory.make(t, clk, defaultPolicy())
			published := publish(t, broker, "poison")
			cause := errs.Define(0x00_02_17_FF, "TEST_CAUSE", "the downstream refused", "private half")

			for attempt := 1; attempt < testDeliveries; attempt++ {
				delivery := receiveOne(t, broker)
				if delivery.Deliveries != attempt {
					t.Fatalf("Deliveries = %d on attempt %d", delivery.Deliveries, attempt)
				}
				verdict := nack(t, broker, delivery.Lease.Receipt, cause)
				if verdict.DeadLettered {
					t.Fatalf("dead-lettered on attempt %d of %d", attempt, testDeliveries)
				}
				//: the retry delay is real: nothing is visible before it elapses.
				receiveNone(t, broker)
				clk.Advance(testRetryDelay)
			}

			last := receiveOne(t, broker)
			verdict := nack(t, broker, last.Lease.Receipt, cause)
			if !verdict.DeadLettered {
				t.Fatalf("attempt %d was not dead-lettered", testDeliveries)
			}
			if !verdict.VisibleAt.IsZero() {
				t.Fatalf("VisibleAt = %v on a dead letter, want the zero Time", verdict.VisibleAt)
			}
			//: and never again, however long anyone waits.
			clk.Advance(testVisibility * 10)
			receiveNone(t, broker)

			assertDeadLetter(t, broker, published.ID, "poison")
		})
	}
}

// assertDeadLetter checks the record kept for an abandoned message.
func assertDeadLetter(t *testing.T, broker corequeue.Broker, wantID, wantPayload string) {
	t.Helper()
	dead := deadLetters(t, broker, 10)
	if len(dead) != 1 {
		t.Fatalf("DeadLetters() returned %d records, want 1", len(dead))
	}
	record := dead[0]
	if record.Message.ID != wantID {
		t.Fatalf("dead letter ID = %q, want %q", record.Message.ID, wantID)
	}
	if string(record.Message.Payload) != wantPayload {
		t.Fatalf("dead letter payload = %q, want %q", record.Message.Payload, wantPayload)
	}
	if record.Deliveries != testDeliveries {
		t.Fatalf("dead letter Deliveries = %d, want %d", record.Deliveries, testDeliveries)
	}
	if record.Reason != "TEST_CAUSE" {
		t.Fatalf("dead letter Reason = %q, want TEST_CAUSE — a dead letter without its cause is an "+
			"investigation nobody can run", record.Reason)
	}
	if record.Cause != "the downstream refused" {
		t.Fatalf("dead letter Cause = %q, want the PUBLIC half", record.Cause)
	}
	if record.Cause == "private half" {
		t.Fatal("the dead letter kept the log-only Private half; rule 4 says it must not")
	}
	if record.Code != 0x00_02_17_FF {
		t.Fatalf("dead letter Code = %#x, want the cause's dotted quad", record.Code)
	}
}

// TestAMessageThatNobodyEverNacksStillReachesTheDeadLetterStore is the crash
// path's terminal state: a payload that kills every consumer that touches it
// cannot loop forever, because a lapsed lease is counted exactly as a nack
// is.
func TestAMessageThatNobodyEverNacksStillReachesTheDeadLetterStore(t *testing.T) {
	t.Parallel()
	for _, factory := range bothBrokers() {
		t.Run(factory.name, func(t *testing.T) {
			t.Parallel()
			clk := clock.NewManualClock(epoch)
			broker := factory.make(t, clk, defaultPolicy())
			publish(t, broker, "kills-the-process")

			for range testDeliveries {
				receiveOne(t, broker)
				clk.Advance(testVisibility)
			}
			//: the reclaim that buries it happens on the next look.
			receiveNone(t, broker)

			dead := deadLetters(t, broker, 10)
			if len(dead) != 1 {
				t.Fatalf("DeadLetters() returned %d records, want 1", len(dead))
			}
			if dead[0].Reason != "LEASE_EXPIRED" {
				t.Fatalf("Reason = %q, want LEASE_EXPIRED — nobody ever reported a cause", dead[0].Reason)
			}
		})
	}
}

// TestReadingTheDeadLetterStoreDoesNotConsumeIt pins the decision: a dead
// letter is evidence, and evidence a read consumes is evidence the second
// investigator does not get.
func TestReadingTheDeadLetterStoreDoesNotConsumeIt(t *testing.T) {
	t.Parallel()
	for _, factory := range bothBrokers() {
		t.Run(factory.name, func(t *testing.T) {
			t.Parallel()
			clk := clock.NewManualClock(epoch)
			broker := factory.make(t, clk, corequeue.PolicyValue{
				VisibilityTimeout: testVisibility, MaxDeliveries: 1,
			})
			publish(t, broker, "one-shot")
			nack(t, broker, receiveOne(t, broker).Lease.Receipt, errors.New("test failure")) //nolint:err113

			for round := range 3 {
				if got := len(deadLetters(t, broker, 10)); got != 1 {
					t.Fatalf("read %d returned %d records, want 1", round, got)
				}
			}
		})
	}
}

// TestAReaderThatEditsADeadLetterPayloadDoesNotRewriteTheStore holds the dead-letter
// read to the port's ownership rule: MessageValue.Payload "belongs to the
// receiver", and a dead letter is evidence, so an investigator that scribbles
// over the bytes it was handed must not change what the next one reads. Two
// records, so the copy is shown to be per entry and the order to survive it.
//
// Seen failing: with the memory broker's DeadLetters copying only the OUTER
// slice, the memory case printed
//
//	dead letter 0 after the first reader edited its copy = "XXXXX", want "first" — the
//	read handed out the bytes the store keeps
//
// while the file case, which re-reads every record from disk, passed.
func TestAReaderThatEditsADeadLetterPayloadDoesNotRewriteTheStore(t *testing.T) {
	t.Parallel()
	want := []string{"first", "second"}
	for _, factory := range bothBrokers() {
		t.Run(factory.name, func(t *testing.T) {
			t.Parallel()
			clk := clock.NewManualClock(epoch)
			broker := factory.make(t, clk, corequeue.PolicyValue{
				VisibilityTimeout: testVisibility, MaxDeliveries: 1,
			})
			for _, payload := range want {
				publish(t, broker, payload)
				//: distinct enqueue instants, so the file broker's name order
				//: — instant, then entropy — is the publication order.
				clk.Advance(time.Second)
			}
			for range want {
				nack(t, broker, receiveOne(t, broker).Lease.Receipt, nil)
			}

			for _, record := range deadLetters(t, broker, 10) {
				//: every byte the first reader was handed.
				for index := range record.Message.Payload {
					record.Message.Payload[index] = 'X'
				}
			}
			again := deadLetters(t, broker, 10)
			if len(again) != len(want) {
				t.Fatalf("DeadLetters() returned %d records, want %d", len(again), len(want))
			}
			for index, record := range again {
				if got := string(record.Message.Payload); got != want[index] {
					t.Fatalf("dead letter %d after the first reader edited its copy = %q, want %q — the "+
						"read handed out the bytes the store keeps", index, got, want[index])
				}
			}
		})
	}
}

// TestANackWithNoCauseIsRecordedAsUnreported keeps "it did not work and I
// cannot say why" a real answer rather than an empty string a reader would
// take for a bug in the SDK.
func TestANackWithNoCauseIsRecordedAsUnreported(t *testing.T) {
	t.Parallel()
	for _, factory := range bothBrokers() {
		t.Run(factory.name, func(t *testing.T) {
			t.Parallel()
			broker := factory.make(t, clock.NewManualClock(epoch), corequeue.PolicyValue{
				VisibilityTimeout: testVisibility, MaxDeliveries: 1,
			})
			publish(t, broker, "silent")
			nack(t, broker, receiveOne(t, broker).Lease.Receipt, nil)

			dead := deadLetters(t, broker, 10)
			if len(dead) != 1 || dead[0].Reason != "UNREPORTED" {
				t.Fatalf("dead letters = %+v, want one record with Reason UNREPORTED", dead)
			}
		})
	}
}

// TestExtendRenewsTheLeaseAndVoidsTheOldReceipt pins the ADR 0039 sibling and
// the one thing about it that surprises: renewing MINTS a new receipt,
// because on the durable broker the deadline lives in the name and the only
// atomic way to change a name is to replace it.
func TestExtendRenewsTheLeaseAndVoidsTheOldReceipt(t *testing.T) {
	t.Parallel()
	for _, factory := range bothBrokers() {
		t.Run(factory.name, func(t *testing.T) {
			t.Parallel()
			clk := clock.NewManualClock(epoch)
			broker := factory.make(t, clk, defaultPolicy())
			publish(t, broker, "slow")
			delivery := receiveOne(t, broker)

			renewed, err := extender(t, broker).Extend(t.Context(), delivery.Lease.Receipt, time.Hour)
			if err != nil {
				t.Fatalf("Extend() = %v, want nil", err)
			}
			if renewed.Receipt == delivery.Lease.Receipt {
				t.Fatal("Extend reused the receipt; the deadline is part of the lease's identity")
			}
			//: well past the ORIGINAL deadline, and still held.
			clk.Advance(testVisibility * 2)
			receiveNone(t, broker)

			if ackErr := broker.Ack(t.Context(), renewed.Receipt); ackErr != nil {
				t.Fatalf("Ack(renewed) = %v, want nil", ackErr)
			}
		})
	}
}

// TestExtendRefusesALapsedLeaseEvenWhenNobodyHasTakenIt pins expiry as a
// DEADLINE rather than as "until somebody else wants it". The handler that
// asks and is refused has learnt that its work is now a duplicate, which is
// exactly when it needed to know.
func TestExtendRefusesALapsedLeaseEvenWhenNobodyHasTakenIt(t *testing.T) {
	t.Parallel()
	for _, factory := range bothBrokers() {
		t.Run(factory.name, func(t *testing.T) {
			t.Parallel()
			clk := clock.NewManualClock(epoch)
			broker := factory.make(t, clk, defaultPolicy())
			publish(t, broker, "slow")
			delivery := receiveOne(t, broker)
			clk.Advance(testVisibility)

			_, err := extender(t, broker).Extend(t.Context(), delivery.Lease.Receipt, time.Hour)
			if !errs.HasCode(err, corequeue.CodeLeaseExpired) {
				t.Fatalf("Extend(lapsed) = %v, want CodeLeaseExpired", err)
			}
		})
	}
}

// TestExtendRefusesANonPositiveRenewal is ADR 0031 on the sibling: "lapse
// now" and "never lapse" are opposites, so neither is guessed.
func TestExtendRefusesANonPositiveRenewal(t *testing.T) {
	t.Parallel()
	for _, factory := range bothBrokers() {
		t.Run(factory.name, func(t *testing.T) {
			t.Parallel()
			broker := factory.make(t, clock.NewManualClock(epoch), defaultPolicy())
			publish(t, broker, "slow")
			delivery := receiveOne(t, broker)
			for _, by := range []time.Duration{0, -time.Second} {
				_, err := extender(t, broker).Extend(t.Context(), delivery.Lease.Receipt, by)
				if !errs.HasCode(err, corequeue.CodeQueueMisconfigured) {
					t.Fatalf("Extend(by=%v) = %v, want CodeQueueMisconfigured", by, err)
				}
			}
		})
	}
}

// TestABatchIsBoundedAndANonPositiveOneIsRefused covers both halves of the
// batch contract, including ADR 0031's reason for the refusal: an empty slice
// forever is a consumer loop that spins and looks exactly like an idle queue.
func TestABatchIsBoundedAndANonPositiveOneIsRefused(t *testing.T) {
	t.Parallel()
	for _, factory := range bothBrokers() {
		t.Run(factory.name, func(t *testing.T) {
			t.Parallel()
			broker := factory.make(t, clock.NewManualClock(epoch), defaultPolicy())
			for index := range 5 {
				publish(t, broker, string(rune('a'+index)))
			}
			if got := len(receive(t, broker, 3)); got != 3 {
				t.Fatalf("Receive(3) returned %d, want 3", got)
			}
			for _, max := range []int{0, -1} {
				_, err := broker.Receive(t.Context(), max)
				if !errs.HasCode(err, corequeue.CodeInvalidBatchSize) {
					t.Fatalf("Receive(%d) = %v, want CodeInvalidBatchSize", max, err)
				}
			}
		})
	}
}

// TestAnOversizedPayloadIsRefusedAtTheProducer pins where the bound is
// enforced: at the only place the payload can still be made smaller. The
// error carries the two SIZES and never the bytes, which is checked too.
func TestAnOversizedPayloadIsRefusedAtTheProducer(t *testing.T) {
	t.Parallel()
	const bound int = 16
	for _, factory := range bothBrokers() {
		t.Run(factory.name, func(t *testing.T) {
			t.Parallel()
			broker := factory.make(t, clock.NewManualClock(epoch), corequeue.PolicyValue{
				VisibilityTimeout: testVisibility, MaxDeliveries: 1, MaxMessageBytes: bound,
			})
			secret := "SUPERSECRETPAYLOADTHATMUSTNOTLEAK"
			_, err := broker.Publish(t.Context(), []byte(secret))
			if !errs.HasCode(err, corequeue.CodeMessageTooLarge) {
				t.Fatalf("Publish(oversized) = %v, want CodeMessageTooLarge", err)
			}
			if rendered := err.Error(); containsSubstring(rendered, secret) {
				t.Fatalf("the error rendered the payload: %q", rendered)
			}
			//: exactly at the bound is accepted; the refusal is strictly above.
			if _, okErr := broker.Publish(t.Context(), make([]byte, bound)); okErr != nil {
				t.Fatalf("Publish(exactly the bound) = %v, want nil", okErr)
			}
		})
	}
}

// TestAnEmptyPayloadIsALegitimateMessage keeps "the fact that it arrived is
// the whole meaning" a thing a caller may send.
func TestAnEmptyPayloadIsALegitimateMessage(t *testing.T) {
	t.Parallel()
	for _, factory := range bothBrokers() {
		t.Run(factory.name, func(t *testing.T) {
			t.Parallel()
			broker := factory.make(t, clock.NewManualClock(epoch), defaultPolicy())
			publish(t, broker, "")
			if got := len(receiveOne(t, broker).Message.Payload); got != 0 {
				t.Fatalf("payload length = %d, want 0", got)
			}
		})
	}
}

// TestBothBrokersRefuseTheSamePolicies is what makes the memory broker an
// honest double: they share core/queue.PolicyValue.Validate, so a consumer's
// test that passes against one is not passing for a reason the other lacks.
func TestBothBrokersRefuseTheSamePolicies(t *testing.T) {
	t.Parallel()
	broken := corequeue.PolicyValue{MaxDeliveries: 3}
	if _, err := svcqueue.NewMemory(svcqueue.MemoryConfig{Policy: broken}); !errs.HasCode(
		err, corequeue.CodeQueueMisconfigured,
	) {
		t.Fatalf("NewMemory(broken) = %v, want CodeQueueMisconfigured", err)
	}
	if _, err := svcqueue.NewFile(svcqueue.FileConfig{
		Dir: t.TempDir(), Policy: broken,
	}); !errs.HasCode(err, corequeue.CodeQueueMisconfigured) {
		t.Fatalf("NewFile(broken) = %v, want CodeQueueMisconfigured", err)
	}
}

// TestBothBrokersRefuseADeadlineOffsetNoInstantCanCarry pins the upper bound
// Validate puts on the two durations every deadline is built from. A
// visibility timeout of math.MaxInt64 — the value somebody reaches for to mean
// "never" — is 292 years, so the durable broker's lease deadline now+timeout
// wrapped negative: pad wrote a sign, parseNano refused the name on the way
// back, and the leased message was stranded in inflight/ for good, never
// reclaimed, never delivered, its receipt reading UNKNOWN_RECEIPT. The retry
// delay did the same to a nacked message in ready/. Both brokers share the
// guard, so both refuse, which is what keeps the memory broker an honest
// double.
//
// Seen failing: with Validate's two new bounds removed, both cases printed,
// for the memory broker and the file broker alike,
//
//	NewMemory(VisibilityTimeout = math.MaxInt64) = <nil>, want CodeQueueMisconfigured
//	NewFile(RetryDelay = math.MaxInt64) = <nil>, want CodeQueueMisconfigured
func TestBothBrokersRefuseADeadlineOffsetNoInstantCanCarry(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		field  string
		policy corequeue.PolicyValue
	}{
		{"VisibilityTimeout = math.MaxInt64", "VisibilityTimeout", corequeue.PolicyValue{
			VisibilityTimeout: math.MaxInt64, MaxDeliveries: testDeliveries,
		}},
		{"RetryDelay = math.MaxInt64", "RetryDelay", corequeue.PolicyValue{
			VisibilityTimeout: testVisibility, RetryDelay: math.MaxInt64, MaxDeliveries: testDeliveries,
		}},
	}
	builders := []struct {
		name  string
		build func(t *testing.T, policy corequeue.PolicyValue) error
	}{
		{"NewMemory", func(_ *testing.T, policy corequeue.PolicyValue) error {
			_, err := svcqueue.NewMemory(svcqueue.MemoryConfig{Policy: policy, Clock: clock.NewManualClock(epoch)})
			return err
		}},
		{"NewFile", func(t *testing.T, policy corequeue.PolicyValue) error {
			_, err := svcqueue.NewFile(svcqueue.FileConfig{
				Dir: t.TempDir(), Policy: policy, Clock: clock.NewManualClock(epoch),
			})
			return err
		}},
	}
	for _, tc := range cases {
		for _, builder := range builders {
			t.Run(builder.name+"/"+tc.name, func(t *testing.T) {
				t.Parallel()
				err := builder.build(t, tc.policy)
				if !errs.HasCode(err, corequeue.CodeQueueMisconfigured) {
					t.Fatalf("%s(%s) = %v, want CodeQueueMisconfigured", builder.name, tc.name, err)
				}
				if got := fieldValue(err, "field"); got != tc.field {
					t.Errorf("field = %q, want %q", got, tc.field)
				}
			})
		}
	}
}

// TestBothBrokersRefuseAnExtensionANameCannotCarry is the same bound on the
// one duration Validate never sees: Extend's, which a handler chooses at
// runtime. Its deadline is now+by, written into the in-flight name like any
// other, so a by that carries it past 2262 would strand the message exactly as
// an unbounded visibility timeout did — and the refusal comes BEFORE anything
// is renamed, so the lease the handler already holds is untouched and still
// acknowledgeable.
//
// It runs on both brokers. The memory broker's time.Time could hold the
// deadline, but a double that accepted what the durable broker refuses would
// let a handler pass its tests and strand messages in production.
//
// Seen failing: with Extend's representability check removed from the file
// broker, and then from the memory one, each printed
//
//	Extend(by = math.MaxInt64) = <nil>, want CodeQueueMisconfigured
func TestBothBrokersRefuseAnExtensionANameCannotCarry(t *testing.T) {
	t.Parallel()
	for _, factory := range bothBrokers() {
		t.Run(factory.name, func(t *testing.T) {
			t.Parallel()
			clk := clock.NewManualClock(epoch)
			broker := factory.make(t, clk, defaultPolicy())
			publish(t, broker, "slow")
			delivery := receiveOne(t, broker)

			_, err := extender(t, broker).Extend(t.Context(), delivery.Lease.Receipt, math.MaxInt64)
			if !errs.HasCode(err, corequeue.CodeQueueMisconfigured) {
				t.Fatalf("Extend(by = math.MaxInt64) = %v, want CodeQueueMisconfigured", err)
			}
			if got := fieldValue(err, "field"); got != "Extend.by" {
				t.Errorf("field = %q, want %q", got, "Extend.by")
			}
			//: nothing moved: the lease the handler holds is still the one it has.
			if ackErr := broker.Ack(t.Context(), delivery.Lease.Receipt); ackErr != nil {
				t.Fatalf("Ack(original receipt) after the refused Extend = %v, want nil", ackErr)
			}
			//: and the largest extension a name CAN carry is honoured.
			publish(t, broker, "slower")
			again := receiveOne(t, broker)
			farthest := time.Unix(0, math.MaxInt64).Sub(clk.Now())
			renewed, extendErr := extender(t, broker).Extend(t.Context(), again.Lease.Receipt, farthest)
			if extendErr != nil {
				t.Fatalf("Extend(by = up to the last nameable instant) = %v, want nil", extendErr)
			}
			if ackErr := broker.Ack(t.Context(), renewed.Receipt); ackErr != nil {
				t.Fatalf("Ack(renewed) = %v, want nil — the renewed lease must read back", ackErr)
			}
		})
	}
}

// TestAnExtensionIsStoredAtTheInstantItWasChecked pins that Extend reads the
// clock once. The memory broker checked now+by against the range a name can
// carry and then stored a second now+by, so a clock that moved between the two
// readings stored a deadline past the one the check accepted — past what the
// durable broker can name, the very asymmetry the check exists to remove. The
// file broker already read once; both are held to it.
//
// Seen failing on the memory broker with the second reading restored: the
// renewed lease expired two seconds past the last nameable instant.
func TestAnExtensionIsStoredAtTheInstantItWasChecked(t *testing.T) {
	t.Parallel()
	last := time.Unix(0, math.MaxInt64)
	for _, factory := range bothBrokers() {
		t.Run(factory.name, func(t *testing.T) {
			t.Parallel()
			clk := &steppingClock{now: epoch, step: time.Second}
			broker := factory.make(t, clk, defaultPolicy())
			publish(t, broker, "slow")
			delivery := receiveOne(t, broker)
			//: up to the last nameable instant, measured from where the clock
			//: stands before it starts moving on every reading.
			by := last.Sub(epoch)
			clk.arm()
			renewed, err := extender(t, broker).Extend(t.Context(), delivery.Lease.Receipt, by)
			if err != nil {
				t.Fatalf("Extend(by = up to the last nameable instant) = %v, want nil", err)
			}
			if renewed.ExpiresAt.After(last) {
				t.Fatalf("the renewed lease expires at %v, past the last nameable instant %v its check was made against",
					renewed.ExpiresAt, last)
			}
		})
	}
}

// steppingClock moves on by step every time it is read once armed, so a
// broker that reads it twice in one call sees two different instants.
type steppingClock struct {
	mu    sync.Mutex
	now   time.Time
	step  time.Duration
	armed bool
}

// Now returns the current instant and, once armed, moves the clock on.
func (c *steppingClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.now
	if c.armed {
		c.now = c.now.Add(c.step)
	}
	return now
}

// Since is Now minus t.
func (c *steppingClock) Since(t time.Time) time.Duration {
	return c.Now().Sub(t)
}

// arm makes every later reading move the clock on.
func (c *steppingClock) arm() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.armed = true
}

// containsSubstring is strings.Contains, spelled locally so the assertion
// above reads as the security check it is.
func containsSubstring(haystack, needle string) bool {
	for index := 0; index+len(needle) <= len(haystack); index++ {
		if haystack[index:index+len(needle)] == needle {
			return true
		}
	}
	return false
}
