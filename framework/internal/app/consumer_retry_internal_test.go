package app

import (
	"context"
	"io"
	"testing"
	"time"

	"github.com/kitsunium/sdk/pkg/v1/clock"
	"github.com/kitsunium/sdk/pkg/v1/data/queue"
)

// A message that does not decode will not decode on its fifth attempt
// either (ADR 0151 §D8): each consumer kit runs — a subscription's, a
// watch's, a queued command's — dead-letters it on its FIRST delivery,
// MESSAGE_UNDECODABLE recorded, instead of spending its attempts. The clock
// is manual and never advanced, so no retry can fall due: a dead letter
// after one delivery is the immediate one.

// undecodablePayload is bytes no consumer of kit's decodes.
var undecodablePayload = []byte("not json at all")

// startUndecodable runs svc in memory on a clock nobody advances.
func startUndecodable(t *testing.T, svc *Service) *App {
	t.Helper()
	clk := clock.NewManualClock(time.Date(2026, 10, 14, 9, 0, 0, 0, time.UTC))
	app := NewApp(svc.name, svc).With(InMemory(), Clock(clk), Listen("127.0.0.1:0"), Env(EnvDev), Analyze(false), Logs(io.Discard))
	must(t, app.Start(t.Context()))
	stopAtCleanup(t, app)
	return app
}

// deadAtOnce waits for the one dead letter of broker, and checks it died on
// its first delivery, as undecodable.
func deadAtOnce(t *testing.T, broker queue.Broker) {
	t.Helper()
	var dead []queue.DeadLetter
	eventuallyInternal(t, "the dead letter", func() bool {
		var err error
		dead, err = broker.(queue.DeadLetterReader).DeadLetters(t.Context(), 10)
		return err == nil && len(dead) == 1
	})
	if d := dead[0]; d.Deliveries != 1 || d.Reason != "MESSAGE_UNDECODABLE" {
		t.Errorf("the dead letter: deliveries %d, reason %q; want 1, MESSAGE_UNDECODABLE", d.Deliveries, d.Reason)
	}
}

func TestAnUndecodableMessageIsDeadLetteredAtOnceBySubscription(t *testing.T) {
	svc := NewService("undecsub", "A subscription handed bytes it cannot decode.")
	letters := svc.Topic[letter2]("letters")
	svc.Subscribe("read", letters, func(context.Context, letter2) error { return nil }, MaxDeliveries(5))
	startUndecodable(t, svc)
	sub := letters.subs[0]
	if err := sub.broker.Publish(t.Context(), undecodablePayload); err != nil {
		t.Fatal(err)
	}
	deadAtOnce(t, sdkQueue(t, sub.broker))
}

func TestAnUndecodableMessageIsDeadLetteredAtOnceByWatch(t *testing.T) {
	svc := NewService("undecwatch", "A watch handed a notice it cannot decode.")
	watch := svc.Watch("content", Moderated, func(context.Context, WrittenEvent) error { return nil }, MaxDeliveries(5))
	startUndecodable(t, svc)
	r := watch.run.Load()
	if r == nil {
		t.Fatal("the watch does not run")
	}
	if err := r.broker.Publish(t.Context(), undecodablePayload); err != nil {
		t.Fatal(err)
	}
	deadAtOnce(t, sdkQueue(t, r.broker))
}

// A queued command's dispatch that does not decode is dead-lettered at once
// and gives its key back, as a dispatch dead-lettered after its last
// attempt does: the same key can be queued again.
func TestAnUndecodableMessageIsDeadLetteredAtOnceByQueuedCommand(t *testing.T) {
	svc := NewService("undeccmd", "A queued command handed a dispatch it cannot decode.")
	cmd := svc.Command("run", func(context.Context, struct {
		Key string `json:"key"`
	},
	) (EmptyValue, error) {
		return EmptyValue{}, nil
	}, Queued(), MaxDeliveries(5)).Key(func(in struct {
		Key string `json:"key"`
	},
	) string {
		return in.Key
	})
	startUndecodable(t, svc)
	q := cmd.queue.Load()
	if q == nil {
		t.Fatal("the command's queue is not open")
	}
	q.mu.Lock()
	if q.pending == nil {
		q.pending = map[string]bool{}
	}
	q.pending["k"] = true
	q.mu.Unlock()
	// The envelope decodes, its key with it; its input does not.
	if err := q.broker.Publish(t.Context(), []byte(`{"key":"k","input":"not an object"}`)); err != nil {
		t.Fatal(err)
	}
	deadAtOnce(t, sdkQueue(t, q.broker))
	eventuallyInternal(t, "the key given back", func() bool {
		q.mu.Lock()
		defer q.mu.Unlock()
		return !q.pending["k"]
	})
}

// A consumer's queue waits longer after each failure (ADR 0151 §D7): half a
// second after the first, doubling, never more than kit's backoff ceiling —
// a downstream that is down is asked less and less often.
func TestAConsumersRetryDelayGrows(t *testing.T) {
	clk := clock.NewManualClock(time.Date(2026, 10, 14, 9, 0, 0, 0, time.UTC))
	a := &App{clock: clk}
	pb, err := a.openQueue(&nodeBase{}, "", consumerPolicy(10))
	must(t, err)
	b := sdkQueue(t, pb)
	ctx := t.Context()
	if _, err := b.Publish(ctx, []byte("work")); err != nil {
		t.Fatal(err)
	}
	// fail leases the message, which must be due now, and nacks it.
	fail := func(attempt int) {
		t.Helper()
		got, err := b.Receive(ctx, 1)
		must(t, err)
		if len(got) != 1 || got[0].Deliveries != attempt {
			t.Fatalf("attempt %d: received %+v", attempt, got)
		}
		if _, err := b.Nack(ctx, got[0].Lease.Receipt, nil); err != nil {
			t.Fatal(err)
		}
	}
	// notYet checks the message is not due after the wait so far.
	notYet := func(after time.Duration) {
		t.Helper()
		got, err := b.Receive(ctx, 1)
		must(t, err)
		if len(got) != 0 {
			t.Fatalf("the message was due again %v after a failure", after)
		}
	}
	fail(1)
	clk.Advance(500 * time.Millisecond)
	fail(2)
	clk.Advance(500 * time.Millisecond)
	notYet(500 * time.Millisecond)
	clk.Advance(500 * time.Millisecond)
	fail(3)
	clk.Advance(1500 * time.Millisecond)
	notYet(1500 * time.Millisecond)
	clk.Advance(500 * time.Millisecond)
	fail(4)
	// The ceiling: past it, every wait is the ceiling.
	for n := 5; n < 10; n++ {
		clk.Advance(loopBackoffMax)
		fail(n)
	}
	clk.Advance(loopBackoffMax - time.Millisecond)
	notYet(loopBackoffMax - time.Millisecond)
	clk.Advance(time.Millisecond)
	fail(10)
}
