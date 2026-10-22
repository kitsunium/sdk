package app

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kitsunium/sdk/pkg/v1/data/queue"
	"github.com/kitsunium/sdk/pkg/v1/errs"
)

// errBusy is a downstream's refusal of the moment.
var errBusy = errors.New("busy")

// waiting waits until b's one message waits, not leased, due at at.
func waiting(t *testing.T, what string, b *SubscriptionWorker[tick], at time.Time) {
	t.Helper()
	until(t, what, func() bool {
		live := liveOf(t, b.broker)
		return len(live) == 1 && !live[0].Leased && live[0].VisibleAt.Equal(at)
	})
}

// RetryIn marks its cause with its delay — never a negative one — and keeps
// an SDK cause's code: what a dead letter records.
func TestRetryInMarksItsCauseWithItsDelay(t *testing.T) {
	cause := errs.New(CodeTopicPublish, "SOME_REASON", "a downstream refused", "a downstream refused")
	for _, tc := range []struct {
		err  error
		want time.Duration
		code errs.Code
	}{
		{RetryIn(30*time.Second, errBusy), 30 * time.Second, CodeRetryIn},
		{RetryIn(-time.Second, nil), 0, CodeRetryIn},
		{RetryIn(time.Minute, cause), time.Minute, CodeTopicPublish},
		{RetryIn(time.Hour, RetryIn(time.Minute, errBusy)), time.Hour, CodeRetryIn},
	} {
		d, ok := retryInOf(tc.err)
		code, _ := errs.CodeOf(tc.err)
		if !ok || d != tc.want || code != tc.code || !errs.HasCode(tc.err, CodeRetryIn) {
			t.Errorf("%v: delay %v (%v), code %v; want %v, %v", tc.err, d, ok, code, tc.want, tc.code)
		}
	}
	if _, ok := retryInOf(errBusy); ok {
		t.Error("an error RetryIn did not mark reads as marked")
	}
}

// A subscription's handler that says when to try again has its message
// handed back with that delay, not the queue's half second, and handled
// once it falls due.
func TestASubscriptionRetriesInTheDelayItsHandlerAsks(t *testing.T) {
	clk := manualAt()
	svc := NewService("retried", "A subscription whose handler asks for a delay.")
	ticks := svc.Topic[tick]("ticks")
	var calls atomic.Int32
	sub := svc.Subscribe("count", ticks, func(context.Context, tick) error {
		if calls.Add(1) == 1 {
			return RetryIn(time.Minute, errBusy)
		}
		return nil
	})
	a := startConsumers(t, svc, clk)
	stopAtCleanup(t, a)
	began := clk.Now()
	must(t, ticks.Publish(t.Context(), tick{N: 1}))
	waiting(t, "the message handed back for a minute", sub, began.Add(time.Minute))
	clk.Advance(30 * time.Second)
	if calls.Load() != 1 {
		t.Fatalf("handled %d times half a minute into a delay of a minute", calls.Load())
	}
	clk.Advance(30 * time.Second)
	until(t, "the message handled again", func() bool { return calls.Load() == 2 && len(liveOf(t, sub.broker)) == 0 })
}

// A delay a handler asks for spends an attempt as any failure does: the
// message that used its last one is dead-lettered with the handler's cause.
func TestARetryInOnTheLastAttemptIsDeadLettered(t *testing.T) {
	svc := NewService("spent", "A subscription with one attempt, which asks for a delay.")
	ticks := svc.Topic[tick]("ticks")
	sub := svc.Subscribe("count", ticks, func(context.Context, tick) error { return RetryIn(time.Minute, errBusy) }, MaxDeliveries(1))
	a := startConsumers(t, svc, manualAt())
	stopAtCleanup(t, a)
	must(t, ticks.Publish(t.Context(), tick{N: 1}))
	until(t, "the dead letter", func() bool {
		dead, err := sdkQueue(t, sub.broker).(queue.DeadLetterReader).DeadLetters(t.Context(), 10)
		return err == nil && len(dead) == 1 && dead[0].Reason == "RETRY_IN"
	})
}

// order is a queued command's input, with its key.
type order struct {
	Key string `json:"key"`
}

// queuedKeyed declares a queued command order by its input's key, run by
// handle.
func queuedKeyed(svc *Service, handle func(context.Context, order) error) *Command[order, EmptyValue] {
	return svc.Command("run", func(ctx context.Context, in order) (EmptyValue, error) {
		return EmptyValue{}, handle(ctx, in)
	}, Queued()).Key(func(in order) string { return in.Key })
}

// A queued command's handler that says when to try again keeps its key
// while it waits: a dispatch with it is not queued again.
func TestAQueuedCommandRetriesInTheDelayItsHandlerAsks(t *testing.T) {
	clk := manualAt()
	svc := NewService("cmdretry", "A queued command whose handler asks for a delay.")
	var calls atomic.Int32
	cmd := queuedKeyed(svc, func(context.Context, order) error {
		if calls.Add(1) == 1 {
			return RetryIn(2*time.Minute, errBusy)
		}
		return nil
	})
	a := startConsumers(t, svc, clk)
	stopAtCleanup(t, a)
	began := clk.Now()
	_, err := cmd.Dispatch(t.Context(), order{Key: "k"})
	must(t, err)
	q := cmd.queue.Load()
	until(t, "the dispatch handed back for two minutes", func() bool {
		live := liveOf(t, q.broker)
		return len(live) == 1 && !live[0].Leased && live[0].VisibleAt.Equal(began.Add(2*time.Minute))
	})
	_, err = cmd.Dispatch(t.Context(), order{Key: "k"})
	must(t, err)
	if live := liveOf(t, q.broker); len(live) != 1 {
		t.Fatalf("a dispatch whose key waits was queued again: %d messages", len(live))
	}
	clk.Advance(2 * time.Minute)
	until(t, "the dispatch handled, its key given back", func() bool {
		q.mu.Lock()
		defer q.mu.Unlock()
		return calls.Load() == 2 && !q.pending["k"]
	})
}

// A message published for later waits, unseen, until its instant; a
// publication for later wakes no loop.
func TestATopicPublishesForLater(t *testing.T) {
	clk := manualAt()
	svc := NewService("later", "A topic published for later.")
	ticks := svc.Topic[tick]("ticks")
	var calls atomic.Int32
	sub := svc.Subscribe("count", ticks, func(context.Context, tick) error {
		calls.Add(1)
		return nil
	})
	a := startConsumers(t, svc, clk)
	stopAtCleanup(t, a)
	at := clk.Now().Add(time.Hour)
	must(t, ticks.PublishAt(t.Context(), tick{N: 1}, at))
	waiting(t, "the message waiting for its instant", sub, at)
	clk.Advance(time.Hour - time.Second)
	if calls.Load() != 0 {
		t.Fatal("a message published for later was delivered before its instant")
	}
	clk.Advance(time.Second)
	until(t, "the message delivered at its instant", func() bool { return calls.Load() == 1 })
}

// A queued command dispatched for later is checked now, and handled at its
// instant; inside a transaction it is held to the commit, and a rollback
// drops it.
func TestAQueuedCommandIsDispatchedForLater(t *testing.T) {
	clk := manualAt()
	svc := NewService("cmdlater", "A queued command dispatched for later.")
	var calls atomic.Int32
	cmd := queuedKeyed(svc, func(context.Context, order) error {
		calls.Add(1)
		return nil
	})
	a := startConsumers(t, svc, clk)
	stopAtCleanup(t, a)
	ctx := t.Context()
	at := clk.Now().Add(time.Hour)
	if err := Transact(ctx, func(ctx context.Context) error {
		must(t, cmd.DispatchAt(ctx, order{Key: "dropped"}, at))
		return errBusy
	}); !errors.Is(err, errBusy) {
		t.Fatalf("the transaction: %v", err)
	}
	must(t, Transact(ctx, func(ctx context.Context) error { return cmd.DispatchAt(ctx, order{Key: "kept"}, at) }))
	q := cmd.queue.Load()
	live := liveOf(t, q.broker)
	if len(live) != 1 || !live[0].VisibleAt.Equal(at) {
		t.Fatalf("after a rollback and a commit, the queue holds %+v", live)
	}
	if err := cmd.DispatchAt(ctx, order{Key: "kept"}, at.Add(time.Hour)); err != nil || len(liveOf(t, q.broker)) != 1 {
		t.Fatalf("a dispatch whose key waits for later was queued again: %v", err)
	}
	if err := cmd.DispatchAt(ctx, order{}, at); err == nil {
		t.Error("a dispatch for later with an empty key was queued")
	}
	clk.Advance(time.Hour)
	until(t, "the dispatch handled at its instant", func() bool { return calls.Load() == 1 })
}

// A command that is not queued refuses a dispatch for later: nothing would
// hold it until its instant.
func TestACommandNotQueuedRefusesADispatchForLater(t *testing.T) {
	svc := NewService("cmdnow", "A command that is not queued.")
	cmd := svc.Command("now", func(context.Context, order) (EmptyValue, error) { return EmptyValue{}, nil })
	a := startConsumers(t, svc, manualAt())
	stopAtCleanup(t, a)
	err := cmd.DispatchAt(t.Context(), order{Key: "k"}, time.Now().Add(time.Hour))
	if reason, _ := errs.ReasonOf(err); reason != "COMMAND_NOT_QUEUED" {
		t.Errorf("a dispatch for later of a command that is not queued: %v", err)
	}
}

// onceTicks declares a topic that publishes once per key over a minute,
// and a subscription counting what it receives.
func onceTicks(name string) (*Service, *TopicService[order], *SubscriptionWorker[order], *atomic.Int32) {
	svc := NewService(name, "A topic that publishes once per key.")
	topic := svc.Topic[order]("events").Once(func(m order) string { return m.Key }, time.Minute)
	var calls atomic.Int32
	sub := svc.Subscribe("count", topic, func(context.Context, order) error {
		calls.Add(1)
		return nil
	})
	return svc, topic, sub, &calls
}

// A topic that publishes once puts a message whose key it published within
// its window in no queue, answers nil, and publishes the key again once the
// window passed; an empty key and a publication for later are refused.
func TestATopicPublishesOncePerKey(t *testing.T) {
	clk := manualAt()
	svc, topic, sub, calls := onceTicks("once")
	a := startConsumers(t, svc, clk)
	stopAtCleanup(t, a)
	ctx := t.Context()
	must(t, topic.Publish(ctx, order{Key: "a"}))
	must(t, topic.Publish(ctx, order{Key: "a"}))
	must(t, topic.Publish(ctx, order{Key: "b"}))
	until(t, "two messages handled", func() bool { return calls.Load() == 2 && len(liveOf(t, sub.broker)) == 0 })
	must(t, topic.Publish(ctx, order{Key: "a"}))
	clk.Advance(time.Minute + time.Second)
	must(t, topic.Publish(ctx, order{Key: "a"}))
	until(t, "the key published again past its window", func() bool { return calls.Load() == 3 })
	if err := topic.Publish(ctx, order{}); err == nil || !strings.Contains(err.Error(), "TOPIC_KEY_EMPTY") {
		t.Errorf("a message with an empty key: %v", err)
	}
	if err := topic.PublishAt(ctx, order{Key: "c"}, clk.Now().Add(time.Hour)); err == nil || !strings.Contains(err.Error(), "TOPIC_LATER_ONCE") {
		t.Errorf("a publication for later on a topic that publishes once: %v", err)
	}
}

// A topic's key is declared once, and never nil.
func TestATopicsKeyIsDeclaredOnceAndNeverNil(t *testing.T) {
	for _, tc := range []struct {
		name string
		decl func(*TopicService[order])
		want string
	}{
		{"nil", func(tp *TopicService[order]) { tp.Once(nil, time.Minute) }, "is given a nil key by Once"},
		{"twice", func(tp *TopicService[order]) {
			tp.Once(func(m order) string { return m.Key }, time.Minute).Once(func(m order) string { return m.Key }, time.Hour)
		}, "is given Once twice"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc := NewService("oncebad", "A topic whose key is wrong.")
			tc.decl(svc.Topic[order]("events"))
			a := NewApp(svc.name, svc).With(InMemory(), Clock(manualAt()), Listen("127.0.0.1:0"), Env(EnvDev), Analyze(false), Logs(io.Discard))
			if err := a.Start(t.Context()); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("%v", err)
				if err == nil {
					stopAtCleanup(t, a)
				}
			}
		})
	}
}
