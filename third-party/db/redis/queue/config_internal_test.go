package queue

import (
	"errors"
	"testing"
	"time"

	goredis "github.com/redis/go-redis/v9"

	"github.com/kitsunium/sdk/internal/kernel/clock"
	"github.com/kitsunium/sdk/internal/kernel/errs"

	corequeue "github.com/kitsunium/sdk/internal/core/data/queue"
)

// lazyClient is a client that never connects: NewStream sends no command.
func lazyClient(t *testing.T) goredis.UniversalClient {
	t.Helper()
	client := goredis.NewClient(&goredis.Options{Addr: "127.0.0.1:1", MaxRetries: -1})
	t.Cleanup(func() { _ = client.Close() })
	return client
}

func validConfig(t *testing.T) StreamConfig {
	return StreamConfig{
		Client: lazyClient(t), Stream: "jobs", Group: "workers",
		Policy: corequeue.PolicyValue{VisibilityTimeout: time.Second, MaxDeliveries: 3},
	}
}

func TestNewStreamRefusesByFieldWhatNoCommandCouldFix(t *testing.T) {
	t.Parallel()
	tests := []struct {
		edit  func(c *StreamConfig)
		field string
	}{
		{func(c *StreamConfig) { c.Client = nil }, "Client"},
		{func(c *StreamConfig) { c.Stream = "" }, "Stream"},
		{func(c *StreamConfig) { c.Stream = "jo}bs" }, "Stream"},
		{func(c *StreamConfig) { c.Group = "" }, "Group"},
	}
	for _, tc := range tests {
		t.Run(tc.field, func(t *testing.T) {
			t.Parallel()
			cfg := validConfig(t)
			tc.edit(&cfg)
			broker, err := NewStream(cfg)
			if broker != nil || !errs.HasCode(err, CodeRedisQueueMisconfigured) {
				t.Fatalf("NewStream = (%v, %v), want STREAM_MISCONFIGURED", broker, err)
			}
			if fields := errs.FieldsOf(err); len(fields) == 0 || fields[0].StringValue() != tc.field {
				t.Fatalf("NewStream = %v, want the field %s named", err, tc.field)
			}
		})
	}
	cfg := validConfig(t)
	cfg.Policy.VisibilityTimeout = 0
	if _, err := NewStream(cfg); !errors.Is(err, corequeue.QueueMisconfigured) {
		t.Fatalf("NewStream(zero visibility) = %v, want QUEUE_MISCONFIGURED", err)
	}
}

func TestTheBrokerImplementsItsSiblings(t *testing.T) {
	t.Parallel()
	broker, err := NewStream(validConfig(t))
	if err != nil {
		t.Fatalf("NewStream = %v", err)
	}
	if _, ok := broker.(corequeue.Waker); !ok {
		t.Error("no Waker")
	}
	if _, ok := broker.(corequeue.LeaseExtender); !ok {
		t.Error("no LeaseExtender")
	}
	if _, ok := broker.(corequeue.Rejecter); !ok {
		t.Error("no Rejecter")
	}
	if _, ok := broker.(corequeue.Releaser); !ok {
		t.Error("no Releaser")
	}
	if _, ok := broker.(corequeue.DelayedNacker); !ok {
		t.Error("no DelayedNacker")
	}
	if _, ok := broker.(corequeue.DeadLetterReader); !ok {
		t.Error("no DeadLetterReader")
	}
	keys := broker.(*streamBroker).keys
	for _, key := range keys {
		if key[:7] != "{jobs}:" {
			t.Errorf("key %q does not open with the hash tag {jobs}:", key)
		}
	}
}

func TestAReceiptRoundTripsAndAForeignOneIsUnknown(t *testing.T) {
	t.Parallel()
	want := receiptValue{id: "1700000000000-0", token: "abc-1", deliveries: 2}
	got, ok := parseReceipt(want.encode())
	if !ok || got != want {
		t.Fatalf("parseReceipt(encode) = (%+v, %v), want %+v", got, ok, want)
	}
	for _, foreign := range []corequeue.ReceiptValue{"", "x", "rs1|id|tok", "rs1||tok|1", "rs1|id||1", "rs1|id|tok|0", "rs2|id|tok|1"} {
		if _, ok := parseReceipt(foreign); ok {
			t.Errorf("parseReceipt(%q) accepted a receipt this package never minted", foreign)
		}
	}
	broker, err := NewStream(validConfig(t))
	if err != nil {
		t.Fatalf("NewStream = %v", err)
	}
	if err := broker.Ack(t.Context(), "never-issued"); !errors.Is(err, corequeue.UnknownReceipt) {
		t.Fatalf("Ack(foreign) = %v, want UNKNOWN_RECEIPT before any command", err)
	}
	if _, err := broker.Receive(t.Context(), 0); !errors.Is(err, corequeue.InvalidBatchSize) {
		t.Fatalf("Receive(0) = %v, want INVALID_BATCH_SIZE", err)
	}
	if _, err := broker.(corequeue.LeaseExtender).Extend(t.Context(), want.encode(), 0); !errors.Is(err, corequeue.QueueMisconfigured) {
		t.Fatalf("Extend(0) = %v, want QUEUE_MISCONFIGURED", err)
	}
}

func TestAnUnreachableServerIsStreamUnreachable(t *testing.T) {
	t.Parallel()
	broker, err := NewStream(validConfig(t))
	if err != nil {
		t.Fatalf("NewStream = %v", err)
	}
	_, perr := broker.Publish(t.Context(), []byte("x"))
	if !errs.HasCode(perr, CodeRedisQueueUnreachable) || !errs.HasCode(perr, corequeue.CodeQueueBackendFailed) {
		t.Fatalf("Publish = %v, want STREAM_UNREACHABLE with QUEUE_BACKEND_FAILED", perr)
	}
	if _, err := broker.Publish(t.Context(), make([]byte, corequeue.DefaultMaxMessageBytes+1)); !errors.Is(err, corequeue.MessageTooLarge) {
		t.Fatalf("Publish(oversized) = %v, want MESSAGE_TOO_LARGE", err)
	}
	if !errs.HasCode(commandError(errors.New("ERR wrong"), "x"), CodeRedisQueueCommandRefused) {
		t.Fatal("a server's answer is not COMMAND_REFUSED")
	}
}

func TestTheWakerSignalsOwnEventsAndReportsTheNearestInstant(t *testing.T) {
	t.Parallel()
	clk := clock.NewManualClock(time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC))
	w := newWaker()
	first := w.wake(clk.Now())
	if first.Scheduled {
		t.Fatal("a fresh waker scheduled something")
	}
	w.signal(clk.Now().Add(3 * time.Second))
	select {
	case <-first.Signal:
	default:
		t.Fatal("a hand-back did not close the signal handed out before it")
	}
	w.schedule(clk.Now().Add(time.Second), 1)
	got := w.wake(clk.Now())
	if !got.Scheduled || got.In != time.Second {
		t.Fatalf("wake = %+v, want the nearest instant, 1s", got)
	}
	clk.Advance(5 * time.Second)
	if later := w.wake(clk.Now()); later.Scheduled {
		t.Fatalf("wake after every instant passed = %+v, want none", later)
	}
}

func TestADeadLetterRecordDecodesWhole(t *testing.T) {
	t.Parallel()
	dead := decodeDeadLetter(map[string]any{
		"id": "1-0", "n": "3", "f": "1800000000000000", "r": "NOT_RETRYABLE", "c": "public", "k": "4294967295",
		"t": "1,2", fieldPayload: "body", fieldEnqueued: "1800000000000000000",
	})
	if dead.Message.ID != "1-0" || dead.Deliveries != 3 || dead.Code != 4294967295 || len(dead.Trail) != 2 ||
		string(dead.Message.Payload) != "body" || dead.FailedAt.UnixMicro() != 1800000000000000 {
		t.Fatalf("decodeDeadLetter = %+v", dead)
	}
	if decodeTrail("1,x") != nil || encodeTrail([]uint32{7, 8}) != "7,8" {
		t.Fatal("the trail codec does not round-trip")
	}
	message := decodeEntry("2-0", []any{fieldPayload, "", fieldEnqueued, "5", "other", "x"})
	if message.ID != "2-0" || len(message.Payload) != 0 || message.EnqueuedAt.UnixNano() != 5 {
		t.Fatalf("decodeEntry = %+v", message)
	}
}
