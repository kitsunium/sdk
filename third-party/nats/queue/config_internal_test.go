package queue

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/kitsunium/sdk/internal/kernel/errs"

	corequeue "github.com/kitsunium/sdk/internal/core/data/queue"
)

func validConfig() Config {
	return Config{
		Conn: &nats.Conn{}, Stream: "JOBS", Subject: "jobs.work", DeadLetterSubject: "jobs.dead", Consumer: "workers",
		Policy: corequeue.PolicyValue{VisibilityTimeout: time.Second, MaxDeliveries: 3}, Timeout: time.Second,
	}
}

func TestNewJetStreamRefusesByFieldWhatNoRequestCouldFix(t *testing.T) {
	t.Parallel()
	tests := []struct {
		edit  func(c *Config)
		field string
	}{
		{func(c *Config) { c.Conn = nil }, "Conn"},
		{func(c *Config) { c.Stream = "" }, "Stream"},
		{func(c *Config) { c.Stream = "a.b" }, "Stream"},
		{func(c *Config) { c.Subject = "" }, "Subject"},
		{func(c *Config) { c.Subject = "jobs.*" }, "Subject"},
		{func(c *Config) { c.Subject = "jobs..work" }, "Subject"},
		{func(c *Config) { c.DeadLetterSubject = c.Subject }, "DeadLetterSubject"},
		{func(c *Config) { c.DeadLetterSubject = "dead.>" }, "DeadLetterSubject"},
		{func(c *Config) { c.Consumer = "" }, "Consumer"},
		{func(c *Config) { c.Consumer = "a b" }, "Consumer"},
		{func(c *Config) { c.Timeout = 0 }, "Timeout"},
	}
	for _, tc := range tests {
		t.Run(tc.field, func(t *testing.T) {
			t.Parallel()
			cfg := validConfig()
			tc.edit(&cfg)
			broker, err := NewJetStream(cfg)
			if broker != nil || !errs.HasCode(err, CodeJetStreamMisconfigured) {
				t.Fatalf("NewJetStream = (%v, %v), want JETSTREAM_MISCONFIGURED", broker, err)
			}
			if fields := errs.FieldsOf(err); len(fields) == 0 || fields[0].StringValue() != tc.field {
				t.Fatalf("NewJetStream = %v, want the field %s named", err, tc.field)
			}
		})
	}
	cfg := validConfig()
	cfg.Policy.MaxDeliveries = 0
	if _, err := NewJetStream(cfg); !errors.Is(err, corequeue.QueueMisconfigured) {
		t.Fatalf("NewJetStream(zero deliveries) = %v, want QUEUE_MISCONFIGURED", err)
	}
}

func TestTheBrokerImplementsItsSiblingsAndSendsNothingAtConstruction(t *testing.T) {
	t.Parallel()
	broker, err := NewJetStream(validConfig())
	if err != nil {
		t.Fatalf("NewJetStream = %v", err)
	}
	for name, ok := range map[string]bool{
		"Waker":            isA[corequeue.Waker](broker),
		"LeaseExtender":    isA[corequeue.LeaseExtender](broker),
		"Rejecter":         isA[corequeue.Rejecter](broker),
		"DelayedNacker":    isA[corequeue.DelayedNacker](broker),
		"OncePublisher":    isA[corequeue.OncePublisher](broker),
		"DeadLetterReader": isA[corequeue.DeadLetterReader](broker),
	} {
		if !ok {
			t.Errorf("no %s", name)
		}
	}
	if isA[corequeue.Releaser](broker) {
		t.Error("a Releaser: the server's delivery count cannot be given back")
	}
	if err := broker.Ack(t.Context(), "never-issued"); !errors.Is(err, corequeue.UnknownReceipt) {
		t.Fatalf("Ack(foreign) = %v, want UNKNOWN_RECEIPT before any request", err)
	}
	if _, err := broker.Receive(t.Context(), 0); !errors.Is(err, corequeue.InvalidBatchSize) {
		t.Fatalf("Receive(0) = %v, want INVALID_BATCH_SIZE", err)
	}
	if _, err := broker.Publish(t.Context(), make([]byte, corequeue.DefaultMaxMessageBytes+1)); !errors.Is(err, corequeue.MessageTooLarge) {
		t.Fatalf("Publish(oversized) = %v, want MESSAGE_TOO_LARGE", err)
	}
	once := broker.(corequeue.OncePublisher)
	if _, _, err := once.PublishOnce(t.Context(), "", nil, DefaultDuplicateWindow); !errors.Is(err, corequeue.QueueMisconfigured) {
		t.Fatalf("PublishOnce(empty key) = %v, want QUEUE_MISCONFIGURED", err)
	}
}

func isA[T any](broker corequeue.Broker) bool {
	_, ok := broker.(T)
	return ok
}

func TestEveryVendorFailureIsTypedUnderThePackageRange(t *testing.T) {
	t.Parallel()
	tests := []struct {
		cause error
		want  errs.Code
	}{
		{jetstream.ErrStreamNotFound, CodeJetStreamStreamMissing},
		{jetstream.ErrConsumerNotFound, CodeJetStreamConsumerMissing},
		{nats.ErrConnectionClosed, CodeJetStreamServerUnreachable},
		{context.DeadlineExceeded, CodeJetStreamServerUnreachable},
		{errors.New("anything else"), CodeJetStreamRequestFailed},
	}
	for _, tc := range tests {
		err := requestError(tc.cause, "op")
		if !errs.HasCode(err, tc.want) || !errs.HasCode(err, corequeue.CodeQueueBackendFailed) || !errors.Is(err, tc.cause) {
			t.Errorf("requestError(%v) = %v, want %s with QUEUE_BACKEND_FAILED and the cause", tc.cause, err, tc.want)
		}
	}
	if !errs.HasCode(publishError(nats.ErrTimeout), CodeJetStreamPublishUnacked) {
		t.Error("a publication with no PubAck is not PUBLISH_UNACKED")
	}
}

func TestADeadLetterRecordDecodesWhole(t *testing.T) {
	t.Parallel()
	msg := nats.NewMsg("dead")
	lease := &leaseValue{message: corequeue.MessageValue{ID: "7", EnqueuedAt: time.Unix(0, 5)}, deliveries: 3}
	setHeaders(msg, lease, causeValue{reason: "NOT_RETRYABLE", public: "p", code: 9, trail: []uint32{1, 2}}, time.Unix(0, 11))
	dead := decodeDeadLetter(&jetstream.RawStreamMsg{Header: msg.Header, Data: []byte("body")})
	if dead.Message.ID != "7" || dead.Deliveries != 3 || dead.Code != 9 || len(dead.Trail) != 2 || dead.Reason != "NOT_RETRYABLE" ||
		string(dead.Message.Payload) != "body" || dead.FailedAt.UnixNano() != 11 || dead.Message.EnqueuedAt.UnixNano() != 5 {
		t.Fatalf("decodeDeadLetter = %+v", dead)
	}
	if decodeTrail("1,x") != nil {
		t.Fatal("a corrupt trail is not dropped")
	}
}
