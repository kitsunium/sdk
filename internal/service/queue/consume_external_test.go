package queue_test

import (
	"context"
	"errors"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kitsunium/sdk/internal/kernel/clock"
	"github.com/kitsunium/sdk/internal/kernel/errs"

	corequeue "github.com/kitsunium/sdk/internal/core/queue"
	svcqueue "github.com/kitsunium/sdk/internal/service/queue"
)

// fastPoll keeps the engine's idle wait short enough that these tests finish
// promptly. The engine waits through kernel/clock, never time.Sleep.
const fastPoll time.Duration = time.Millisecond

// engineBudget bounds how long a test waits for the engine to do a thing
// before declaring it broken.
const engineBudget time.Duration = 10 * time.Second

// TestConsumeRefusesAHandlerWhoseAuthorHasNotAssertedIdempotence is the
// domain's one piece of deliberate ceremony, and the reason it exists: this
// queue delivers AT LEAST ONCE, the SDK cannot check whether a handler
// survives that, so the caller asserts it in code where a reviewer sees it —
// the resilience.HedgeConfig.Idempotent instrument.
func TestConsumeRefusesAHandlerWhoseAuthorHasNotAssertedIdempotence(t *testing.T) {
	t.Parallel()
	broker := makeMemory(t, clock.NewManualClock(epoch), defaultPolicy())
	err := svcqueue.Consume(t.Context(), broker, svcqueue.ConsumerConfig{
		Handler: func(context.Context, corequeue.DeliveryValue) error { return nil },
		//: HandlerIsIdempotent deliberately left at its zero value.
	})
	if !errs.HasCode(err, svcqueue.CodeConsumerMisconfigured) {
		t.Fatalf("Consume() = %v, want CodeConsumerMisconfigured", err)
	}
}

// TestConsumeRefusesANilHandler covers the other half of the same refusal: a
// consumer that leases messages and drops them is worse than one that does
// not run.
func TestConsumeRefusesANilHandler(t *testing.T) {
	t.Parallel()
	broker := makeMemory(t, clock.NewManualClock(epoch), defaultPolicy())
	err := svcqueue.Consume(t.Context(), broker, svcqueue.ConsumerConfig{HandlerIsIdempotent: true})
	if !errs.HasCode(err, svcqueue.CodeConsumerMisconfigured) {
		t.Fatalf("Consume() = %v, want CodeConsumerMisconfigured", err)
	}
}

// TestConsumeRunsTheHandlerOnItsOwnGoroutineAndAcknowledges is the third axis
// of ADR 0053's frontier made concrete: the handler runs on a goroutine
// Consume owns, long after Publish returned.
func TestConsumeRunsTheHandlerOnItsOwnGoroutineAndAcknowledges(t *testing.T) {
	t.Parallel()
	const count int = 20
	broker := makeMemory(t, clock.System, defaultPolicy())
	for index := range count {
		publish(t, broker, strconv.Itoa(index))
	}
	var handled atomic.Int64
	runEngine(t, broker, svcqueue.ConsumerConfig{
		Handler: func(context.Context, corequeue.DeliveryValue) error {
			handled.Add(1)
			return nil
		},
		HandlerIsIdempotent: true, PollInterval: fastPoll,
	}, func() bool { return handled.Load() == int64(count) })

	//: acknowledged, so nothing comes back.
	receiveNone(t, broker)
}

// TestAFailingHandlerIsRetriedAndThenDeadLettered walks the failure path
// through the engine rather than through hand-written Nacks, and checks that
// the handler's error is what the dead letter kept.
func TestAFailingHandlerIsRetriedAndThenDeadLettered(t *testing.T) {
	t.Parallel()
	broker := makeMemory(t, clock.System, corequeue.PolicyValue{
		VisibilityTimeout: time.Minute, MaxDeliveries: testDeliveries,
	})
	publish(t, broker, "always-fails")
	cause := errs.Define(0x00_03_35_FF, "TEST_HANDLER_CAUSE", "the handler refused", "private half")

	var attempts atomic.Int64
	runEngine(t, broker, svcqueue.ConsumerConfig{
		Handler: func(context.Context, corequeue.DeliveryValue) error {
			attempts.Add(1)
			return cause
		},
		HandlerIsIdempotent: true, PollInterval: fastPoll,
	}, func() bool { return attempts.Load() == int64(testDeliveries) })

	dead := deadLetters(t, broker, 10)
	if len(dead) != 1 {
		t.Fatalf("DeadLetters() returned %d records, want 1", len(dead))
	}
	if dead[0].Reason != "TEST_HANDLER_CAUSE" {
		t.Fatalf("Reason = %q, want the handler's own reason", dead[0].Reason)
	}
	if dead[0].Cause == "private half" {
		t.Fatal("the dead letter kept the log-only Private half")
	}
	if got := attempts.Load(); got != int64(testDeliveries) {
		t.Fatalf("the handler ran %d times, want exactly %d", got, testDeliveries)
	}
}

// TestAPanickingHandlerIsRecoveredAndTheConsumerKeepsRunning pins the rule a
// panic escaping one handler would break: it would kill the consumer PROCESS,
// abandoning every other in-flight lease, each of which would then have to
// time out one visibility timeout at a time.
func TestAPanickingHandlerIsRecoveredAndTheConsumerKeepsRunning(t *testing.T) {
	t.Parallel()
	broker := makeMemory(t, clock.System, corequeue.PolicyValue{
		VisibilityTimeout: time.Minute, MaxDeliveries: 1,
	})
	publish(t, broker, "explodes")
	publish(t, broker, "fine")

	var survived atomic.Bool
	var crashes atomic.Int64
	runEngine(t, broker, svcqueue.ConsumerConfig{
		Handler: func(_ context.Context, delivery corequeue.DeliveryValue) error {
			if string(delivery.Message.Payload) == "explodes" {
				crashes.Add(1)
				panic("the handler exploded")
			}
			survived.Store(true)
			return nil
		},
		HandlerIsIdempotent: true, PollInterval: fastPoll,
	}, func() bool { return survived.Load() && crashes.Load() >= 1 })

	dead := deadLetters(t, broker, 10)
	if len(dead) != 1 || dead[0].Reason != "HANDLER_PANICKED" {
		t.Fatalf("dead letters = %+v, want one HANDLER_PANICKED record", dead)
	}
	if !containsSubstring(dead[0].Cause, "panicked") {
		t.Fatalf("dead letter Cause = %q, want the recovered panic's public half", dead[0].Cause)
	}
}

// TestConsumeReturnsNilWhenItsContextEnds pins that a cancelled consumer is a
// STOPPED consumer and not a failed one — and that every worker has returned
// by the time Consume does, so a caller that cancels and waits knows nothing
// is still running.
//
// GOROUTINE LIFECYCLE: one goroutine runs Consume; it is ended by the cancel
// below and joined by running.Wait before the assertion, so the test cannot
// outlive it and the result is read after the write that produced it.
func TestConsumeReturnsNilWhenItsContextEnds(t *testing.T) {
	t.Parallel()
	const parallelism int = 4
	broker := makeMemory(t, clock.System, defaultPolicy())
	ctx, cancel := context.WithCancel(t.Context())
	var running sync.WaitGroup
	var result error
	running.Go(func() {
		result = svcqueue.Consume(ctx, broker, svcqueue.ConsumerConfig{
			Handler:             func(context.Context, corequeue.DeliveryValue) error { return nil },
			HandlerIsIdempotent: true, PollInterval: fastPoll, Parallelism: parallelism,
		})
	})
	cancel()
	running.Wait()
	if result != nil {
		t.Fatalf("Consume() = %v, want nil for a cancelled consumer", result)
	}
}

// TestParallelismRunsHandlersConcurrently pins the field that makes this the
// consumer's goroutine PLURAL: four workers, and a handler that will not
// return until all four are inside it at once.
func TestParallelismRunsHandlersConcurrently(t *testing.T) {
	t.Parallel()
	const workers int = 4
	broker := makeMemory(t, clock.System, corequeue.PolicyValue{
		VisibilityTimeout: time.Minute, MaxDeliveries: 1,
	})
	for index := range workers {
		publish(t, broker, strconv.Itoa(index))
	}
	together := make(chan struct{})
	var inside atomic.Int64
	runEngine(t, broker, svcqueue.ConsumerConfig{
		Handler: func(ctx context.Context, _ corequeue.DeliveryValue) error {
			if inside.Add(1) == int64(workers) {
				close(together)
			}
			select {
			case <-together:
				//: all four are inside at the same moment.
				return nil
			case <-ctx.Done():
				//: the engine stopped before they all arrived.
				return ctx.Err()
			}
		},
		HandlerIsIdempotent: true, PollInterval: fastPoll, Parallelism: workers,
	}, func() bool {
		select {
		case <-together:
			return true
		default:
			return false
		}
	})
}

// TestABatchIsLeasedAtOnceAndProcessedInOrder pins BatchSize's contract, and
// with it the thing its documentation warns about: the batch is held under a
// lease each while being processed one at a time.
func TestABatchIsLeasedAtOnceAndProcessedInOrder(t *testing.T) {
	t.Parallel()
	const count int = 6
	clk := clock.NewManualClock(epoch)
	broker := makeMemory(t, clk, corequeue.PolicyValue{
		VisibilityTimeout: time.Minute, MaxDeliveries: 1,
	})
	for index := range count {
		clk.Advance(1)
		publish(t, broker, strconv.Itoa(index))
	}
	var order sync.Mutex
	var seen []string
	runEngine(t, broker, svcqueue.ConsumerConfig{
		Handler: func(_ context.Context, delivery corequeue.DeliveryValue) error {
			order.Lock()
			defer order.Unlock()
			seen = append(seen, string(delivery.Message.Payload))
			return nil
		},
		HandlerIsIdempotent: true, PollInterval: fastPoll, BatchSize: count,
	}, func() bool {
		order.Lock()
		defer order.Unlock()
		return len(seen) == count
	})
	order.Lock()
	defer order.Unlock()
	for index := range count {
		if seen[index] != strconv.Itoa(index) {
			t.Fatalf("processed %v, want publication order", seen)
		}
	}
}

// runEngine starts Consume, waits for done to report true, then cancels and
// waits for every worker to return.
//
// GOROUTINE LIFECYCLE: one goroutine runs Consume, which itself owns
// cfg.Parallelism workers and joins them before returning. It is ended by the
// cancel on every path out of this function — including the budget expiry —
// and joined by running.Wait, so no goroutine outlives the test that started
// it and result is never read while it is being written.
func runEngine(t *testing.T, broker corequeue.Broker, cfg svcqueue.ConsumerConfig, done func() bool) {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	var running sync.WaitGroup
	var result error
	running.Go(func() { result = svcqueue.Consume(ctx, broker, cfg) })
	deadline := time.Now().Add(engineBudget)
	for !done() {
		if time.Now().After(deadline) {
			cancel()
			running.Wait()
			t.Fatalf("the consumer never reached the expected state within %v", engineBudget)
		}
		time.Sleep(fastPoll)
	}
	cancel()
	running.Wait()
	if result != nil && !errors.Is(result, context.Canceled) {
		t.Fatalf("Consume() = %v, want nil", result)
	}
}
