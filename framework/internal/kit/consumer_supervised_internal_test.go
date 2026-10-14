package kit

import (
	"context"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/kitsunium/sdk/framework/model"
	"github.com/kitsunium/sdk/pkg/v1/clock"
	"github.com/kitsunium/sdk/pkg/v1/data/queue"
)

// queue.Consume returns when its broker fails — a storage error is not one
// a consumer can poll its way out of —, and a consumer that returned while
// the app still serves is a component that is "up" and does nothing. So kit
// runs every consumer under the SDK's supervisor (ADR 0112), as it runs a
// hand-written loop: the consumer whose queue failed comes back after kit's
// backoff, and the Studio counts the restart and shows the error.

// flakyBroker is a queue whose first Receive fails as a storage failure
// would, and which works afterwards.
type flakyBroker struct {
	queue.Broker

	mu     sync.Mutex
	failed bool
}

// Receive fails once, then receives from the queue it wraps.
func (b *flakyBroker) Receive(ctx context.Context, maxMessages int) ([]queue.Delivery, error) {
	b.mu.Lock()
	first := !b.failed
	b.failed = true
	b.mu.Unlock()
	if first {
		return nil, failure(CodeQueueOpen, "QUEUE_BROKEN", "the queue's storage failed", nil)
	}
	return b.Broker.Receive(ctx, maxMessages)
}

// startFlaky runs svc in memory on a manual clock, every queue it opens
// failing at its first Receive, and returns the app, its clock and the
// queues it opened, unwrapped.
func startFlaky(t *testing.T, svc *Service) (*App, *clock.ManualClock, func() []queue.Broker) {
	t.Helper()
	clk := clock.NewManualClock(time.Date(2026, 10, 14, 9, 0, 0, 0, time.UTC))
	app := NewApp(svc.name, svc).With(InMemory(), Clock(clk), Listen("127.0.0.1:0"), Env(EnvDev), Analyze(false), Logs(io.Discard))
	var (
		mu     sync.Mutex
		opened []queue.Broker
	)
	app.wrapQueue = func(b queue.Broker) queue.Broker {
		mu.Lock()
		opened = append(opened, b)
		mu.Unlock()
		return &flakyBroker{Broker: b}
	}
	must(t, app.Start(t.Context()))
	stopAtCleanup(t, app)
	return app, clk, func() []queue.Broker {
		mu.Lock()
		defer mu.Unlock()
		return opened
	}
}

// comesBack waits for the consumer of node to be restarting after its
// queue failed, then advances the clock through the backoff and checks that
// the consumer, back, delivers what its queue holds: the undecodable
// message it dead-letters at once.
func comesBack(t *testing.T, app *App, clk *clock.ManualClock, node string, opened func() []queue.Broker) {
	t.Helper()
	name := node + " consumer"
	eventuallyInternal(t, "the consumer to wait for its restart", func() bool {
		l, ok := consumerLoop(app, name)
		return ok && l.State == model.LoopRestarting
	})
	brokers := opened()
	if len(brokers) != 1 {
		t.Fatalf("%d queues opened; want 1", len(brokers))
	}
	if _, err := brokers[0].Publish(t.Context(), undecodablePayload); err != nil {
		t.Fatal(err)
	}
	eventuallyInternal(t, "the consumer to come back", func() bool {
		clk.Advance(time.Second)
		dead, err := brokers[0].(queue.DeadLetterReader).DeadLetters(t.Context(), 10)
		return err == nil && len(dead) == 1
	})
	l, _ := consumerLoop(app, name)
	if l.Restarts != 1 || l.LastError == "" || l.State == model.LoopStopped {
		t.Errorf("the consumer's loop: restarts %d, last error %q, state %q; want 1 restart, the failure, not stopped", l.Restarts, l.LastError, l.State)
	}
}

func TestASubscriptionWhoseQueueFailsComesBack(t *testing.T) {
	svc := NewService("flakysub", "A subscription whose queue fails once.")
	letters := svc.Topic[letter2]("letters")
	sub := svc.Subscribe("read", letters, func(context.Context, letter2) error { return nil })
	app, clk, opened := startFlaky(t, svc)
	comesBack(t, app, clk, sub.id, opened)
}

func TestAWatchWhoseQueueFailsComesBack(t *testing.T) {
	svc := NewService("flakywatch", "A watch whose queue fails once.")
	watch := svc.Watch("content", Moderated, func(context.Context, WrittenEvent) error { return nil })
	app, clk, opened := startFlaky(t, svc)
	comesBack(t, app, clk, watch.id, opened)
}

func TestAQueuedCommandWhoseQueueFailsComesBack(t *testing.T) {
	svc := NewService("flakycmd", "A queued command whose queue fails once.")
	cmd := svc.Command("run", func(context.Context, struct{}) (EmptyValue, error) {
		return EmptyValue{}, nil
	}, Queued())
	app, clk, opened := startFlaky(t, svc)
	comesBack(t, app, clk, cmd.id, opened)
}

// consumerLoop is the loop called name as the Studio shows it, if the app
// runs one.
func consumerLoop(app *App, name string) (model.Loop, bool) {
	for _, l := range app.sortedLoops() {
		if l.Name == name {
			return l, true
		}
	}
	return model.Loop{}, false
}
