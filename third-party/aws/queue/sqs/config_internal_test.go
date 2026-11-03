package sqs

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/aws/smithy-go"

	"github.com/kitsunium/sdk/internal/core/observe/logger/writer"
	"github.com/kitsunium/sdk/internal/kernel/clock"
	"github.com/kitsunium/sdk/internal/kernel/errs"

	corequeue "github.com/kitsunium/sdk/internal/core/data/queue"
)

// staticCreds is a CredentialProvider with fixed test credentials.
type staticCreds struct{}

func (staticCreds) Credentials(_ context.Context) (writer.CredentialValue, error) {
	return writer.NewCredentialValue("test", "test", ""), nil
}

func validConfig() Config {
	return Config{
		QueueURL: "https://sqs.eu-west-3.amazonaws.com/1/q", DeadLetterQueueURL: "https://sqs.eu-west-3.amazonaws.com/1/dlq",
		Region: "eu-west-3", Credentials: staticCreds{}, Policy: rowPolicy(),
	}
}

func TestNewRefusesByFieldWhatNoRequestCouldFix(t *testing.T) {
	t.Parallel()
	tests := []struct {
		edit  func(c *Config)
		field string
	}{
		{func(c *Config) { c.QueueURL = "" }, "QueueURL"},
		{func(c *Config) { c.DeadLetterQueueURL = "" }, "DeadLetterQueueURL"},
		{func(c *Config) { c.DeadLetterQueueURL = c.QueueURL }, "DeadLetterQueueURL"},
		{func(c *Config) { c.Region = "" }, "Region"},
		{func(c *Config) { c.Credentials = nil }, "Credentials"},
		{func(c *Config) { c.Policy.VisibilityTimeout = MaxVisibility + time.Second }, "Policy.VisibilityTimeout"},
		{func(c *Config) { c.Policy.RetryDelay = MaxVisibility + time.Second }, "Policy.RetryDelay"},
		{func(c *Config) { c.WaitTime = MaxWaitTime + time.Second }, "WaitTime"},
		{func(c *Config) { c.WaitTime = -time.Second }, "WaitTime"},
	}
	for _, tc := range tests {
		t.Run(tc.field, func(t *testing.T) {
			t.Parallel()
			cfg := validConfig()
			tc.edit(&cfg)
			broker, err := New(cfg)
			if broker != nil || !errs.HasCode(err, CodeSQSMisconfigured) {
				t.Fatalf("New = (%v, %v), want SQS_MISCONFIGURED", broker, err)
			}
			if fields := errs.FieldsOf(err); len(fields) == 0 || fields[0].StringValue() != tc.field {
				t.Fatalf("New = %v, want the field %s named", err, tc.field)
			}
		})
	}
}

func TestEveryBrokerRefusesTheSamePolicies(t *testing.T) {
	t.Parallel()
	for _, policy := range []corequeue.PolicyValue{
		{MaxDeliveries: 3},
		{VisibilityTimeout: time.Second},
		{VisibilityTimeout: time.Second, MaxDeliveries: 3, MaxMessageBytes: -1},
		{VisibilityTimeout: time.Second, MaxDeliveries: 3, MaxRetryDelay: time.Second},
	} {
		cfg := validConfig()
		cfg.Policy = policy
		if _, err := New(cfg); !errors.Is(err, corequeue.QueueMisconfigured) {
			t.Errorf("New(%+v) = %v, want QUEUE_MISCONFIGURED", policy, err)
		}
	}
	if broker, err := New(validConfig()); err != nil || broker == nil {
		t.Fatalf("New(valid) = (%v, %v), want a broker", broker, err)
	}
}

func TestAFailedRequestIsThePackagesSentinelAndKeepsTheLeaseWhenItHolds(t *testing.T) {
	t.Parallel()
	clk := clock.NewManualClock(epoch)
	fake := newFakeSQS(clk)
	broker := newBroker(Config{QueueURL: "q", DeadLetterQueueURL: "dlq", Policy: rowPolicy(), Clock: clk}, fake)
	if _, err := broker.Publish(t.Context(), []byte("x")); err != nil {
		t.Fatalf("Publish = %v", err)
	}
	got, err := broker.Receive(t.Context(), 1)
	if err != nil || len(got) != 1 {
		t.Fatalf("Receive = (%v, %v)", got, err)
	}
	cause := &smithy.GenericAPIError{Code: "InternalError"}
	fake.fail(cause)
	ackErr := broker.Ack(t.Context(), got[0].Lease.Receipt)
	if !errs.HasCode(ackErr, CodeSQSAckFailed) || !errs.HasCode(ackErr, corequeue.CodeQueueBackendFailed) || !errors.Is(ackErr, cause) {
		t.Fatalf("Ack = %v, want ACK_FAILED, QUEUE_BACKEND_FAILED in its trail, the cause kept", ackErr)
	}
	if _, err := broker.Publish(t.Context(), []byte("y")); !errs.HasCode(err, CodeSQSSendFailed) {
		t.Fatalf("Publish = %v, want SEND_FAILED", err)
	}
	if _, err := broker.Receive(t.Context(), 1); !errs.HasCode(err, CodeSQSReceiveFailed) {
		t.Fatalf("Receive = %v, want RECEIVE_FAILED", err)
	}
	fake.fail(nil)
	if err := broker.Ack(t.Context(), got[0].Lease.Receipt); err != nil {
		t.Fatalf("Ack after the failure = %v, want nil: the lease still held", err)
	}
	if fake.depth("q") != 0 {
		t.Fatalf("queue depth = %d, want 0", fake.depth("q"))
	}
}

func TestAReceiptFromAnotherBrokerIsUnknown(t *testing.T) {
	t.Parallel()
	clk := clock.NewManualClock(epoch)
	fake := newFakeSQS(clk)
	cfg := Config{QueueURL: "q", DeadLetterQueueURL: "dlq", Policy: rowPolicy(), Clock: clk}
	issuer, other := newBroker(cfg, fake), newBroker(cfg, fake)
	if _, err := issuer.Publish(t.Context(), []byte("x")); err != nil {
		t.Fatalf("Publish = %v", err)
	}
	got, err := issuer.Receive(t.Context(), 1)
	if err != nil || len(got) != 1 {
		t.Fatalf("Receive = (%v, %v)", got, err)
	}
	if err := other.Ack(t.Context(), got[0].Lease.Receipt); !errors.Is(err, corequeue.UnknownReceipt) {
		t.Fatalf("Ack on another broker = %v, want UNKNOWN_RECEIPT", err)
	}
}

func TestADeadLetterKeepsItsTrail(t *testing.T) {
	t.Parallel()
	env := fakeEnv(t, corequeue.PolicyValue{VisibilityTimeout: time.Second, MaxDeliveries: 1})
	publish(t, env, "x")
	got := receiveOne(t, env)
	cause := errs.Wrap(corequeue.NotRetryable, errs.WrapParams{Code: corequeue.CodeRetryRequested, Reason: "WRAPPED"})
	if _, err := env.broker.Nack(t.Context(), got.Lease.Receipt, cause); err != nil {
		t.Fatalf("Nack = %v", err)
	}
	dead := deadLetters(t, env)
	if len(dead) != 1 || len(dead[0].Trail) != 1 || dead[0].Trail[0] != uint32(corequeue.CodeRetryRequested) {
		t.Fatalf("DeadLetters = %+v, want the wrap's code in the trail", dead)
	}
}
