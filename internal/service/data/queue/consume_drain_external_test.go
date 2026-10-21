package queue_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/kitsunium/sdk/internal/kernel/clock"
	"github.com/kitsunium/sdk/internal/kernel/errs"

	corequeue "github.com/kitsunium/sdk/internal/core/data/queue"
	svcqueue "github.com/kitsunium/sdk/internal/service/data/queue"
)

// testDrain is the DrainTimeout these cases give the message in hand, well
// inside its lease's margin.
const testDrain time.Duration = 3 * time.Second

// testRenewEvery is AutoExtend's cadence under defaultPolicy: a third of the
// lease.
const testRenewEvery time.Duration = testVisibility / 3

// blockingHandler signals inside once it runs, then returns nil when finish
// closes or its context's error when that ends first, reporting on ended the
// context's cause at that moment — nil when finish came first.
func blockingHandler(inside chan<- struct{}, finish <-chan struct{}, ended chan<- error) corequeue.Handler {
	return func(ctx context.Context, _ corequeue.DeliveryValue) error {
		inside <- struct{}{}
		select {
		case <-finish:
			ended <- context.Cause(ctx)
			return nil
		case <-ctx.Done():
			ended <- context.Cause(ctx)
			return ctx.Err()
		}
	}
}

// TestADrainLetsTheMessageInHandFinish pins DrainTimeout's first half: the
// stop does not reach a handler that finishes inside the drain, and what it
// finished is acknowledged — nothing comes back.
func TestADrainLetsTheMessageInHandFinish(t *testing.T) {
	t.Parallel()
	for _, factory := range everyBroker() {
		t.Run(factory.name, func(t *testing.T) {
			t.Parallel()
			clk := clock.NewManualClock(epoch)
			broker := factory.make(t, clk, defaultPolicy())
			publish(t, broker, "in hand")
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			inside, finish, ended := make(chan struct{}, 1), make(chan struct{}), make(chan error, 1)
			wait := consuming(ctx, broker, svcqueue.ConsumerConfig{
				Handler: blockingHandler(inside, finish, ended), DrainTimeout: testDrain,
				HandlerIsIdempotent: true, PollInterval: fastPoll, Clock: clk,
			})
			<-inside
			cancel()
			//: the lease's margin, and now the drain.
			clk.BlockUntil(2)
			clk.Advance(testDrain - time.Nanosecond)
			close(finish)
			if cause := <-ended; cause != nil {
				t.Fatalf("the handler's context ended inside the drain, cause %v", cause)
			}
			if err := wait(); err != nil {
				t.Fatalf("Consume() = %v, want nil", err)
			}
			clk.Advance(testVisibility * 2)
			receiveNone(t, broker)
		})
	}
}

// TestADrainThatRunsOutHandsTheMessageBackUncounted pins its second half: once
// the drain is over the handler's context is cancelled with the stop as its
// cause, and the message it did not finish is receivable at once, as the
// delivery it was — not leased for a visibility timeout, not counted.
func TestADrainThatRunsOutHandsTheMessageBackUncounted(t *testing.T) {
	t.Parallel()
	for _, factory := range everyBroker() {
		t.Run(factory.name, func(t *testing.T) {
			t.Parallel()
			clk := clock.NewManualClock(epoch)
			broker := factory.make(t, clk, defaultPolicy())
			publish(t, broker, "cut short")
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			inside, ended := make(chan struct{}, 1), make(chan error, 1)
			wait := consuming(ctx, broker, svcqueue.ConsumerConfig{
				Handler: blockingHandler(inside, nil, ended), DrainTimeout: testDrain,
				HandlerIsIdempotent: true, PollInterval: fastPoll, Clock: clk,
			})
			<-inside
			cancel()
			clk.BlockUntil(2)
			clk.Advance(testDrain)
			if cause := <-ended; !errors.Is(cause, context.Canceled) {
				t.Fatalf("the handler's context cause = %v, want the consumer's stop", cause)
			}
			if err := wait(); err != nil {
				t.Fatalf("Consume() = %v, want nil", err)
			}
			if again := receiveOne(t, broker); again.Deliveries != 1 {
				t.Fatalf("the message cut short came back as delivery %d, want 1", again.Deliveries)
			}
		})
	}
}

// TestALeaseThatEndsDuringADrainStillHandsTheMessageBackUncounted pins the
// drain against the lease bound: a drain longer than what the lease has left
// is cut by the lease's margin, and the message the stop interrupted is still
// the stop's — released, uncounted, not nacked toward the dead-letter store.
func TestALeaseThatEndsDuringADrainStillHandsTheMessageBackUncounted(t *testing.T) {
	t.Parallel()
	for _, factory := range everyBroker() {
		t.Run(factory.name, func(t *testing.T) {
			t.Parallel()
			clk := clock.NewManualClock(epoch)
			broker := factory.make(t, clk, defaultPolicy())
			publish(t, broker, "outlasted")
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			inside, ended := make(chan struct{}, 1), make(chan error, 1)
			wait := consuming(ctx, broker, svcqueue.ConsumerConfig{
				Handler: blockingHandler(inside, nil, ended), DrainTimeout: 2 * testVisibility,
				HandlerIsIdempotent: true, PollInterval: fastPoll, Clock: clk,
			})
			<-inside
			cancel()
			clk.BlockUntil(2)
			//: the lease's margin comes long before the drain ends.
			clk.Advance(testVisibility - testMargin)
			if cause := <-ended; !errs.HasCode(cause, corequeue.CodeLeaseExpired) {
				t.Fatalf("the handler's context cause = %v, want the lease's LEASE_EXPIRED", cause)
			}
			if err := wait(); err != nil {
				t.Fatalf("Consume() = %v, want nil", err)
			}
			if again := receiveOne(t, broker); again.Deliveries != 1 {
				t.Fatalf("the message the stop interrupted came back as delivery %d, want 1", again.Deliveries)
			}
		})
	}
}

// TestAStopWithNoDrainHandsTheInterruptedMessageBack is the zero DrainTimeout,
// which is what a stop always was — the handler's context ends at once — and
// the defect beside it: the nack of the interrupted message was sent on the
// dead context, every durable broker refused it, and the message stayed
// leased. It is now handed back, receivable at once, uncounted.
func TestAStopWithNoDrainHandsTheInterruptedMessageBack(t *testing.T) {
	t.Parallel()
	for _, factory := range everyBroker() {
		t.Run(factory.name, func(t *testing.T) {
			t.Parallel()
			clk := clock.NewManualClock(epoch)
			broker := factory.make(t, clk, defaultPolicy())
			publish(t, broker, "interrupted")
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			inside, ended := make(chan struct{}, 1), make(chan error, 1)
			wait := consuming(ctx, broker, svcqueue.ConsumerConfig{
				Handler:             blockingHandler(inside, nil, ended),
				HandlerIsIdempotent: true, PollInterval: fastPoll, Clock: clk,
			})
			<-inside
			cancel()
			if cause := <-ended; !errors.Is(cause, context.Canceled) {
				t.Fatalf("the handler's context cause = %v, want the consumer's stop", cause)
			}
			if err := wait(); err != nil {
				t.Fatalf("Consume() = %v, want nil", err)
			}
			if again := receiveOne(t, broker); again.Deliveries != 1 {
				t.Fatalf("the interrupted message came back as delivery %d, want 1", again.Deliveries)
			}
		})
	}
}

// withoutRelease is a broker of the caller's own making that has no Releaser:
// only the four methods of the port.
type withoutRelease struct {
	corequeue.Broker
}

// TestAStopFallsBackToANackWithoutReleaser pins the fallback: over a broker
// with no Releaser, the batch a stop interrupts is nacked — retried after its
// delay with the delivery counted, so the courtesy is lost and never the
// message — rather than left leased for a visibility timeout.
func TestAStopFallsBackToANackWithoutReleaser(t *testing.T) {
	t.Parallel()
	for _, factory := range everyBroker() {
		t.Run(factory.name, func(t *testing.T) {
			t.Parallel()
			clk := clock.NewManualClock(epoch)
			inner := factory.make(t, clk, defaultPolicy())
			broker := withoutRelease{Broker: inner}
			publishInOrder(t, clk, inner, "first", "second")
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			wait := consuming(ctx, broker, svcqueue.ConsumerConfig{
				Handler: func(context.Context, corequeue.DeliveryValue) error {
					cancel()
					return nil
				},
				HandlerIsIdempotent: true, PollInterval: fastPoll, Clock: clk, BatchSize: 2,
			})
			if err := wait(); err != nil {
				t.Fatalf("Consume() = %v, want nil", err)
			}
			receiveNone(t, inner)
			clk.Advance(testRetryDelay)
			again := receiveOne(t, inner)
			if string(again.Message.Payload) != "second" || again.Deliveries != 2 {
				t.Fatalf("after the retry delay, got %q as delivery %d, want second as delivery 2",
					again.Message.Payload, again.Deliveries)
			}
		})
	}
}

// hangingRelease is a broker whose Release does not answer until its context
// ends — a medium that stopped answering — and says so on entered.
type hangingRelease struct {
	corequeue.Broker
	entered chan<- struct{}
	cause   chan<- error
}

// Release hangs until ctx ends, and reports why it ended.
func (h hangingRelease) Release(ctx context.Context, _ corequeue.ReceiptValue) error {
	h.entered <- struct{}{}
	<-ctx.Done()
	h.cause <- context.Cause(ctx)
	return ctx.Err()
}

// TestAnOutcomeSentPastTheStopIsBoundedBySettleTimeout pins SettleTimeout: a
// hand-back sent once the consumer is stopping runs on a context the stop does
// not cancel, so a medium that stopped answering would hold the stop forever.
// The bound ends it at SettleTimeout on the consumer's clock, with
// QUEUE_BACKEND_FAILED as the cause, and Consume returns nil: it was stopping.
func TestAnOutcomeSentPastTheStopIsBoundedBySettleTimeout(t *testing.T) {
	t.Parallel()
	const settle = 2 * time.Second
	clk := clock.NewManualClock(epoch)
	inner := makeMemory(t, clk, defaultPolicy())
	entered, cause := make(chan struct{}, 1), make(chan error, 1)
	broker := hangingRelease{Broker: inner, entered: entered, cause: cause}
	publishInOrder(t, clk, inner, "first", "second")
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	wait := consuming(ctx, broker, svcqueue.ConsumerConfig{
		Handler: func(context.Context, corequeue.DeliveryValue) error {
			cancel()
			return nil
		},
		HandlerIsIdempotent: true, PollInterval: fastPoll, Clock: clk, BatchSize: 2, SettleTimeout: settle,
	})
	<-entered
	//: the bound, armed before the hand-back was sent.
	clk.BlockUntil(1)
	clk.Advance(settle - time.Nanosecond)
	select {
	case got := <-cause:
		t.Fatalf("the hand-back was cut short before SettleTimeout, cause %v", got)
	default:
	}
	clk.Advance(time.Nanosecond)
	if got := <-cause; !errs.HasCode(got, corequeue.CodeQueueBackendFailed) {
		t.Fatalf("the hand-back's context cause = %v, want QUEUE_BACKEND_FAILED", got)
	}
	if err := wait(); err != nil {
		t.Fatalf("Consume() = %v, want nil — it was stopping", err)
	}
}

// renewSpy forwards to a broker that renews and releases, and reports each
// renewal it made.
type renewSpy struct {
	corequeue.Broker
	renewed chan corequeue.LeaseValue
}

// Extend renews through the broker and reports the new lease.
func (s renewSpy) Extend(ctx context.Context, receipt corequeue.ReceiptValue, by time.Duration) (corequeue.LeaseValue, error) {
	lease, err := s.Broker.(corequeue.LeaseExtender).Extend(ctx, receipt, by)
	if err == nil {
		s.renewed <- lease
	}
	return lease, err
}

// Release is the broker's.
func (s renewSpy) Release(ctx context.Context, receipt corequeue.ReceiptValue) error {
	return s.Broker.(corequeue.Releaser).Release(ctx, receipt)
}

// TestAutoExtendRenewsTheLeaseWhileTheHandlerRuns pins AutoExtend: every third
// of the lease the engine renews it, by the lease's own length, so a handler
// that runs past the lease it was granted is neither stopped nor duplicated,
// and its work is acknowledged on the receipt of the last renewal.
func TestAutoExtendRenewsTheLeaseWhileTheHandlerRuns(t *testing.T) {
	t.Parallel()
	for _, factory := range everyBroker() {
		t.Run(factory.name, func(t *testing.T) {
			t.Parallel()
			clk := clock.NewManualClock(epoch)
			inner := factory.make(t, clk, defaultPolicy())
			broker := renewSpy{Broker: inner, renewed: make(chan corequeue.LeaseValue, 4)}
			publish(t, inner, "long")
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			inside, finish, ended := make(chan struct{}, 1), make(chan struct{}), make(chan error, 1)
			wait := consuming(ctx, broker, svcqueue.ConsumerConfig{
				Handler: blockingHandler(inside, finish, ended), AutoExtend: true,
				HandlerIsIdempotent: true, PollInterval: fastPoll, Clock: clk,
			})
			<-inside
			//: four renewals take the handler past the lease it was granted;
			//: each is seen before the next third is let elapse, which also
			//: means the margin moved with the one before it.
			for round := range 4 {
				clk.Advance(testRenewEvery)
				lease := <-broker.renewed
				if left := lease.ExpiresAt.Sub(clk.Now()); left != testVisibility {
					t.Fatalf("renewal %d left %v of lease, want the %v the broker granted", round, left, testVisibility)
				}
			}
			if now := clk.Now().Sub(epoch); now < testVisibility {
				t.Fatalf("the clock is at %v, want past the first lease's %v", now, testVisibility)
			}
			close(finish)
			if cause := <-ended; cause != nil {
				t.Fatalf("a renewed handler's context ended, cause %v", cause)
			}
			cancel()
			if err := wait(); err != nil {
				t.Fatalf("Consume() = %v, want nil", err)
			}
			//: acknowledged on the last receipt: nothing comes back.
			clk.Advance(testVisibility * 2)
			receiveNone(t, inner)
		})
	}
}

// TestAutoExtendRenewsABatchMessageBeforeItStarts pins the batch half: a
// message whose lease is a third spent when its turn comes is renewed before
// its handler starts, which then sees the whole lease the broker grants.
func TestAutoExtendRenewsABatchMessageBeforeItStarts(t *testing.T) {
	t.Parallel()
	for _, factory := range everyBroker() {
		t.Run(factory.name, func(t *testing.T) {
			t.Parallel()
			clk := clock.NewManualClock(epoch)
			broker := factory.make(t, clk, defaultPolicy())
			publishInOrder(t, clk, broker, "slow", "next")
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			leftForNext := make(chan time.Duration, 1)
			wait := consuming(ctx, broker, svcqueue.ConsumerConfig{
				Handler: func(_ context.Context, delivery corequeue.DeliveryValue) error {
					switch string(delivery.Message.Payload) {
					case "slow":
						//: half the batch's lease, on the first message.
						clk.Advance(testVisibility / 2)
					default:
						leftForNext <- delivery.Lease.ExpiresAt.Sub(clk.Now())
						cancel()
					}
					return nil
				},
				AutoExtend: true, HandlerIsIdempotent: true, PollInterval: fastPoll, Clock: clk, BatchSize: 2,
			})
			if err := wait(); err != nil {
				t.Fatalf("Consume() = %v, want nil", err)
			}
			if left := <-leftForNext; left != testVisibility {
				t.Fatalf("the second message started with %v of lease, want the renewed %v", left, testVisibility)
			}
			clk.Advance(testVisibility * 2)
			receiveNone(t, broker)
		})
	}
}

// refusedRenewal is a broker whose every renewal is refused: the lease lapsed.
type refusedRenewal struct {
	corequeue.Broker
}

// Extend refuses, as a broker does for a lease that lapsed.
func (refusedRenewal) Extend(context.Context, corequeue.ReceiptValue, time.Duration) (corequeue.LeaseValue, error) {
	return corequeue.LeaseValue{}, errs.Wrap(corequeue.LeaseExpired, errs.WrapParams{}, errs.String("broker", "test"))
}

// TestARefusedRenewalStopsTheHandler pins what AutoExtend does when the broker
// will not renew: the handler's work is becoming a duplicate, and it hears so
// through its context, with the refusal as the cause.
func TestARefusedRenewalStopsTheHandler(t *testing.T) {
	t.Parallel()
	clk := clock.NewManualClock(epoch)
	broker := refusedRenewal{Broker: makeMemory(t, clk, defaultPolicy())}
	publish(t, broker, "lost")
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	inside, ended := make(chan struct{}, 1), make(chan error, 1)
	wait := consuming(ctx, broker, svcqueue.ConsumerConfig{
		Handler: blockingHandler(inside, nil, ended), AutoExtend: true,
		HandlerIsIdempotent: true, PollInterval: fastPoll, Clock: clk,
	})
	<-inside
	clk.Advance(testRenewEvery)
	if cause := <-ended; !errs.HasCode(cause, corequeue.CodeLeaseExpired) {
		t.Fatalf("the handler's context cause = %v, want the refusal's LEASE_EXPIRED", cause)
	}
	cancel()
	if err := wait(); err != nil {
		t.Fatalf("Consume() = %v, want nil", err)
	}
}
