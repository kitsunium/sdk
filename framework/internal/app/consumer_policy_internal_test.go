package app

import (
	"context"
	"io"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kitsunium/sdk/framework/internal/app/plug"
	"github.com/kitsunium/sdk/pkg/v1/clock"
	"github.com/kitsunium/sdk/pkg/v1/data/queue"
)

// tick is a message of the consumers' tests.
type tick struct {
	N int `json:"n"`
}

// startConsumers runs svc in memory on clk.
func startConsumers(t *testing.T, svc *Service, clk clock.Timed) *App {
	t.Helper()
	a := NewApp(svc.name, svc).With(InMemory(), Clock(clk), Listen("127.0.0.1:0"), Env(EnvDev), Analyze(false), Logs(io.Discard))
	must(t, a.Start(t.Context()))
	return a
}

// manualAt is a manual clock at a fixed instant.
func manualAt() *clock.ManualClock {
	return clock.NewManualClock(time.Date(2026, 10, 21, 9, 0, 0, 0, time.UTC))
}

// until polls cond, yielding, until it holds or a second of wall time passed:
// what a goroutine the manual clock woke does is not done when Advance
// returns.
func until(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		runtime.Gosched()
	}
}

// liveOf are the live messages of b, a queue the subsystem opened.
func liveOf(t *testing.T, b plug.Broker) []queue.LiveMessage {
	t.Helper()
	live, err := sdkQueue(t, b).(queue.MessageReader).Messages(t.Context(), "", 10)
	must(t, err)
	return live
}

// recordedConsume is what the consumer of a queue the recording driver
// opened was configured with.
type recordedConsume struct {
	mu       sync.Mutex
	policies map[string]plug.QueuePolicyValue
	consumes map[string]plug.ConsumeConfig
}

// recordingBroker records its consumer's configuration under its queue's
// name.
type recordingBroker struct {
	plug.Broker
	name string
	rec  *recordedConsume
}

// Consume records cfg, then consumes.
func (b recordingBroker) Consume(ctx context.Context, cfg plug.ConsumeConfig) error {
	b.rec.mu.Lock()
	b.rec.consumes[b.name] = cfg
	b.rec.mu.Unlock()
	return b.Broker.Consume(ctx, cfg)
}

// recordConsumers has the queue subsystem record each queue's policy and
// its consumer's configuration, for the test's length.
func recordConsumers(t *testing.T) *recordedConsume {
	t.Helper()
	saved := plug.Queues.Load()
	rec := &recordedConsume{policies: map[string]plug.QueuePolicyValue{}, consumes: map[string]plug.ConsumeConfig{}}
	driver := *saved
	driver.Open = func(cfg plug.QueueConfig) (plug.Broker, error) {
		b, err := saved.Open(cfg)
		if err != nil {
			return nil, err
		}
		rec.mu.Lock()
		rec.policies[cfg.Name] = cfg.Policy
		rec.mu.Unlock()
		return recordingBroker{Broker: b, name: cfg.Name, rec: rec}, nil
	}
	plug.Queues.Store(&driver)
	t.Cleanup(func() { plug.Queues.Store(saved) })
	return rec
}

// Each consumer's queue is leased for its lease, and its consumer drains,
// settles and renews as its settings say: the code's values for a
// subscription that names them, the defaults — AutoExtend on — for a watch
// and a queued command.
func TestAConsumersKnobsReachItsQueue(t *testing.T) {
	rec := recordConsumers(t)
	svc := NewService("knobs", "Consumers whose knobs reach their queues.")
	ticks := svc.Topic[tick]("ticks")
	svc.Subscribe("count", ticks, func(context.Context, tick) error { return nil },
		Drain(2*time.Second), Settle(4*time.Second), Lease(time.Minute), AutoExtend(false))
	svc.Watch("screen", Moderated, func(context.Context, WrittenEvent) error { return nil })
	svc.Command("redo", func(context.Context, tick) (EmptyValue, error) { return EmptyValue{}, nil }, Queued())
	a := startConsumers(t, svc, manualAt())
	stopAtCleanup(t, a)
	want := map[string]struct {
		lease, drain, settle time.Duration
		extend               bool
	}{
		"knobs/subscription/count":  {time.Minute, 2 * time.Second, 4 * time.Second, false},
		"knobs/subscription/screen": {defaultLease, defaultDrain, defaultSettle, true},
		"knobs/command/redo":        {defaultLease, defaultDrain, defaultSettle, true},
	}
	until(t, "every consumer running", func() bool {
		rec.mu.Lock()
		defer rec.mu.Unlock()
		return len(rec.consumes) == len(want)
	})
	rec.mu.Lock()
	defer rec.mu.Unlock()
	for name, w := range want {
		p, c := rec.policies[name], rec.consumes[name]
		if p.VisibilityTimeout != w.lease || c.DrainTimeout != w.drain || c.SettleTimeout != w.settle || c.AutoExtend != w.extend {
			t.Errorf("%s: lease %v, drain %v, settle %v, auto-extend %v; want %+v", name, p.VisibilityTimeout, c.DrainTimeout, c.SettleTimeout, c.AutoExtend, w)
		}
	}
}

// The app's per-component stop budget covers the longest a consumer's stop
// takes — its drain, its settle, and the margin —, ten seconds at least, and
// the whole stop's deadline grows by what that adds; a configuration that
// lengthens a drain lengthens the budget.
func TestTheStopBudgetCoversTheLongestDrain(t *testing.T) {
	plain := NewService("plain", "No consumer.")
	plain.Command("now", func(context.Context, tick) (EmptyValue, error) { return EmptyValue{}, nil })
	a := startConsumers(t, plain, manualAt())
	stopAtCleanup(t, a)
	if got := a.componentStopBudget(); got != defaultStopBudget {
		t.Errorf("no consumer: a budget of %v, want %v", got, defaultStopBudget)
	}
	slow := NewService("slow", "A consumer that drains long.")
	ticks := slow.Topic[tick]("ticks")
	slow.Subscribe("count", ticks, func(context.Context, tick) error { return nil }, Drain(20*time.Second), Settle(5*time.Second))
	b := startConsumers(t, slow, manualAt())
	stopAtCleanup(t, b)
	if got := b.componentStopBudget(); got != 26*time.Second {
		t.Errorf("a drain of 20s and a settle of 5s: a budget of %v, want 26s", got)
	}
	if got := b.stopDeadline(runStopDeadline); got != 46*time.Second {
		t.Errorf("the whole stop's deadline: %v, want 46s", got)
	}
	t.Setenv("SLOW_SLOW_SUBSCRIPTION_COUNT_DRAIN", "40s")
	again := NewService("slow", "The same, its drain set by the environment.")
	ticks2 := again.Topic[tick]("ticks")
	again.Subscribe("count", ticks2, func(context.Context, tick) error { return nil }, Drain(20*time.Second), Settle(5*time.Second))
	c := startConsumers(t, again, manualAt())
	stopAtCleanup(t, c)
	if got := c.componentStopBudget(); got != 46*time.Second {
		t.Errorf("a drain of 40s from the environment: a budget of %v, want 46s", got)
	}
}

// blockingTicks is a service whose subscription's handler says it started,
// then waits for release or for its context to end, and records that
// context; opts configure the subscription.
type blockingTicks struct {
	svc     *Service
	ticks   *TopicService[tick]
	sub     *SubscriptionWorker[tick]
	started chan struct{}
	release chan struct{}
	ctx     atomic.Pointer[context.Context]
}

// newBlockingTicks declares the service.
func newBlockingTicks(name string, opts ...SubscriptionConfigurer) *blockingTicks {
	b := &blockingTicks{started: make(chan struct{}, 1), release: make(chan struct{})}
	b.svc = NewService(name, "A subscription whose handler waits.")
	b.ticks = b.svc.Topic[tick]("ticks")
	b.sub = b.svc.Subscribe("count", b.ticks, func(ctx context.Context, _ tick) error {
		b.ctx.Store(&ctx)
		b.started <- struct{}{}
		select {
		case <-b.release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}, opts...)
	return b
}

// handlerDone reports whether the running handler's context ended.
func (b *blockingTicks) handlerDone() bool {
	ctx := b.ctx.Load()
	return ctx != nil && (*ctx).Err() != nil
}

// stopInBackground stops a, and returns what its stop returns once it does.
func stopInBackground(a *App) <-chan error {
	done := make(chan error, 1)
	go func() { done <- a.Stop(context.Background()) }()
	return done
}

// stopping waits until a's stop began.
func stopping(t *testing.T, a *App) {
	t.Helper()
	until(t, "the stop", func() bool {
		a.mu.Lock()
		defer a.mu.Unlock()
		return a.stopping
	})
}

// A handler that finishes within its drain is acknowledged: the stop waits
// for it instead of cancelling it, and its message is gone.
func TestAHandlerThatFinishesWithinItsDrainIsAcknowledged(t *testing.T) {
	b := newBlockingTicks("drained")
	a := startConsumers(t, b.svc, manualAt())
	must(t, b.ticks.Publish(t.Context(), tick{N: 1}))
	<-b.started
	stopped := stopInBackground(a)
	stopping(t, a)
	if b.handlerDone() {
		t.Fatal("the stop cancelled a handler within its drain")
	}
	close(b.release)
	must(t, <-stopped)
	if live := liveOf(t, b.sub.broker); len(live) != 0 {
		t.Errorf("the handled message is still in its queue: %+v", live)
	}
	if l := a.loopNamed(b.sub.id + " consumer"); l.Errors != 0 || l.Restarts != 0 {
		t.Errorf("the consumer's loop: %d errors, %d restarts", l.Errors, l.Restarts)
	}
}

// A handler still running when its drain ends has its context cancelled —
// never before the drain —, and its message is handed back uncounted: the
// stop is no failure, neither the loop's nor its supervisor's.
func TestAHandlerTheDrainCutsShortIsHandedBackAndNoFailure(t *testing.T) {
	clk := manualAt()
	// A settle and a lease long enough that the clock the test moves reaches
	// neither the stop's budget nor the lease's margin first, however late
	// the consumer arms its drain: the test moves the clock a second at a
	// time until the handler hears its stop.
	b := newBlockingTicks("cut", Settle(time.Hour), Lease(2*time.Hour))
	a := startConsumers(t, b.svc, clk)
	must(t, b.ticks.Publish(t.Context(), tick{N: 1}))
	<-b.started
	began := clk.Now()
	stopped := stopInBackground(a)
	stopping(t, a)
	for !b.handlerDone() {
		if clk.Since(began) > 50*time.Minute {
			t.Fatal("the drain never ended")
		}
		clk.Advance(time.Second)
		for range 64 {
			if b.handlerDone() {
				break
			}
			runtime.Gosched()
		}
	}
	if waited := clk.Since(began); waited < defaultDrain {
		t.Errorf("the handler was cancelled %v into the stop, before its drain of %v", waited, defaultDrain)
	}
	must(t, <-stopped)
	live := liveOf(t, b.sub.broker)
	if len(live) != 1 || live[0].Leased || live[0].Deliveries != 0 {
		t.Errorf("the message cut short: %+v; want it waiting, its delivery uncounted", live)
	}
	l := a.loopNamed(b.sub.id + " consumer")
	if l.Errors != 0 || l.Restarts != 0 || l.LastError != "" {
		t.Errorf("the consumer's loop: %d errors, %d restarts, %q", l.Errors, l.Restarts, l.LastError)
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, p := range a.problems {
		if p.Node == b.sub.id {
			t.Errorf("the stop was reported: %s", p.Message)
		}
	}
}

// A running handler keeps its message past its lease: the lease is renewed
// a third of the way through, again and again, and the message is neither
// handed to another consumer nor counted again.
func TestAutoExtendKeepsARunningHandlersLease(t *testing.T) {
	clk := manualAt()
	b := newBlockingTicks("renewed")
	a := startConsumers(t, b.svc, clk)
	stopAtCleanup(t, a)
	must(t, b.ticks.Publish(t.Context(), tick{N: 1}))
	<-b.started
	visible := liveOf(t, b.sub.broker)[0].VisibleAt
	for range 4 {
		clk.Advance(defaultLease/3 + time.Second)
		until(t, "the lease renewed", func() bool {
			live := liveOf(t, b.sub.broker)
			return len(live) == 1 && live[0].VisibleAt.After(visible)
		})
		visible = liveOf(t, b.sub.broker)[0].VisibleAt
	}
	live := liveOf(t, b.sub.broker)
	if b.handlerDone() || len(live) != 1 || !live[0].Leased || live[0].Deliveries != 1 {
		t.Errorf("past its first lease, the message is %+v, the handler done: %v", live, b.handlerDone())
	}
	close(b.release)
	until(t, "the message acknowledged", func() bool { return len(liveOf(t, b.sub.broker)) == 0 })
}

// Without AutoExtend, a handler is stopped before its lease ends: the
// message is never held by two handlers at once.
func TestWithoutAutoExtendAHandlerStopsBeforeItsLease(t *testing.T) {
	clk := manualAt()
	b := newBlockingTicks("lapsing", AutoExtend(false))
	a := startConsumers(t, b.svc, clk)
	stopAtCleanup(t, a)
	must(t, b.ticks.Publish(t.Context(), tick{N: 1}))
	<-b.started
	// The bound is armed before the handler starts: a second short of the
	// lease, past its margin, the handler hears it.
	clk.Advance(defaultLease - time.Second)
	until(t, "the handler stopped within its lease", b.handlerDone)
}

// keyedRun is a queued command's input: its key.
type keyedRun struct {
	Key string `json:"key"`
}

// A queued dispatch the stop's drain cuts short on its last delivery keeps
// its key: its message is handed back uncounted and waits for the next
// start, so a dispatch with the same key is deduplicated, not queued a
// second time beside it.
func TestADispatchTheStopCutsShortOnItsLastDeliveryKeepsItsKey(t *testing.T) {
	clk := manualAt()
	started := make(chan struct{}, 1)
	var handlerCtx atomic.Pointer[context.Context]
	svc := NewService("cutkey", "A queued command whose handler waits for its stop.")
	cmd := svc.Command("run", func(ctx context.Context, _ keyedRun) (EmptyValue, error) {
		handlerCtx.Store(&ctx)
		started <- struct{}{}
		<-ctx.Done()
		return EmptyValue{}, ctx.Err()
	}, Queued(), MaxDeliveries(1), Settle(time.Hour), Lease(2*time.Hour)).Key(func(in keyedRun) string { return in.Key })
	cut := func() bool {
		ctx := handlerCtx.Load()
		return ctx != nil && (*ctx).Err() != nil
	}
	a := startConsumers(t, svc, clk)
	q := cmd.queue.Load()
	must(t, cmd.accept(t.Context(), a, "", keyedRun{Key: "k"}, time.Time{}))
	<-started
	began := clk.Now()
	stopped := stopInBackground(a)
	stopping(t, a)
	for !cut() {
		if clk.Since(began) > 50*time.Minute {
			t.Fatal("the drain never ended")
		}
		clk.Advance(time.Second)
		for range 64 {
			if cut() {
				break
			}
			runtime.Gosched()
		}
	}
	must(t, <-stopped)
	if live := liveOf(t, q.broker); len(live) != 1 || live[0].Deliveries != 0 {
		t.Fatalf("the dispatch cut short: %+v; want it waiting, its delivery uncounted", live)
	}
	q.mu.Lock()
	held := q.pending["k"]
	q.mu.Unlock()
	if !held {
		t.Error("the stop gave back the key of a dispatch still in its queue")
	}
	must(t, cmd.accept(t.Context(), a, "", keyedRun{Key: "k"}, time.Time{}))
	if live := liveOf(t, q.broker); len(live) != 1 {
		t.Errorf("a dispatch with the same key was queued beside the one cut short: %d messages", len(live))
	}
}
