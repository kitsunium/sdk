package queue_test

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kitsunium/sdk/internal/kernel/clock"
	"github.com/kitsunium/sdk/internal/kernel/errs"

	corequeue "github.com/kitsunium/sdk/internal/core/data/queue"
	svcqueue "github.com/kitsunium/sdk/internal/service/data/queue"
)

// testMargin is how long before its lease ends a handler is stopped under
// defaultPolicy: a tenth of the lease, the SettleTimeout default being longer.
const testMargin time.Duration = testVisibility / 10

// consuming runs Consume on its own goroutine and returns the function that
// waits for it to return and reports what it returned.
//
// GOROUTINE LIFECYCLE: one goroutine runs Consume; the caller ends it by
// cancelling ctx (or the handler does) and joins it through the returned
// function, which every caller calls before the test returns.
func consuming(
	ctx context.Context, broker corequeue.Broker, cfg svcqueue.ConsumerConfig,
) (wait func() error) {
	var running sync.WaitGroup
	var result error
	running.Go(func() { result = svcqueue.Consume(ctx, broker, cfg) })
	return func() error {
		running.Wait()
		return result
	}
}

// publishInOrder publishes each payload one nanosecond after the previous one,
// so every broker hands them over in that order.
func publishInOrder(t *testing.T, clk *clock.ManualClock, broker corequeue.Broker, payloads ...string) {
	t.Helper()
	for _, payload := range payloads {
		clk.Advance(time.Nanosecond)
		publish(t, broker, payload)
	}
}

// TestAStopInTheMiddleOfABatchHandsTheUnstartedMessagesBackUncounted is the
// first half of kitsunium/sdk#474: a consumer stopped while its batch was
// half done used to hand the rest of the batch to the handler under a dead
// context, and the nack of each was refused — they stayed leased for a whole
// visibility timeout and came back with a delivery burnt, so every deployment
// moved a message closer to the dead-letter store without anybody having
// tried it. Now a message the handler never started is handed back at once,
// its count untouched: receivable the instant Consume returns, as its first
// delivery.
func TestAStopInTheMiddleOfABatchHandsTheUnstartedMessagesBackUncounted(t *testing.T) {
	t.Parallel()
	for _, factory := range everyBroker() {
		t.Run(factory.name, func(t *testing.T) {
			t.Parallel()
			clk := clock.NewManualClock(epoch)
			broker := factory.make(t, clk, defaultPolicy())
			publishInOrder(t, clk, broker, "first", "second", "third")
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			var started atomic.Int64
			wait := consuming(ctx, broker, svcqueue.ConsumerConfig{
				Handler: func(handlerCtx context.Context, _ corequeue.DeliveryValue) error {
					//: a handler that honours its context does no work once stopped.
					if handlerCtx.Err() != nil {
						return handlerCtx.Err()
					}
					started.Add(1)
					//: the stop lands while the batch's first message is in hand.
					cancel()
					return nil
				},
				HandlerIsIdempotent: true, PollInterval: fastPoll, Clock: clk, BatchSize: 3,
			})
			if err := wait(); err != nil {
				t.Fatalf("Consume() = %v, want nil for a stopped consumer", err)
			}
			if got := started.Load(); got != 1 {
				t.Fatalf("the handler started %d messages, want 1 — the stop came during the first", got)
			}
			//: no clock movement: they are receivable now, not after a lease.
			batch := receive(t, broker, 10)
			if len(batch) != 2 {
				t.Fatalf("after the stop, Receive(10) = %d deliveries, want the 2 never started", len(batch))
			}
			for _, delivery := range batch {
				if delivery.Deliveries != 1 {
					t.Errorf("%q came back as delivery %d, want 1 — a message nobody tried keeps its count",
						delivery.Message.Payload, delivery.Deliveries)
				}
			}
		})
	}
}

// TestAMessageWhoseLeaseRanOutInItsBatchIsNotHandedToTheHandler is the second
// half: a batch is leased under one deadline and handled one message after
// another, so a slow first message used to leave the second a lease that had
// all but lapsed — and the handler ran it anyway, while the broker was about
// to give it to somebody else. Now a message whose lease is within the margin
// is never started: it is handed back uncounted and received again with a
// fresh lease, and the handler only ever sees a lease it can finish under.
func TestAMessageWhoseLeaseRanOutInItsBatchIsNotHandedToTheHandler(t *testing.T) {
	t.Parallel()
	for _, factory := range everyBroker() {
		t.Run(factory.name, func(t *testing.T) {
			t.Parallel()
			clk := clock.NewManualClock(epoch)
			broker := factory.make(t, clk, defaultPolicy())
			publishInOrder(t, clk, broker, "slow", "next")
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			type seen struct {
				payload    string
				left       time.Duration
				deliveries int
			}
			var mu sync.Mutex
			var handled []seen
			wait := consuming(ctx, broker, svcqueue.ConsumerConfig{
				Handler: func(_ context.Context, delivery corequeue.DeliveryValue) error {
					mu.Lock()
					handled = append(handled, seen{
						payload: string(delivery.Message.Payload), deliveries: delivery.Deliveries,
						left: delivery.Lease.ExpiresAt.Sub(clk.Now()),
					})
					mu.Unlock()
					switch string(delivery.Message.Payload) {
					case "slow":
						//: the first message takes all but a nanosecond of the batch's lease.
						clk.Advance(testVisibility - time.Nanosecond)
					case "next":
						cancel()
					}
					return nil
				},
				HandlerIsIdempotent: true, PollInterval: fastPoll, Clock: clk, BatchSize: 2,
			})
			if err := wait(); err != nil {
				t.Fatalf("Consume() = %v, want nil", err)
			}
			mu.Lock()
			defer mu.Unlock()
			if len(handled) != 2 {
				t.Fatalf("the handler ran %+v, want slow then next once each", handled)
			}
			for _, call := range handled {
				if call.left <= testMargin {
					t.Errorf("%q was handed over with %v of its lease left, want more than the %v margin",
						call.payload, call.left, testMargin)
				}
				if call.deliveries != 1 {
					t.Errorf("%q was handed over as delivery %d, want 1 — a message never started keeps its count",
						call.payload, call.deliveries)
				}
			}
		})
	}
}

// TestAHandlerIsStoppedBeforeItsLeaseEnds pins the bound itself: the handler's
// context is cancelled a margin before the broker would hand its message to
// somebody else, with LEASE_EXPIRED as its cause — before, it ran on, and a
// second consumer could be running the same message beside it.
func TestAHandlerIsStoppedBeforeItsLeaseEnds(t *testing.T) {
	t.Parallel()
	for _, factory := range everyBroker() {
		t.Run(factory.name, func(t *testing.T) {
			t.Parallel()
			clk := clock.NewManualClock(epoch)
			broker := factory.make(t, clk, defaultPolicy())
			publish(t, broker, "long")
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			inside := make(chan struct{})
			cause := make(chan error, 1)
			wait := consuming(ctx, broker, svcqueue.ConsumerConfig{
				Handler: func(handlerCtx context.Context, _ corequeue.DeliveryValue) error {
					close(inside)
					select {
					case <-handlerCtx.Done():
						cause <- context.Cause(handlerCtx)
					case <-time.After(engineBudget):
						cause <- nil
					}
					cancel()
					return handlerCtx.Err()
				},
				HandlerIsIdempotent: true, PollInterval: fastPoll, Clock: clk,
			})
			<-inside
			//: one nanosecond short of the margin, the handler runs on.
			clk.Advance(testVisibility - testMargin - time.Nanosecond)
			select {
			case got := <-cause:
				t.Fatalf("the handler was stopped %v before its lease's margin, cause %v", time.Nanosecond, got)
			default:
			}
			clk.Advance(time.Nanosecond)
			got := <-cause
			if err := wait(); err != nil {
				t.Fatalf("Consume() = %v, want nil", err)
			}
			if !errs.HasCode(got, corequeue.CodeLeaseExpired) {
				t.Fatalf("the handler's context cause = %v, want LEASE_EXPIRED a margin before the lease ends", got)
			}
		})
	}
}
