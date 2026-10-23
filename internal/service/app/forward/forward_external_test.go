package forward_test

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"testing"
	"time"

	kerrs "github.com/kitsunium/sdk/internal/kernel/errs"

	coreforward "github.com/kitsunium/sdk/internal/core/app/forward"
	corequeue "github.com/kitsunium/sdk/internal/core/data/queue"
	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	svcforward "github.com/kitsunium/sdk/internal/service/app/forward"
	svcqueue "github.com/kitsunium/sdk/internal/service/data/queue"
)

// TestNewRefusesWhatCouldNeverForward pins ADR 0185's refusals: the three
// required fields, a policy it does not know, and every negative bound —
// each named by field, none of them a default.
func TestNewRefusesWhatCouldNeverForward(t *testing.T) {
	broker, err := svcqueue.NewMemory(svcqueue.MemoryConfig{Policy: testPolicy})
	if err != nil {
		t.Fatalf("NewMemory: %v", err)
	}
	send := func(context.Context, []byte) error { return nil }
	valid := func() svcforward.Config {
		return svcforward.Config{Broker: broker, Send: send, Retryable: retryable}
	}
	cases := map[string]func(*svcforward.Config){
		"Broker":       func(c *svcforward.Config) { c.Broker = nil },
		"Send":         func(c *svcforward.Config) { c.Send = nil },
		"Retryable":    func(c *svcforward.Config) { c.Retryable = nil },
		"WhenFull":     func(c *svcforward.Config) { c.WhenFull = svcforward.Block + 1 },
		"MaxBatches":   func(c *svcforward.Config) { c.MaxBatches = -1 },
		"MaxBytes":     func(c *svcforward.Config) { c.MaxBytes = -1 },
		"BlockTimeout": func(c *svcforward.Config) { c.BlockTimeout = -time.Second },
		"SendTimeout":  func(c *svcforward.Config) { c.SendTimeout = -time.Second },
		"MaxAge":       func(c *svcforward.Config) { c.MaxAge = -time.Second },
		"RecountInterval": func(c *svcforward.Config) {
			c.RecountInterval = -time.Second
		},
	}
	for field, spoil := range cases {
		t.Run(field, func(t *testing.T) {
			cfg := valid()
			spoil(&cfg)
			_, err := svcforward.New(context.Background(), cfg)
			if !errors.Is(err, coreforward.ForwardMisconfigured) {
				t.Fatalf("New = %v, want ForwardMisconfigured", err)
			}
			if got := field0(err, "field"); got != field {
				t.Errorf("field = %q, want %q", got, field)
			}
		})
	}
	if _, err := svcforward.New(context.Background(), valid()); err != nil {
		t.Fatalf("New with the three required fields = %v, want nil", err)
	}
	if _, err := svcforward.NewSpanExporter("spans", nil); !errors.Is(err, coreforward.ForwardMisconfigured) {
		t.Errorf("NewSpanExporter(nil) = %v, want ForwardMisconfigured", err)
	}
	if _, err := svcforward.NewMetricExporter("metrics", nil); !errors.Is(err, coreforward.ForwardMisconfigured) {
		t.Errorf("NewMetricExporter(nil) = %v, want ForwardMisconfigured", err)
	}
}

// TestFullPolicyNamesItself pins String, the name a ForwardFull carries.
func TestFullPolicyNamesItself(t *testing.T) {
	for policy, want := range map[svcforward.FullPolicy]string{
		svcforward.DropOldest: "drop-oldest", svcforward.DropNewest: "drop-newest",
		svcforward.Block: "block", svcforward.Block + 1: "unknown",
	} {
		if got := policy.String(); got != want {
			t.Errorf("%d.String() = %q, want %q", policy, got, want)
		}
	}
}

// TestABatchIsSentAndCounted is the nominal path: kept, sent once, the
// bytes unchanged, acknowledged, the counters saying so.
func TestABatchIsSentAndCounted(t *testing.T) {
	fx := newFixture(t, svcforward.Config{})
	fx.enqueue(t, "batch-1")
	if stats := fx.forwarder.Stats(); stats.Pending != 1 || stats.PendingBytes != 7 || stats.Enqueued != 1 {
		t.Fatalf("after Enqueue stats = %+v, want one batch of 7 bytes pending", stats)
	}
	fx.run(t)
	if got := string(fx.sender.awaitCall(t)); got != "batch-1" {
		t.Fatalf("sent %q, want batch-1", got)
	}
	stats := fx.awaitStats(t, "sent", func(s svcforward.StatsValue) bool { return s.Sent == 1 })
	if stats.Pending != 0 || stats.PendingBytes != 0 || stats.Retries != 0 {
		t.Errorf("after the send stats = %+v, want nothing pending and no retry", stats)
	}
	if err := fx.forwarder.Enqueue(context.Background(), nil); err != nil || fx.forwarder.Stats().Enqueued != 1 {
		t.Errorf("an empty batch = %v, want nil and nothing kept", err)
	}
}

// TestTheCollectorDownThenBackIsRetriedOnTheBackoff pins D6's curve: each
// retryable failure waits the backoff of its attempt on the clock — three
// seconds, then six —, one request at a time, and the batch is sent once the
// collector answers.
func TestTheCollectorDownThenBackIsRetriedOnTheBackoff(t *testing.T) {
	fx := newFixture(t, svcforward.Config{}, transient, transient, nil)
	fx.enqueue(t, "batch-1")
	fx.run(t)
	fx.sender.awaitCall(t)
	fx.clock.awaitTimer(t, testBase)
	fx.clock.Advance(testBase)
	fx.sender.awaitCall(t)
	fx.clock.awaitTimer(t, 2*testBase)
	fx.clock.Advance(2 * testBase)
	if got := string(fx.sender.awaitCall(t)); got != "batch-1" {
		t.Fatalf("third attempt sent %q, want batch-1", got)
	}
	stats := fx.awaitStats(t, "sent after two retries", func(s svcforward.StatsValue) bool { return s.Sent == 1 })
	if stats.Retries != 2 || stats.Pending != 0 || stats.DeadLettered != 0 {
		t.Errorf("stats = %+v, want two retries and the batch sent", stats)
	}
}

// TestARetryAfterIsWaitedInFull pins D6's other half: a 429 whose collector
// says 120 s is retried after exactly 120 s — not the 3 s of the curve, not
// its 12 s ceiling — and the delay is what Stats reports last.
func TestARetryAfterIsWaitedInFull(t *testing.T) {
	fx := newFixture(t, svcforward.Config{}, retryAfter(120), nil)
	fx.enqueue(t, "batch-1")
	fx.run(t)
	fx.sender.awaitCall(t)
	fx.clock.awaitTimer(t, 120*time.Second)
	if stats := fx.forwarder.Stats(); stats.LastRetryAfter != 120*time.Second || !stats.LastRetryAfterAt.Equal(epoch) {
		t.Errorf("stats = %+v, want the collector's 120 s recorded at the epoch", stats)
	}
	fx.clock.Advance(120 * time.Second)
	fx.sender.awaitCall(t)
	fx.awaitStats(t, "sent after the imposed delay", func(s svcforward.StatsValue) bool { return s.Sent == 1 })
}

// TestARetryAfterNeverOutlivesTheBatch pins the bound on an imposed delay: a
// collector asking for a day is waited only until the batch's MaxAge, and the
// batch, still refused, is then dropped as expired.
func TestARetryAfterNeverOutlivesTheBatch(t *testing.T) {
	fx := newFixture(t, svcforward.Config{}, retryAfter(86_400))
	fx.enqueue(t, "batch-1")
	fx.run(t)
	fx.sender.awaitCall(t)
	fx.clock.awaitTimer(t, testMaxAge)
	fx.clock.Advance(testMaxAge)
	fx.sender.awaitCall(t)
	stats := fx.awaitStats(t, "expired", func(s svcforward.StatsValue) bool { return s.Expired == 1 })
	if stats.Pending != 0 || stats.Sent != 0 || stats.DeadLettered != 0 {
		t.Errorf("stats = %+v, want the batch expired and nothing else", stats)
	}
}

// TestARejectedBatchIsDeadLetteredOnce pins D7: a permanent refusal — a 400
// — is not retried, lands in the broker's dead-letter store with the
// collector's verdict, and is counted.
func TestARejectedBatchIsDeadLetteredOnce(t *testing.T) {
	fx := newFixture(t, svcforward.Config{}, permanent)
	fx.enqueue(t, "batch-1")
	fx.run(t)
	fx.sender.awaitCall(t)
	stats := fx.awaitStats(t, "dead-lettered", func(s svcforward.StatsValue) bool { return s.DeadLettered == 1 })
	if stats.Retries != 0 || stats.Pending != 0 || stats.Sent != 0 {
		t.Errorf("stats = %+v, want one dead letter and no retry", stats)
	}
	reader, ok := fx.broker.(corequeue.DeadLetterReader)
	if !ok {
		t.Fatal("the memory broker reads no dead letters")
	}
	var dead []corequeue.DeadLetterValue
	deadline := time.Now().Add(waitBudget)
	for len(dead) == 0 && time.Now().Before(deadline) {
		var err error
		if dead, err = reader.DeadLetters(context.Background(), 10); err != nil {
			t.Fatalf("DeadLetters: %v", err)
		}
	}
	if len(dead) != 1 || string(dead[0].Message.Payload) != "batch-1" || dead[0].Reason != "OTLP_EXPORT_REJECTED" {
		t.Fatalf("dead letters = %+v, want batch-1 with the collector's verdict", dead)
	}
	select {
	case batch := <-fx.sender.calls:
		t.Errorf("a dead-lettered batch was sent again: %q", batch)
	default:
	}
}

// TestAnExpiredBatchIsDroppedUnsent pins D6's age: a batch delivered after
// MaxAge is acknowledged without being sent, and counted Expired.
func TestAnExpiredBatchIsDroppedUnsent(t *testing.T) {
	fx := newFixture(t, svcforward.Config{})
	fx.enqueue(t, "stale")
	fx.clock.Advance(testMaxAge)
	fx.run(t)
	stats := fx.awaitStats(t, "expired", func(s svcforward.StatsValue) bool { return s.Expired == 1 })
	if stats.Pending != 0 || stats.Sent != 0 {
		t.Errorf("stats = %+v, want the batch expired unsent", stats)
	}
	select {
	case batch := <-fx.sender.calls:
		t.Errorf("an expired batch was sent: %q", batch)
	default:
	}
}

// TestDropOldestKeepsTheNewest pins D5's zero: at its bound the forwarder
// takes the oldest batch out, counts it, and keeps the new one.
func TestDropOldestKeepsTheNewest(t *testing.T) {
	fx := newFixture(t, svcforward.Config{MaxBatches: 2})
	for i := range 3 {
		fx.enqueue(t, fmt.Sprintf("batch-%d", i))
	}
	if stats := fx.forwarder.Stats(); stats.Pending != 2 || stats.Dropped != 1 || stats.Enqueued != 3 {
		t.Fatalf("stats = %+v, want two pending, one dropped", stats)
	}
	fx.run(t)
	got := []string{string(fx.sender.awaitCall(t)), string(fx.sender.awaitCall(t))}
	if got[0] != "batch-1" || got[1] != "batch-2" {
		t.Errorf("sent %v, want the two newest in order", got)
	}
}

// TestDropOldestCountsBytes pins the second bound: bytes, not batches.
func TestDropOldestCountsBytes(t *testing.T) {
	fx := newFixture(t, svcforward.Config{MaxBytes: 10})
	fx.enqueue(t, "aaaa")
	fx.enqueue(t, "bbbb")
	fx.enqueue(t, "cccc")
	if stats := fx.forwarder.Stats(); stats.PendingBytes != 8 || stats.Dropped != 1 {
		t.Fatalf("stats = %+v, want 8 bytes pending, one dropped", stats)
	}
	err := fx.forwarder.Enqueue(context.Background(), []byte("0123456789x"))
	if !errors.Is(err, coreforward.BatchTooLarge) || field0(err, "bytes") != "11" {
		t.Fatalf("an 11-byte batch under a 10-byte bound = %v, want BatchTooLarge naming its size", err)
	}
	if stats := fx.forwarder.Stats(); stats.Dropped != 2 || stats.Pending != 2 {
		t.Errorf("stats = %+v, want the oversized batch counted dropped", stats)
	}
}

// TestDropOldestFallsBackWhenNothingCanBeTaken pins the fallback: the only
// batch held is being sent, so the new one is the one refused.
func TestDropOldestFallsBackWhenNothingCanBeTaken(t *testing.T) {
	fx := newFixture(t, svcforward.Config{MaxBatches: 1})
	fx.sender.hold = make(chan struct{})
	fx.enqueue(t, "in-flight")
	fx.run(t)
	fx.sender.awaitCall(t)
	err := fx.forwarder.Enqueue(context.Background(), []byte("newest"))
	if !errors.Is(err, coreforward.ForwardFull) || field0(err, "policy") != "drop-oldest" {
		t.Fatalf("Enqueue = %v, want ForwardFull under drop-oldest", err)
	}
	close(fx.sender.hold)
	fx.awaitStats(t, "sent", func(s svcforward.StatsValue) bool { return s.Sent == 1 && s.Dropped == 1 })
}

// TestDropNewestRefusesTheNewBatch pins DropNewest: the queue keeps what it
// has, the new batch is refused with ForwardFull and counted.
func TestDropNewestRefusesTheNewBatch(t *testing.T) {
	fx := newFixture(t, svcforward.Config{MaxBatches: 1, WhenFull: svcforward.DropNewest})
	fx.enqueue(t, "kept")
	err := fx.forwarder.Enqueue(context.Background(), []byte("refused"))
	if !errors.Is(err, coreforward.ForwardFull) || field0(err, "policy") != "drop-newest" {
		t.Fatalf("Enqueue = %v, want ForwardFull under drop-newest", err)
	}
	if stats := fx.forwarder.Stats(); stats.Pending != 1 || stats.Dropped != 1 || stats.Enqueued != 1 {
		t.Errorf("stats = %+v, want the first kept and the second dropped", stats)
	}
	fx.run(t)
	if got := string(fx.sender.awaitCall(t)); got != "kept" {
		t.Errorf("sent %q, want kept", got)
	}
}

// TestBlockWaitsForRoomThenGivesUp pins Block's bound: a producer waits on
// the clock for BlockTimeout, then is refused with ForwardFull and counted.
func TestBlockWaitsForRoomThenGivesUp(t *testing.T) {
	fx := newFixture(t, svcforward.Config{MaxBatches: 1, WhenFull: svcforward.Block})
	fx.enqueue(t, "kept")
	result := make(chan error, 1)
	go func() { result <- fx.forwarder.Enqueue(context.Background(), []byte("waiting")) }()
	fx.clock.awaitTimer(t, testBlock)
	fx.clock.Advance(testBlock)
	if err := <-result; !errors.Is(err, coreforward.ForwardFull) || field0(err, "policy") != "block" {
		t.Fatalf("Enqueue = %v, want ForwardFull under block", err)
	}
	if stats := fx.forwarder.Stats(); stats.Dropped != 1 || stats.Pending != 1 {
		t.Errorf("stats = %+v, want one dropped, one pending", stats)
	}
}

// TestBlockIsReleasedByASend pins Block's purpose: a batch the consumer
// sends makes room, and the waiting producer's batch is kept.
func TestBlockIsReleasedByASend(t *testing.T) {
	fx := newFixture(t, svcforward.Config{MaxBatches: 1, WhenFull: svcforward.Block})
	fx.enqueue(t, "first")
	result := make(chan error, 1)
	go func() { result <- fx.forwarder.Enqueue(context.Background(), []byte("second")) }()
	fx.clock.awaitTimer(t, testBlock)
	fx.run(t)
	if err := <-result; err != nil {
		t.Fatalf("Enqueue = %v, want the batch kept once the first was sent", err)
	}
	got := []string{string(fx.sender.awaitCall(t)), string(fx.sender.awaitCall(t))}
	if got[0] != "first" || got[1] != "second" {
		t.Errorf("sent %v, want first then second", got)
	}
	fx.awaitStats(t, "both sent", func(s svcforward.StatsValue) bool { return s.Sent == 2 && s.Dropped == 0 })
}

// TestBlockEndsWithItsCaller pins that the caller's context bounds the wait
// too: a producer whose context ends is refused with its reason, counted.
func TestBlockEndsWithItsCaller(t *testing.T) {
	fx := newFixture(t, svcforward.Config{MaxBatches: 1, WhenFull: svcforward.Block})
	fx.enqueue(t, "kept")
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() { result <- fx.forwarder.Enqueue(ctx, []byte("waiting")) }()
	fx.clock.awaitTimer(t, testBlock)
	cancel()
	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatalf("Enqueue = %v, want the caller's cancellation", err)
	}
	if stats := fx.forwarder.Stats(); stats.Dropped != 1 {
		t.Errorf("stats = %+v, want the abandoned batch counted dropped", stats)
	}
}

// TestAStopHandsTheBatchBackUncounted pins D9: a batch being sent when Run's
// context ends is handed back, still pending, and sent by the next run.
func TestAStopHandsTheBatchBackUncounted(t *testing.T) {
	fx := newFixture(t, svcforward.Config{})
	fx.sender.hold = make(chan struct{})
	fx.enqueue(t, "batch-1")
	fx.run(t)
	fx.sender.awaitCall(t)
	fx.halt(t)
	stats := fx.forwarder.Stats()
	if stats.Pending != 1 || stats.Sent != 0 || stats.Expired != 0 || stats.DeadLettered != 0 {
		t.Fatalf("after the stop stats = %+v, want the batch still pending", stats)
	}
	fx.sender.mu.Lock()
	fx.sender.hold = nil
	fx.sender.mu.Unlock()
	fx.run(t)
	if got := string(fx.sender.awaitCall(t)); got != "batch-1" {
		t.Fatalf("the next run sent %q, want batch-1", got)
	}
	fx.awaitStats(t, "sent by the next run", func(s svcforward.StatsValue) bool { return s.Sent == 1 && s.Pending == 0 })
}

// TestAnAttemptIsBoundedBySendTimeout pins the bound on one attempt: a
// collector that never answers is abandoned when SendTimeout passes on the
// clock, and the attempt counts as a retryable failure.
func TestAnAttemptIsBoundedBySendTimeout(t *testing.T) {
	fx := newFixture(t, svcforward.Config{}, nil)
	fx.sender.hold = make(chan struct{})
	fx.enqueue(t, "batch-1")
	fx.run(t)
	fx.sender.awaitCall(t)
	fx.clock.awaitTimer(t, testSendTimeout)
	fx.clock.Advance(testSendTimeout)
	fx.clock.awaitTimer(t, testBase)
	fx.sender.mu.Lock()
	fx.sender.hold = nil
	fx.sender.mu.Unlock()
	fx.clock.Advance(testBase)
	fx.sender.awaitCall(t)
	fx.awaitStats(t, "sent after the bound", func(s svcforward.StatsValue) bool { return s.Sent == 1 && s.Retries == 1 })
}

// TestTheBatchesSurviveARestart pins the point of the package: batches kept
// by one process — its broker closed, nothing sent — are found by the next,
// counted against the bounds from the start, and sent by it.
func TestTheBatchesSurviveARestart(t *testing.T) {
	dir := t.TempDir()
	clk := newTimerClock()
	first := newFixtureOn(t, clk, fileBroker(t, clk, dir), svcforward.Config{MaxBatches: 3})
	for i := range 3 {
		first.enqueue(t, fmt.Sprintf("batch-%d", i))
	}
	closeBroker(t, first.broker)

	second := newFixtureOn(t, clk, fileBroker(t, clk, dir), svcforward.Config{MaxBatches: 3, WhenFull: svcforward.DropNewest})
	t.Cleanup(func() { closeBroker(t, second.broker) })
	if stats := second.forwarder.Stats(); stats.Pending != 3 || stats.PendingBytes != 21 {
		t.Fatalf("after the restart stats = %+v, want the three batches the last process kept", stats)
	}
	if err := second.forwarder.Enqueue(context.Background(), []byte("over")); !errors.Is(err, coreforward.ForwardFull) {
		t.Fatalf("Enqueue past the bound after a restart = %v, want ForwardFull", err)
	}
	second.run(t)
	sent := map[string]bool{}
	for range 3 {
		sent[string(second.sender.awaitCall(t))] = true
	}
	for i := range 3 {
		if !sent[fmt.Sprintf("batch-%d", i)] {
			t.Errorf("batch-%d was not sent after the restart: %v", i, sent)
		}
	}
	second.awaitStats(t, "all sent", func(s svcforward.StatsValue) bool { return s.Sent == 3 && s.Pending == 0 })
}

// fileBroker opens the durable broker in dir on clk.
func fileBroker(t *testing.T, clk *timerClock, dir string) corequeue.Broker {
	t.Helper()
	broker, err := svcqueue.NewFile(svcqueue.FileConfig{Clock: clk, Dir: dir, Policy: testPolicy})
	if diskRefused() {
		// The durable broker publishes through internal/service/data/vfs,
		// which refuses this platform by design (ADR 0018, ADR 0056, ADR
		// 0144): the refusal is asserted, then the case skips — the broker
		// it needs does not exist here (ADR 0095).
		if !kerrs.HasCode(err, coreproc.CodeUnsupportedPlatform) || broker != nil {
			t.Fatalf("NewFile on %s = (%v, %v), want (nil, UNSUPPORTED_PLATFORM)", runtime.GOOS, broker, err)
		}
		t.Skipf("the durable broker refuses %s by design; that refusal is asserted above", runtime.GOOS)
	}
	if err != nil {
		t.Fatalf("NewFile: %v", err)
	}
	return broker
}

// diskRefused reports whether internal/service/data/vfs refuses this GOOS by
// design — spelled out, as the queue suite does, so a refusal on Linux, macOS
// or a BSD fails instead of skipping.
func diskRefused() bool {
	switch runtime.GOOS {
	case "linux", "android", "darwin", "ios", "freebsd", "openbsd", "netbsd", "dragonfly":
		return false
	}
	return true
}

// closeBroker closes a broker that can be closed — a process ending.
func closeBroker(t *testing.T, broker corequeue.Broker) {
	t.Helper()
	if closer, ok := broker.(interface{ Close() error }); ok {
		if err := closer.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}
	}
}

// field0 renders the outermost errs field named key, or "".
func field0(err error, key string) string {
	for _, field := range kerrs.FieldsOf(err) {
		if field.Key() == key {
			return field.StringValue()
		}
	}
	return ""
}
