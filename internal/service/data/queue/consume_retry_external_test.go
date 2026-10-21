package queue_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/kitsunium/sdk/internal/kernel/clock"

	corequeue "github.com/kitsunium/sdk/internal/core/data/queue"
	svcqueue "github.com/kitsunium/sdk/internal/service/data/queue"
)

// retryAsked is the delay the handlers below ask for: well past the policy's
// RetryDelay, so a message back at testRetryDelay was nacked the old way.
const retryAsked time.Duration = 30 * time.Second

// errThrottled is a downstream's refusal that says when to come back.
var errThrottled = errors.New("the downstream answered 429")

// consumeInBackground starts Consume over broker and returns the function
// that stops it and waits for every worker.
//
// GOROUTINE LIFECYCLE: one goroutine runs Consume; the returned stop cancels
// its context and joins it, and the test calls stop on every path through
// t.Cleanup, so no goroutine outlives the test.
func consumeInBackground(t *testing.T, broker corequeue.Broker, cfg svcqueue.ConsumerConfig) {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	var running sync.WaitGroup
	running.Go(func() {
		if err := svcqueue.Consume(ctx, broker, cfg); err != nil {
			t.Errorf("Consume() = %v, want nil", err)
		}
	})
	t.Cleanup(func() {
		cancel()
		running.Wait()
	})
}

// await fails the test when ch does not deliver within the engine's budget.
// The budget is a guard against a hang, never a wait the test relies on.
func await[T any](t *testing.T, ch <-chan T, what string) T {
	t.Helper()
	select {
	case value := <-ch:
		return value
	case <-time.After(engineBudget):
		t.Fatalf("%s never happened within %v", what, engineBudget)
		var zero T
		return zero
	}
}

// report hands a handler's delivery count to the test, or gives up when the
// consumer stops: a delivery the test no longer reads must not hold the
// handler, and with it the cleanup that waits for every worker.
func report(ctx context.Context, attempts chan<- int, deliveries int) {
	select {
	case attempts <- deliveries:
	case <-ctx.Done():
	}
}

// TestAHandlerThatSaysWhenToRetryIsNackedWithItsDelay is RetryAfter through
// Consume on every broker (ADR 0183): the message is handed back through the
// broker's DelayedNacker, receivable at the delay the handler named — not at
// the policy's RetryDelay — and its next delivery is counted.
func TestAHandlerThatSaysWhenToRetryIsNackedWithItsDelay(t *testing.T) {
	t.Parallel()
	for _, factory := range everyBroker() {
		t.Run(factory.name, func(t *testing.T) {
			t.Parallel()
			clk := clock.NewManualClock(epoch)
			broker := factory.make(t, clk, defaultPolicy())
			publish(t, broker, "work")
			handedBack := broker.(corequeue.Waker).Wake().Signal
			attempts := make(chan int, 2)
			consumeInBackground(t, broker, svcqueue.ConsumerConfig{
				Handler: func(ctx context.Context, delivery corequeue.DeliveryValue) error {
					report(ctx, attempts, delivery.Deliveries)
					if delivery.Deliveries == 1 {
						return corequeue.RetryAfter(errThrottled, retryAsked)
					}
					return nil
				},
				Clock: clk, PollInterval: time.Hour, HandlerIsIdempotent: true,
			})
			if first := await(t, attempts, "the first attempt"); first != 1 {
				t.Fatalf("the first attempt was delivery %d, want 1", first)
			}
			await(t, handedBack, "the hand-back")
			live, err := messageReader(t, broker).Messages(t.Context(), "", 10)
			if err != nil || len(live) != 1 || live[0].Leased || !live[0].VisibleAt.Equal(epoch.Add(retryAsked)) || live[0].Deliveries != 1 {
				t.Fatalf("after the handler's RetryAfter, Messages() = (%+v, %v), want one waiting until %v", live, err, epoch.Add(retryAsked))
			}
			//: the worker found nothing receivable and sleeps until the delay
			//: ends: advancing before its timer is armed would arm it a whole
			//: delay after the advance, and the retry would never come.
			clk.BlockUntil(1)
			clk.Advance(retryAsked)
			if second := await(t, attempts, "the retry"); second != 2 {
				t.Fatalf("the retry was delivery %d, want 2", second)
			}
		})
	}
}

// policyOnly is a broker with the four verbs of Broker and no sibling at all:
// a connector whose redelivery delay is fixed.
type policyOnly struct {
	corequeue.Broker
}

// TestARetryAfterOnABrokerWithoutTheSiblingIsAPlainNack pins the fallback: a
// broker that cannot delay a nack is nacked, the message waits the policy's
// RetryDelay, and nothing is lost but the delay asked for.
func TestARetryAfterOnABrokerWithoutTheSiblingIsAPlainNack(t *testing.T) {
	t.Parallel()
	clk := clock.NewManualClock(epoch)
	inner := makeMemory(t, clk, defaultPolicy())
	publish(t, inner, "work")
	handedBack := inner.(corequeue.Waker).Wake().Signal
	attempts := make(chan int, 2)
	consumeInBackground(t, policyOnly{Broker: inner}, svcqueue.ConsumerConfig{
		Handler: func(ctx context.Context, delivery corequeue.DeliveryValue) error {
			report(ctx, attempts, delivery.Deliveries)
			return corequeue.RetryAfter(errThrottled, retryAsked)
		},
		Clock: clk, PollInterval: time.Hour, HandlerIsIdempotent: true,
	})
	await(t, attempts, "the first attempt")
	await(t, handedBack, "the nack")
	live, err := messageReader(t, inner).Messages(t.Context(), "", 10)
	if err != nil || len(live) != 1 || !live[0].VisibleAt.Equal(epoch.Add(testRetryDelay)) {
		t.Fatalf("Messages() = (%+v, %v), want one waiting the policy's %v", live, err, testRetryDelay)
	}
}

// TestDoNotRetryWinsOverRetryAfter pins the precedence: a failure marked both
// ways is dead-lettered at once, its delay unused — a message no retry can fix
// is not retried later either.
func TestDoNotRetryWinsOverRetryAfter(t *testing.T) {
	t.Parallel()
	clk := clock.NewManualClock(epoch)
	broker := makeMemory(t, clk, defaultPolicy())
	publish(t, broker, "work")
	attempts := make(chan int, 2)
	observed := outcomeSpy{Broker: broker, outcomes: make(chan string, 2)}
	consumeInBackground(t, observed, svcqueue.ConsumerConfig{
		Handler: func(ctx context.Context, delivery corequeue.DeliveryValue) error {
			report(ctx, attempts, delivery.Deliveries)
			return corequeue.DoNotRetry(corequeue.RetryAfter(errThrottled, retryAsked))
		},
		Clock: clk, PollInterval: time.Hour, HandlerIsIdempotent: true,
	})
	await(t, attempts, "the first attempt")
	if outcome := await(t, observed.outcomes, "the outcome"); outcome != "reject" {
		t.Fatalf("the engine reported %s, want reject", outcome)
	}
	if dead := deadLetters(t, broker, 10); len(dead) != 1 || dead[0].Deliveries != 1 {
		t.Fatalf("dead letters = %+v, want the message rejected on its first delivery", dead)
	}
}

// outcomeSpy is a broker that reports which of its two failure siblings the
// engine reached, after the broker underneath did what it was asked.
type outcomeSpy struct {
	corequeue.Broker
	outcomes chan string
}

// Reject delegates, then reports.
func (s outcomeSpy) Reject(ctx context.Context, receipt corequeue.ReceiptValue, cause error) error {
	err := s.Broker.(corequeue.Rejecter).Reject(ctx, receipt, cause)
	s.outcomes <- "reject"
	return err
}

// NackAfter delegates, then reports.
func (s outcomeSpy) NackAfter(ctx context.Context, receipt corequeue.ReceiptValue, cause error, delay time.Duration) (corequeue.NackValue, error) {
	verdict, err := s.Broker.(corequeue.DelayedNacker).NackAfter(ctx, receipt, cause, delay)
	s.outcomes <- "nackAfter"
	return verdict, err
}
