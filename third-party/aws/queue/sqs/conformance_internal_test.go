package sqs

import (
	"errors"
	"testing"
	"time"

	"github.com/kitsunium/sdk/internal/kernel/clock"
	"github.com/kitsunium/sdk/internal/kernel/errs"

	corequeue "github.com/kitsunium/sdk/internal/core/data/queue"
)

// The rows of internal/service/data/queue's conformance table that a broker
// over SQS can keep, restated until the importable queuetest suite exists
// (kitsunium/sdk#985). They run over the in-memory SQS below on a
// ManualClock in every lane, and over LocalStack on the wall clock under the
// `localstack` tag (localstack_test.go): a row moves time with env.advance,
// never with a sleep of its own.

// queueEnv is one broker under test and the way to move its time — the
// broker's clock and the server's together.
type queueEnv struct {
	broker  corequeue.Broker
	advance func(d time.Duration)
}

// envFactory builds a broker over fresh queues with policy.
type envFactory func(t *testing.T, policy corequeue.PolicyValue) queueEnv

// epoch anchors every ManualClock, so an instant in a failure reads as a date.
var epoch = time.Date(2026, time.November, 2, 12, 0, 0, 0, time.UTC)

// rowPolicy is the policy every row starts from: a lease long enough for a
// round trip to LocalStack, three deliveries, no retry delay.
func rowPolicy() corequeue.PolicyValue {
	return corequeue.PolicyValue{VisibilityTimeout: 2 * time.Second, MaxDeliveries: 3}
}

// fakeEnv builds the broker over the in-memory SQS on a ManualClock.
func fakeEnv(t *testing.T, policy corequeue.PolicyValue) queueEnv {
	t.Helper()
	clk := clock.NewManualClock(epoch)
	broker := newBroker(Config{QueueURL: "q", DeadLetterQueueURL: "dlq", Policy: policy, Clock: clk}, newFakeSQS(clk))
	return queueEnv{broker: broker, advance: clk.Advance}
}

var queueRows = []struct {
	name string
	run  func(t *testing.T, newEnv envFactory)
}{
	{"a message is removed at acknowledgement and not at read", rowRemovedAtAck},
	{"a lapsed lease redelivers with the count incremented", rowLapsedRedelivers},
	{"an ack from a lapsed lease is refused and removes nothing", rowLapsedAckRefused},
	{"a receipt this broker never issued is unknown", rowUnknownReceipt},
	{"a nack retries until the budget is spent and then dead-letters", rowNackDeadLetters},
	{"a message nobody nacks still reaches the dead-letter queue", rowLapsesDeadLetter},
	{"a nack with no cause is recorded as unreported", rowNilCause},
	{"Extend renews the lease and voids the old receipt", rowExtend},
	{"Extend refuses a lapsed lease", rowExtendLapsed},
	{"Extend refuses a non-positive renewal", rowExtendNonPositive},
	{"a batch is bounded and a non-positive one refused", rowBatch},
	{"an oversized payload is refused at the producer", rowOversized},
	{"an empty payload is a legitimate message", rowEmptyPayload},
	{"Reject dead-letters at once with the count it had", rowReject},
	{"NackAfter holds the message for the delay it names", rowNackAfter},
	{"PublishAt holds the message until its instant", rowPublishAt},
}

// runQueueRows runs every row over make.
func runQueueRows(t *testing.T, newEnv envFactory) {
	t.Helper()
	for _, row := range queueRows {
		t.Run(row.name, func(t *testing.T) { row.run(t, newEnv) })
	}
}

func TestTheFakeQueuePassesTheQueueConformanceRows(t *testing.T) {
	t.Parallel()
	runQueueRows(t, fakeEnv)
}

func publish(t *testing.T, env queueEnv, payload string) corequeue.MessageValue {
	t.Helper()
	message, err := env.broker.Publish(t.Context(), []byte(payload))
	if err != nil {
		t.Fatalf("Publish(%q) = %v, want nil", payload, err)
	}
	return message
}

func receive(t *testing.T, env queueEnv, max int) []corequeue.DeliveryValue {
	t.Helper()
	got, err := env.broker.Receive(t.Context(), max)
	if err != nil {
		t.Fatalf("Receive(%d) = %v, want nil", max, err)
	}
	return got
}

func receiveOne(t *testing.T, env queueEnv) corequeue.DeliveryValue {
	t.Helper()
	got := receive(t, env, 1)
	if len(got) != 1 {
		t.Fatalf("Receive = %d messages, want 1", len(got))
	}
	return got[0]
}

func expectEmpty(t *testing.T, env queueEnv) {
	t.Helper()
	if got := receive(t, env, MaxReceiveBatch); len(got) != 0 {
		t.Fatalf("Receive = %d messages, want none", len(got))
	}
}

func deadLetters(t *testing.T, env queueEnv) []corequeue.DeadLetterValue {
	t.Helper()
	reader, ok := env.broker.(corequeue.DeadLetterReader)
	if !ok {
		t.Fatal("the broker is no DeadLetterReader")
	}
	dead, err := reader.DeadLetters(t.Context(), 100)
	if err != nil {
		t.Fatalf("DeadLetters = %v, want nil", err)
	}
	return dead
}

func lapse(env queueEnv, policy corequeue.PolicyValue) {
	env.advance(policy.VisibilityTimeout + time.Second)
}

func rowRemovedAtAck(t *testing.T, newEnv envFactory) {
	policy := rowPolicy()
	env := newEnv(t, policy)
	sent := publish(t, env, "work")
	got := receiveOne(t, env)
	if got.Message.ID != sent.ID || string(got.Message.Payload) != "work" || got.Deliveries != 1 {
		t.Fatalf("Receive = %+v, want the published message, delivery 1", got)
	}
	expectEmpty(t, env)
	if err := env.broker.Ack(t.Context(), got.Lease.Receipt); err != nil {
		t.Fatalf("Ack = %v, want nil", err)
	}
	lapse(env, policy)
	expectEmpty(t, env)
}

func rowLapsedRedelivers(t *testing.T, newEnv envFactory) {
	policy := rowPolicy()
	env := newEnv(t, policy)
	sent := publish(t, env, "work")
	first := receiveOne(t, env)
	lapse(env, policy)
	second := receiveOne(t, env)
	if second.Message.ID != sent.ID || second.Deliveries != 2 || first.Deliveries != 1 {
		t.Fatalf("deliveries = (%d, %d) of %s, want (1, 2) of %s", first.Deliveries, second.Deliveries, second.Message.ID, sent.ID)
	}
}

func rowLapsedAckRefused(t *testing.T, newEnv envFactory) {
	policy := rowPolicy()
	env := newEnv(t, policy)
	publish(t, env, "work")
	first := receiveOne(t, env)
	lapse(env, policy)
	err := env.broker.Ack(t.Context(), first.Lease.Receipt)
	if !errors.Is(err, corequeue.LeaseExpired) || !errs.HasCode(err, CodeSQSLeaseLost) {
		t.Fatalf("Ack(lapsed) = %v, want LEASE_EXPIRED with LEASE_LOST in its trail", err)
	}
	if again := receiveOne(t, env); again.Deliveries != 2 {
		t.Fatalf("redelivery = %d, want 2: the refused ack removed something", again.Deliveries)
	}
}

func rowUnknownReceipt(t *testing.T, newEnv envFactory) {
	env := newEnv(t, rowPolicy())
	for _, receipt := range []corequeue.ReceiptValue{"", "never-issued", "sqs1.zz.0123456789abcdef1", "sqs1.1.0123456789abcdef1"} {
		if err := env.broker.Ack(t.Context(), receipt); !errors.Is(err, corequeue.UnknownReceipt) {
			t.Errorf("Ack(%q) = %v, want UNKNOWN_RECEIPT", receipt, err)
		}
	}
}

func rowNackDeadLetters(t *testing.T, newEnv envFactory) {
	policy := rowPolicy()
	env := newEnv(t, policy)
	sent := publish(t, env, "poison")
	cause := errs.Wrap(corequeue.NotRetryable, errs.WrapParams{})
	for attempt := 1; attempt <= policy.MaxDeliveries; attempt++ {
		got := receiveOne(t, env)
		verdict, err := env.broker.Nack(t.Context(), got.Lease.Receipt, cause)
		if err != nil || verdict.Deliveries != attempt || verdict.DeadLettered != (attempt == policy.MaxDeliveries) {
			t.Fatalf("Nack #%d = (%+v, %v), want dead-lettered only on the last", attempt, verdict, err)
		}
		env.advance(time.Second)
	}
	expectEmpty(t, env)
	dead := deadLetters(t, env)
	if len(dead) != 1 {
		t.Fatalf("DeadLetters = %d, want 1", len(dead))
	}
	letter := dead[0]
	if letter.Message.ID != sent.ID || string(letter.Message.Payload) != "poison" || letter.Deliveries != policy.MaxDeliveries ||
		letter.Reason != "NOT_RETRYABLE" || letter.Code != uint32(corequeue.CodeNotRetryable) || letter.Cause == "" {
		t.Fatalf("dead letter = %+v, want the message, its count and its cause", letter)
	}
	if !letter.Message.EnqueuedAt.Equal(sent.EnqueuedAt) {
		t.Fatalf("EnqueuedAt = %v, want %v", letter.Message.EnqueuedAt, sent.EnqueuedAt)
	}
	if again := deadLetters(t, env); len(again) != 1 {
		t.Fatalf("a second DeadLetters = %d, want 1: reading consumed the store", len(again))
	}
}

func rowLapsesDeadLetter(t *testing.T, newEnv envFactory) {
	policy := rowPolicy()
	policy.MaxDeliveries = 2
	env := newEnv(t, policy)
	sent := publish(t, env, "crashes its consumer")
	for range policy.MaxDeliveries {
		receiveOne(t, env)
		lapse(env, policy)
	}
	expectEmpty(t, env)
	dead := deadLetters(t, env)
	if len(dead) != 1 || dead[0].Message.ID != sent.ID || dead[0].Reason != "LEASE_EXPIRED" || dead[0].Deliveries != policy.MaxDeliveries {
		t.Fatalf("DeadLetters = %+v, want the message, LEASE_EXPIRED, %d deliveries", dead, policy.MaxDeliveries)
	}
}

func rowNilCause(t *testing.T, newEnv envFactory) {
	policy := rowPolicy()
	policy.MaxDeliveries = 1
	env := newEnv(t, policy)
	publish(t, env, "x")
	got := receiveOne(t, env)
	if _, err := env.broker.Nack(t.Context(), got.Lease.Receipt, nil); err != nil {
		t.Fatalf("Nack(nil) = %v, want nil", err)
	}
	dead := deadLetters(t, env)
	if len(dead) != 1 || dead[0].Reason != "UNREPORTED" || dead[0].Cause != "" || dead[0].Code != 0 {
		t.Fatalf("DeadLetters = %+v, want UNREPORTED and no cause", dead)
	}
}

func rowExtend(t *testing.T, newEnv envFactory) {
	policy := rowPolicy()
	env := newEnv(t, policy)
	publish(t, env, "slow")
	got := receiveOne(t, env)
	extender := env.broker.(corequeue.LeaseExtender)
	renewed, err := extender.Extend(t.Context(), got.Lease.Receipt, 4*policy.VisibilityTimeout)
	if err != nil || renewed.Receipt == got.Lease.Receipt || !renewed.ExpiresAt.After(got.Lease.ExpiresAt) {
		t.Fatalf("Extend = (%+v, %v), want a new, later lease", renewed, err)
	}
	if err := env.broker.Ack(t.Context(), got.Lease.Receipt); !errors.Is(err, corequeue.LeaseExpired) {
		t.Fatalf("Ack(old receipt) = %v, want LEASE_EXPIRED", err)
	}
	lapse(env, policy)
	expectEmpty(t, env)
	if err := env.broker.Ack(t.Context(), renewed.Receipt); err != nil {
		t.Fatalf("Ack(renewed) = %v, want nil", err)
	}
}

func rowExtendLapsed(t *testing.T, newEnv envFactory) {
	policy := rowPolicy()
	env := newEnv(t, policy)
	publish(t, env, "x")
	got := receiveOne(t, env)
	lapse(env, policy)
	_, err := env.broker.(corequeue.LeaseExtender).Extend(t.Context(), got.Lease.Receipt, policy.VisibilityTimeout)
	if !errors.Is(err, corequeue.LeaseExpired) {
		t.Fatalf("Extend(lapsed) = %v, want LEASE_EXPIRED", err)
	}
}

func rowExtendNonPositive(t *testing.T, newEnv envFactory) {
	env := newEnv(t, rowPolicy())
	publish(t, env, "x")
	got := receiveOne(t, env)
	extender := env.broker.(corequeue.LeaseExtender)
	for _, by := range []time.Duration{0, -time.Second} {
		if _, err := extender.Extend(t.Context(), got.Lease.Receipt, by); !errors.Is(err, corequeue.QueueMisconfigured) {
			t.Errorf("Extend(%v) = %v, want QUEUE_MISCONFIGURED", by, err)
		}
	}
	if _, err := extender.Extend(t.Context(), got.Lease.Receipt, MaxVisibility+time.Second); !errs.HasCode(err, CodeSQSDelayTooLong) {
		t.Errorf("Extend(13h) = %v, want DELAY_TOO_LONG", err)
	}
}

func rowBatch(t *testing.T, newEnv envFactory) {
	env := newEnv(t, rowPolicy())
	if _, err := env.broker.Receive(t.Context(), 0); !errors.Is(err, corequeue.InvalidBatchSize) {
		t.Errorf("Receive(0) = %v, want INVALID_BATCH_SIZE", err)
	}
	if _, err := env.broker.Receive(t.Context(), MaxReceiveBatch+1); !errs.HasCode(err, CodeSQSBatchTooLarge) {
		t.Errorf("Receive(11) = %v, want BATCH_TOO_LARGE", err)
	}
	for range 5 {
		publish(t, env, "x")
	}
	if got := receive(t, env, 2); len(got) > 2 || len(got) == 0 {
		t.Fatalf("Receive(2) = %d messages, want 1 or 2", len(got))
	}
}

func rowOversized(t *testing.T, newEnv envFactory) {
	policy := rowPolicy()
	policy.MaxMessageBytes = 8
	env := newEnv(t, policy)
	if _, err := env.broker.Publish(t.Context(), []byte("123456789")); !errors.Is(err, corequeue.MessageTooLarge) {
		t.Fatalf("Publish(9 bytes) = %v, want MESSAGE_TOO_LARGE", err)
	}
	wide := newEnv(t, rowPolicy())
	if _, err := wide.broker.Publish(t.Context(), make([]byte, MaxPayloadBytes+1)); !errors.Is(err, corequeue.MessageTooLarge) {
		t.Fatalf("Publish(MaxPayloadBytes+1) = %v, want MESSAGE_TOO_LARGE", err)
	}
	expectEmpty(t, env)
}

func rowEmptyPayload(t *testing.T, newEnv envFactory) {
	env := newEnv(t, rowPolicy())
	publish(t, env, "")
	if got := receiveOne(t, env); len(got.Message.Payload) != 0 {
		t.Fatalf("payload = %q, want empty", got.Message.Payload)
	}
}

func rowReject(t *testing.T, newEnv envFactory) {
	env := newEnv(t, rowPolicy())
	publish(t, env, "undecodable")
	got := receiveOne(t, env)
	if err := env.broker.(corequeue.Rejecter).Reject(t.Context(), got.Lease.Receipt, corequeue.DoNotRetry(nil)); err != nil {
		t.Fatalf("Reject = %v, want nil", err)
	}
	expectEmpty(t, env)
	if dead := deadLetters(t, env); len(dead) != 1 || dead[0].Deliveries != 1 {
		t.Fatalf("DeadLetters = %+v, want one, after one delivery", dead)
	}
}

func rowNackAfter(t *testing.T, newEnv envFactory) {
	policy := rowPolicy()
	env := newEnv(t, policy)
	publish(t, env, "later")
	got := receiveOne(t, env)
	nacker := env.broker.(corequeue.DelayedNacker)
	if _, err := nacker.NackAfter(t.Context(), got.Lease.Receipt, nil, MaxVisibility+time.Second); !errs.HasCode(err, CodeSQSDelayTooLong) {
		t.Fatalf("NackAfter(13h) = %v, want DELAY_TOO_LONG", err)
	}
	verdict, err := nacker.NackAfter(t.Context(), got.Lease.Receipt, nil, 3*time.Second)
	if err != nil || verdict.DeadLettered {
		t.Fatalf("NackAfter = (%+v, %v), want handed back", verdict, err)
	}
	expectEmpty(t, env)
	env.advance(4 * time.Second)
	if again := receiveOne(t, env); again.Deliveries != 2 {
		t.Fatalf("redelivery = %d, want 2", again.Deliveries)
	}
}

func rowPublishAt(t *testing.T, newEnv envFactory) {
	env := newEnv(t, rowPolicy())
	publisher := env.broker.(corequeue.DelayedPublisher)
	now := time.Now()
	if fake, ok := env.broker.(*sqsBroker); ok {
		now = fake.clock.Now()
	}
	if _, err := publisher.PublishAt(t.Context(), []byte("too late"), now.Add(MaxPublishDelay+time.Minute)); !errs.HasCode(err, CodeSQSDelayTooLong) {
		t.Fatalf("PublishAt(+16m) = %v, want DELAY_TOO_LONG", err)
	}
	if _, err := publisher.PublishAt(t.Context(), []byte("reminder"), now.Add(3*time.Second)); err != nil {
		t.Fatalf("PublishAt = %v, want nil", err)
	}
	expectEmpty(t, env)
	env.advance(4 * time.Second)
	if got := receiveOne(t, env); string(got.Message.Payload) != "reminder" {
		t.Fatalf("payload = %q, want reminder", got.Message.Payload)
	}
}
