//go:build integration

// Package brokerrows is the conformance rows of internal/service/data/queue,
// restated for the vendor brokers this directory drives against real servers
// — the Valkey stream broker and the JetStream broker — until the importable
// queuetest suite exists (kitsunium/sdk#985). A row moves time with
// Env.Advance, never with a sleep of its own: a broker whose leases are its
// own clock's advances a ManualClock, one whose leases are the server's
// sleeps. A row that needs a sibling the broker does not implement is
// skipped by name.
package brokerrows

import (
	"errors"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/kitsunium/sdk/internal/kernel/errs"

	corequeue "github.com/kitsunium/sdk/internal/core/data/queue"
)

// Env is one broker under test and the way to move its time.
type Env struct {
	// Broker is the broker under test, over fresh queues.
	Broker corequeue.Broker
	// Advance moves the broker's time — and the server's, when the server
	// keeps the leases — forward by d.
	Advance func(d time.Duration)
	// Sibling builds a second broker over the SAME queue, as a second
	// replica would: the race row runs one consumer on each.
	Sibling func() corequeue.Broker
}

// Factory builds an Env with policy.
type Factory func(t *testing.T, policy corequeue.PolicyValue) Env

// Policy is the policy every row starts from: a lease long enough for a
// round trip, three deliveries, no retry delay.
func Policy() corequeue.PolicyValue {
	return corequeue.PolicyValue{VisibilityTimeout: 2 * time.Second, MaxDeliveries: 3}
}

// Run runs every row over factory.
func Run(t *testing.T, factory Factory) {
	t.Helper()
	rows := []struct {
		run  func(t *testing.T, factory Factory)
		name string
	}{
		{rowRemovedAtAck, "a message is removed at acknowledgement and not at read"},
		{rowLapsedRedelivers, "a lapsed lease redelivers with the count incremented"},
		{rowLapsedAckRefused, "an ack from a lapsed lease is refused and removes nothing"},
		{rowUnknownReceipt, "a receipt this broker never issued is unknown"},
		{rowNackDeadLetters, "a nack retries until the budget is spent and then dead-letters"},
		{rowLapsesDeadLetter, "a message nobody nacks still reaches the dead-letter store"},
		{rowNilCause, "a nack with no cause is recorded as unreported"},
		{rowExtend, "Extend renews the lease and voids the old receipt"},
		{rowExtendLapsed, "Extend refuses a lapsed lease"},
		{rowBatch, "a batch is bounded and a non-positive one refused"},
		{rowOversized, "an oversized payload is refused at the producer"},
		{rowEmptyPayload, "an empty payload is a legitimate message"},
		{rowReject, "Reject dead-letters at once with the count it had"},
		{rowRelease, "Release hands the message back with its count given back"},
		{rowNackAfter, "NackAfter holds the message for the delay it names"},
		{rowRace, "two consumers of one queue never both hold one message"},
	}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) { row.run(t, factory) })
	}
}

func publish(t *testing.T, env Env, payload string) corequeue.MessageValue {
	t.Helper()
	message, err := env.Broker.Publish(t.Context(), []byte(payload))
	if err != nil {
		t.Fatalf("Publish(%q) = %v, want nil", payload, err)
	}
	return message
}

func receive(t *testing.T, broker corequeue.Broker, max int) []corequeue.DeliveryValue {
	t.Helper()
	got, err := broker.Receive(t.Context(), max)
	if err != nil {
		t.Fatalf("Receive(%d) = %v, want nil", max, err)
	}
	return got
}

func receiveOne(t *testing.T, env Env) corequeue.DeliveryValue {
	t.Helper()
	got := receive(t, env.Broker, 1)
	if len(got) != 1 {
		t.Fatalf("Receive = %d messages, want 1", len(got))
	}
	return got[0]
}

func expectEmpty(t *testing.T, env Env) {
	t.Helper()
	if got := receive(t, env.Broker, 10); len(got) != 0 {
		t.Fatalf("Receive = %d messages, want none", len(got))
	}
}

func deadLetters(t *testing.T, env Env) []corequeue.DeadLetterValue {
	t.Helper()
	reader, ok := env.Broker.(corequeue.DeadLetterReader)
	if !ok {
		t.Fatal("the broker is no DeadLetterReader")
	}
	dead, err := reader.DeadLetters(t.Context(), 100)
	if err != nil {
		t.Fatalf("DeadLetters = %v, want nil", err)
	}
	return dead
}

func lapse(env Env, policy corequeue.PolicyValue) {
	env.Advance(policy.VisibilityTimeout + time.Second)
}

func rowRemovedAtAck(t *testing.T, factory Factory) {
	policy := Policy()
	env := factory(t, policy)
	sent := publish(t, env, "work")
	got := receiveOne(t, env)
	if got.Message.ID != sent.ID || string(got.Message.Payload) != "work" || got.Deliveries != 1 {
		t.Fatalf("Receive = %+v, want the published message, delivery 1", got)
	}
	expectEmpty(t, env)
	if err := env.Broker.Ack(t.Context(), got.Lease.Receipt); err != nil {
		t.Fatalf("Ack = %v, want nil", err)
	}
	lapse(env, policy)
	expectEmpty(t, env)
}

func rowLapsedRedelivers(t *testing.T, factory Factory) {
	policy := Policy()
	env := factory(t, policy)
	sent := publish(t, env, "work")
	first := receiveOne(t, env)
	lapse(env, policy)
	second := receiveOne(t, env)
	if second.Message.ID != sent.ID || second.Deliveries != 2 || first.Deliveries != 1 {
		t.Fatalf("deliveries = (%d, %d), want (1, 2) of %s", first.Deliveries, second.Deliveries, sent.ID)
	}
}

func rowLapsedAckRefused(t *testing.T, factory Factory) {
	policy := Policy()
	env := factory(t, policy)
	publish(t, env, "work")
	first := receiveOne(t, env)
	lapse(env, policy)
	if err := env.Broker.Ack(t.Context(), first.Lease.Receipt); !errors.Is(err, corequeue.LeaseExpired) {
		t.Fatalf("Ack(lapsed) = %v, want LEASE_EXPIRED", err)
	}
	if again := receiveOne(t, env); again.Deliveries != 2 {
		t.Fatalf("redelivery = %d, want 2: the refused ack removed something", again.Deliveries)
	}
}

func rowUnknownReceipt(t *testing.T, factory Factory) {
	env := factory(t, Policy())
	for _, receipt := range []corequeue.ReceiptValue{"", "never-issued"} {
		if err := env.Broker.Ack(t.Context(), receipt); !errors.Is(err, corequeue.UnknownReceipt) {
			t.Errorf("Ack(%q) = %v, want UNKNOWN_RECEIPT", receipt, err)
		}
	}
}

func rowNackDeadLetters(t *testing.T, factory Factory) {
	policy := Policy()
	env := factory(t, policy)
	sent := publish(t, env, "poison")
	cause := errs.Wrap(corequeue.NotRetryable, errs.WrapParams{})
	for attempt := 1; attempt <= policy.MaxDeliveries; attempt++ {
		got := receiveOne(t, env)
		verdict, err := env.Broker.Nack(t.Context(), got.Lease.Receipt, cause)
		if err != nil || verdict.Deliveries != attempt || verdict.DeadLettered != (attempt == policy.MaxDeliveries) {
			t.Fatalf("Nack #%d = (%+v, %v), want dead-lettered only on the last", attempt, verdict, err)
		}
		env.Advance(100 * time.Millisecond)
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
	if again := deadLetters(t, env); len(again) != 1 {
		t.Fatalf("a second DeadLetters = %d, want 1: reading consumed the store", len(again))
	}
}

func rowLapsesDeadLetter(t *testing.T, factory Factory) {
	policy := Policy()
	policy.MaxDeliveries = 2
	env := factory(t, policy)
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

func rowNilCause(t *testing.T, factory Factory) {
	policy := Policy()
	policy.MaxDeliveries = 1
	env := factory(t, policy)
	publish(t, env, "x")
	got := receiveOne(t, env)
	if _, err := env.Broker.Nack(t.Context(), got.Lease.Receipt, nil); err != nil {
		t.Fatalf("Nack(nil) = %v, want nil", err)
	}
	dead := deadLetters(t, env)
	if len(dead) != 1 || dead[0].Reason != "UNREPORTED" || dead[0].Cause != "" || dead[0].Code != 0 {
		t.Fatalf("DeadLetters = %+v, want UNREPORTED and no cause", dead)
	}
}

func rowExtend(t *testing.T, factory Factory) {
	policy := Policy()
	env := factory(t, policy)
	extender, ok := env.Broker.(corequeue.LeaseExtender)
	if !ok {
		t.Skip("no LeaseExtender")
	}
	publish(t, env, "slow")
	got := receiveOne(t, env)
	renewed, err := extender.Extend(t.Context(), got.Lease.Receipt, policy.VisibilityTimeout)
	if err != nil || renewed.Receipt == got.Lease.Receipt || renewed.ExpiresAt.Before(got.Lease.ExpiresAt) {
		t.Fatalf("Extend = (%+v, %v), want a new lease, no earlier", renewed, err)
	}
	if err := env.Broker.Ack(t.Context(), got.Lease.Receipt); !errors.Is(err, corequeue.LeaseExpired) {
		t.Fatalf("Ack(old receipt) = %v, want LEASE_EXPIRED", err)
	}
	if err := env.Broker.Ack(t.Context(), renewed.Receipt); err != nil {
		t.Fatalf("Ack(renewed) = %v, want nil", err)
	}
}

func rowExtendLapsed(t *testing.T, factory Factory) {
	policy := Policy()
	env := factory(t, policy)
	extender, ok := env.Broker.(corequeue.LeaseExtender)
	if !ok {
		t.Skip("no LeaseExtender")
	}
	publish(t, env, "x")
	got := receiveOne(t, env)
	lapse(env, policy)
	if _, err := extender.Extend(t.Context(), got.Lease.Receipt, policy.VisibilityTimeout); !errors.Is(err, corequeue.LeaseExpired) {
		t.Fatalf("Extend(lapsed) = %v, want LEASE_EXPIRED", err)
	}
	if _, err := extender.Extend(t.Context(), got.Lease.Receipt, 0); !errors.Is(err, corequeue.QueueMisconfigured) {
		t.Fatalf("Extend(0) = %v, want QUEUE_MISCONFIGURED", err)
	}
}

func rowBatch(t *testing.T, factory Factory) {
	env := factory(t, Policy())
	if _, err := env.Broker.Receive(t.Context(), 0); !errors.Is(err, corequeue.InvalidBatchSize) {
		t.Errorf("Receive(0) = %v, want INVALID_BATCH_SIZE", err)
	}
	for range 5 {
		publish(t, env, "x")
	}
	if got := receive(t, env.Broker, 2); len(got) != 2 {
		t.Fatalf("Receive(2) = %d messages, want 2", len(got))
	}
}

func rowOversized(t *testing.T, factory Factory) {
	policy := Policy()
	policy.MaxMessageBytes = 8
	env := factory(t, policy)
	if _, err := env.Broker.Publish(t.Context(), []byte("123456789")); !errors.Is(err, corequeue.MessageTooLarge) {
		t.Fatalf("Publish(9 bytes) = %v, want MESSAGE_TOO_LARGE", err)
	}
	expectEmpty(t, env)
}

func rowEmptyPayload(t *testing.T, factory Factory) {
	env := factory(t, Policy())
	publish(t, env, "")
	if got := receiveOne(t, env); len(got.Message.Payload) != 0 {
		t.Fatalf("payload = %q, want empty", got.Message.Payload)
	}
}

func rowReject(t *testing.T, factory Factory) {
	env := factory(t, Policy())
	rejecter, ok := env.Broker.(corequeue.Rejecter)
	if !ok {
		t.Skip("no Rejecter")
	}
	publish(t, env, "undecodable")
	got := receiveOne(t, env)
	if err := rejecter.Reject(t.Context(), got.Lease.Receipt, corequeue.DoNotRetry(nil)); err != nil {
		t.Fatalf("Reject = %v, want nil", err)
	}
	expectEmpty(t, env)
	if dead := deadLetters(t, env); len(dead) != 1 || dead[0].Deliveries != 1 {
		t.Fatalf("DeadLetters = %+v, want one, after one delivery", dead)
	}
}

func rowRelease(t *testing.T, factory Factory) {
	env := factory(t, Policy())
	releaser, ok := env.Broker.(corequeue.Releaser)
	if !ok {
		t.Skip("no Releaser")
	}
	publish(t, env, "not started")
	got := receiveOne(t, env)
	if err := releaser.Release(t.Context(), got.Lease.Receipt); err != nil {
		t.Fatalf("Release = %v, want nil", err)
	}
	if again := receiveOne(t, env); again.Deliveries != got.Deliveries {
		t.Fatalf("after Release, Deliveries = %d, want %d", again.Deliveries, got.Deliveries)
	}
}

func rowNackAfter(t *testing.T, factory Factory) {
	env := factory(t, Policy())
	nacker, ok := env.Broker.(corequeue.DelayedNacker)
	if !ok {
		t.Skip("no DelayedNacker")
	}
	publish(t, env, "later")
	got := receiveOne(t, env)
	verdict, err := nacker.NackAfter(t.Context(), got.Lease.Receipt, nil, 2*time.Second)
	if err != nil || verdict.DeadLettered {
		t.Fatalf("NackAfter = (%+v, %v), want handed back", verdict, err)
	}
	expectEmpty(t, env)
	env.Advance(3 * time.Second)
	if again := receiveOne(t, env); again.Deliveries != 2 {
		t.Fatalf("redelivery = %d, want 2", again.Deliveries)
	}
}

// rowRace runs one consumer on each of two brokers over one queue of a
// thousand messages: every message is delivered once — no double delivery,
// no loss.
func rowRace(t *testing.T, factory Factory) {
	policy := Policy()
	policy.VisibilityTimeout = time.Minute
	env := factory(t, policy)
	const total = 1000
	for i := range total {
		publish(t, env, "m"+strconv.Itoa(i))
	}
	brokers := []corequeue.Broker{env.Broker, env.Sibling()}
	var mu sync.Mutex
	seen := map[string]int{}
	var consumers sync.WaitGroup
	for _, broker := range brokers {
		consumers.Add(1)
		go func() {
			defer consumers.Done()
			for empty := 0; empty < 3; {
				got, err := broker.Receive(t.Context(), 10)
				if err != nil {
					t.Errorf("Receive = %v", err)
					return
				}
				if len(got) == 0 {
					empty++
					continue
				}
				empty = 0
				for _, delivery := range got {
					mu.Lock()
					seen[delivery.Message.ID]++
					mu.Unlock()
					if err := broker.Ack(t.Context(), delivery.Lease.Receipt); err != nil {
						t.Errorf("Ack = %v", err)
					}
				}
			}
		}()
	}
	consumers.Wait()
	doubles := 0
	for _, count := range seen {
		if count > 1 {
			doubles++
		}
	}
	if doubles != 0 || len(seen) != total {
		t.Fatalf("%d double deliveries and %d losses over %d messages, want none", doubles, total-len(seen), total)
	}
}
