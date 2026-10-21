package forward_test

import (
	"context"
	"errors"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/kitsunium/sdk/internal/kernel/clock"
	kerrs "github.com/kitsunium/sdk/internal/kernel/errs"

	corequeue "github.com/kitsunium/sdk/internal/core/data/queue"
	coretrace "github.com/kitsunium/sdk/internal/core/observe/trace"
	svcforward "github.com/kitsunium/sdk/internal/service/app/forward"
	svcqueue "github.com/kitsunium/sdk/internal/service/data/queue"
	svctrace "github.com/kitsunium/sdk/internal/service/observe/trace"
)

// The numbers every case shares, each distinct from the others so a timer's
// duration says which wait it is.
const (
	testSendTimeout time.Duration = 41 * time.Second
	testBase        time.Duration = 3 * time.Second
	testCeiling     time.Duration = 12 * time.Second
	testMaxAge      time.Duration = 2 * time.Hour
	testBlock       time.Duration = 7 * time.Second
	// testLease is longer than any advance a case makes, so no lease, no
	// renewal and no margin before a lease ends fires inside a case.
	testLease time.Duration = 96 * time.Hour
	// waitBudget turns a regression into a failure rather than a hang. It is
	// read on the wall clock and never slept on.
	waitBudget time.Duration = 20 * time.Second
)

// epoch anchors every ManualClock, so an instant in a failure is readable.
var epoch = time.Date(2026, time.October, 21, 9, 0, 0, 0, time.UTC)

// testPolicy is the queue every case builds: a lease no case outlives, and
// deliveries no case exhausts — the forwarder's budget is its age.
var testPolicy = corequeue.PolicyValue{
	VisibilityTimeout: testLease,
	MaxDeliveries:     1_000_000,
	MaxMessageBytes:   1 << 20,
}

// timerClock is a ManualClock that says each timer's duration once the timer
// is armed, so a case advances exactly past the wait it expects — never
// before the wait exists.
type timerClock struct {
	*clock.ManualClock
	// armed carries every timer's duration, in order.
	armed chan time.Duration
}

// newTimerClock returns a timerClock at epoch.
func newTimerClock() *timerClock {
	return &timerClock{ManualClock: clock.NewManualClock(epoch), armed: make(chan time.Duration, 4096)}
}

// NewTimer arms the timer, then reports its duration.
func (c *timerClock) NewTimer(d time.Duration) clock.Timer {
	timer := c.ManualClock.NewTimer(d)
	select {
	case c.armed <- d:
	default:
	}
	return timer
}

// awaitTimer waits for a timer of exactly d to be armed, skipping the others.
func (c *timerClock) awaitTimer(t *testing.T, d time.Duration) {
	t.Helper()
	deadline := time.After(waitBudget)
	for {
		select {
		case got := <-c.armed:
			if got == d {
				return
			}
		case <-deadline:
			t.Fatalf("no timer of %v was armed", d)
		}
	}
}

// sender is a scripted collector: each call takes the next verdict, the last
// one repeating, and reports the batch it was given.
type sender struct {
	mu       sync.Mutex
	verdicts []error
	calls    chan []byte
	hold     chan struct{}
}

// newSender returns a sender answering verdicts in turn.
func newSender(verdicts ...error) *sender {
	return &sender{verdicts: verdicts, calls: make(chan []byte, 1024)}
}

// send is the Sender.
func (s *sender) send(ctx context.Context, batch []byte) error {
	s.calls <- append([]byte(nil), batch...)
	s.mu.Lock()
	hold := s.hold
	var verdict error
	if len(s.verdicts) > 0 {
		verdict = s.verdicts[0]
		if len(s.verdicts) > 1 {
			s.verdicts = s.verdicts[1:]
		}
	}
	s.mu.Unlock()
	if hold != nil {
		select {
		case <-hold:
		case <-ctx.Done():
			return kerrs.Wrap(transient, kerrs.WrapParams{}, kerrs.String("cause", ctx.Err().Error()))
		}
	}
	return verdict
}

// awaitCall returns the next batch the sender was given.
func (s *sender) awaitCall(t *testing.T) []byte {
	t.Helper()
	select {
	case batch := <-s.calls:
		return batch
	case <-time.After(waitBudget):
		t.Fatal("the sender was never called")
		return nil
	}
}

// transient is a retryable failure — the collector unavailable — and
// permanent one that is not — the batch rejected.
var (
	transient = coretrace.OTLPExportUnavailable
	permanent = coretrace.OTLPExportRejected
)

// retryAfter is a transient failure whose collector said to wait seconds.
func retryAfter(seconds int) error {
	return kerrs.Wrap(transient, kerrs.WrapParams{}, kerrs.Int("retry_after_seconds", seconds))
}

// retryable is the classification every case configures.
var retryable = svctrace.OTLPRetryable

// fixture is one forwarder over one broker on one clock.
type fixture struct {
	clock     *timerClock
	broker    corequeue.Broker
	sender    *sender
	forwarder *svcforward.Forwarder
	stop      context.CancelFunc
	done      chan error
}

// newFixture builds a forwarder over a memory broker with cfg's policy
// fields, the sender answering verdicts.
func newFixture(t *testing.T, cfg svcforward.Config, verdicts ...error) *fixture {
	t.Helper()
	clk := newTimerClock()
	broker, err := svcqueue.NewMemory(svcqueue.MemoryConfig{Clock: clk, Policy: testPolicy})
	if err != nil {
		t.Fatalf("NewMemory: %v", err)
	}
	return newFixtureOn(t, clk, broker, cfg, verdicts...)
}

// newFixtureOn builds a forwarder over broker.
func newFixtureOn(t *testing.T, clk *timerClock, broker corequeue.Broker, cfg svcforward.Config, verdicts ...error) *fixture {
	t.Helper()
	fx := &fixture{clock: clk, broker: broker, sender: newSender(verdicts...)}
	cfg.Broker, cfg.Send, cfg.Clock = broker, fx.sender.send, clk
	if cfg.Retryable == nil {
		cfg.Retryable = retryable
	}
	cfg.SendTimeout = orDuration(cfg.SendTimeout, testSendTimeout)
	cfg.MaxAge = orDuration(cfg.MaxAge, testMaxAge)
	cfg.BlockTimeout = orDuration(cfg.BlockTimeout, testBlock)
	if cfg.Backoff.BaseDelay == 0 {
		cfg.Backoff.BaseDelay, cfg.Backoff.MaxDelay = testBase, testCeiling
	}
	cfg.PollInterval = orDuration(cfg.PollInterval, time.Hour)
	forwarder, err := svcforward.New(context.Background(), cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	fx.forwarder = forwarder
	return fx
}

// orDuration returns d, or fallback when d is zero.
func orDuration(d, fallback time.Duration) time.Duration {
	if d == 0 {
		return fallback
	}
	return d
}

// run starts the forwarder's consumer; the case's cleanup stops it.
func (fx *fixture) run(t *testing.T) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	fx.stop, fx.done = cancel, make(chan error, 1)
	go func() { fx.done <- fx.forwarder.Run(ctx) }()
	t.Cleanup(func() { fx.halt(t) })
}

// halt stops the consumer and waits for it, once.
func (fx *fixture) halt(t *testing.T) {
	t.Helper()
	if fx.stop == nil {
		return
	}
	fx.stop()
	fx.stop = nil
	select {
	case err := <-fx.done:
		if err != nil && !errors.Is(err, context.Canceled) {
			t.Errorf("Run = %v", err)
		}
	case <-time.After(waitBudget):
		t.Error("Run did not return after its context ended")
	}
}

// enqueue keeps batch, failing the case on a refusal.
func (fx *fixture) enqueue(t *testing.T, batch string) {
	t.Helper()
	if err := fx.forwarder.Enqueue(context.Background(), []byte(batch)); err != nil {
		t.Fatalf("Enqueue(%q): %v", batch, err)
	}
}

// awaitStats waits until the forwarder's counters satisfy ok, yielding the
// processor between reads; it never sleeps.
func (fx *fixture) awaitStats(t *testing.T, what string, ok func(svcforward.StatsValue) bool) svcforward.StatsValue {
	t.Helper()
	deadline := time.Now().Add(waitBudget)
	for {
		stats := fx.forwarder.Stats()
		if ok(stats) {
			return stats
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s: never reached, stats = %+v", what, stats)
		}
		runtime.Gosched()
	}
}
