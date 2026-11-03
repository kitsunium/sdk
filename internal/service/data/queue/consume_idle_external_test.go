// Package queue_test — the idle workers of one Consume poll through one of
// them (issue #1049).
package queue_test

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kitsunium/sdk/internal/kernel/clock"

	corequeue "github.com/kitsunium/sdk/internal/core/data/queue"
	svcqueue "github.com/kitsunium/sdk/internal/service/data/queue"
)

// countingBroker counts the Receives a consumer sends a broker, and wakes as
// the broker does.
type countingBroker struct {
	corequeue.Broker
	receives atomic.Int64
}

// Receive counts, then asks the broker.
func (b *countingBroker) Receive(ctx context.Context, max int) ([]corequeue.DeliveryValue, error) {
	b.receives.Add(1)
	return b.Broker.Receive(ctx, max)
}

// Wake is the broker's.
func (b *countingBroker) Wake() corequeue.WakeValue {
	return b.Broker.(corequeue.Waker).Wake()
}

// waitFor polls cond on the real clock until it holds, or fails the test.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(engineBudget)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("%s did not happen within %v", what, engineBudget)
		}
		time.Sleep(fastPoll)
	}
}

// TestIdleWorkersPollOncePerInterval pins the gate on every broker: eight
// idle workers of one Consume ask the broker once per poll interval, not
// eight times — after the one look each takes when it starts.
//
// Seen failing before the change: every interval sent eight Receives.
func TestIdleWorkersPollOncePerInterval(t *testing.T) {
	t.Parallel()
	const poll = 100 * time.Millisecond
	for _, factory := range everyBroker() {
		t.Run(factory.name, func(t *testing.T) {
			t.Parallel()
			clk := clock.NewManualClock(epoch)
			broker := &countingBroker{Broker: factory.make(t, clk, defaultPolicy())}
			ctx, cancel := context.WithCancel(t.Context())
			wait := consuming(ctx, broker, svcqueue.ConsumerConfig{
				Handler: accept, HandlerIsIdempotent: true, PollInterval: poll, Clock: clk, Parallelism: 8,
			})
			defer func() {
				cancel()
				if err := wait(); err != nil {
					t.Errorf("Consume() = %v", err)
				}
			}()
			waitFor(t, "eight first looks", func() bool { return broker.receives.Load() == 8 })
			clk.BlockUntil(1)
			for interval := 1; interval <= 3; interval++ {
				before := broker.receives.Load()
				clk.Advance(poll)
				waitFor(t, "the poll", func() bool { return broker.receives.Load() > before })
				clk.BlockUntil(1)
				if got := broker.receives.Load() - before; got != 1 {
					t.Fatalf("interval %d sent %d Receives, want 1 for eight idle workers", interval, got)
				}
			}
		})
	}
}

// TestSQLIdleWorkersProbeOncePerInterval pins the gate in the statement log
// of the fake engine: eight idle workers, one probe per interval.
func TestSQLIdleWorkersProbeOncePerInterval(t *testing.T) {
	t.Parallel()
	const poll = 100 * time.Millisecond
	eachDialect(t, func(t *testing.T, fx *sqlFixture, clk *clock.ManualClock) {
		ctx, cancel := context.WithCancel(t.Context())
		wait := consuming(ctx, fx.broker, svcqueue.ConsumerConfig{
			Handler: accept, HandlerIsIdempotent: true, PollInterval: poll, Clock: clk, Parallelism: 8,
		})
		defer func() {
			cancel()
			if err := wait(); err != nil {
				t.Errorf("Consume() = %v", err)
			}
		}()
		waitFor(t, "eight first probes", func() bool { return countRole(fx.engine.roles(), "probe") == 8 })
		clk.BlockUntil(1)
		fx.engine.resetLog()
		clk.Advance(poll)
		waitFor(t, "the poll's probe", func() bool { return countRole(fx.engine.roles(), "probe") >= 1 })
		clk.BlockUntil(1)
		if got := countRole(fx.engine.roles(), "probe"); got != 1 {
			t.Fatalf("an idle interval of eight workers sent %d probes, want 1", got)
		}
	})
}

// TestFourMessagesPublishedAtOnceReachFourIdleWorkersAtOnce pins that the
// gate costs no latency on every broker: four idle workers, four messages
// published at once, and the four handlers run together with the clock never
// moved — the polling worker is woken by the publication and wakes the rest.
func TestFourMessagesPublishedAtOnceReachFourIdleWorkersAtOnce(t *testing.T) {
	t.Parallel()
	for _, factory := range everyBroker() {
		t.Run(factory.name, func(t *testing.T) {
			t.Parallel()
			clk := clock.NewManualClock(epoch)
			broker := factory.make(t, clk, defaultPolicy())
			var running sync.WaitGroup
			running.Add(4)
			together := make(chan struct{})
			go func() {
				running.Wait()
				close(together)
			}()
			var handled atomic.Int64
			ctx, cancel := context.WithCancel(t.Context())
			wait := consuming(ctx, broker, svcqueue.ConsumerConfig{
				Handler: func(handlerCtx context.Context, _ corequeue.DeliveryValue) error {
					running.Done()
					select {
					case <-together:
					case <-handlerCtx.Done():
					}
					handled.Add(1)
					return nil
				},
				HandlerIsIdempotent: true, PollInterval: idlePoll, Clock: clk, Parallelism: 4,
			})
			clk.BlockUntil(1)
			publisher := broker.(corequeue.BatchPublisher)
			if _, err := publisher.PublishMany(t.Context(), [][]byte{[]byte("a"), []byte("b"), []byte("c"), []byte("d")}); err != nil {
				t.Fatalf("PublishMany() = %v", err)
			}
			select {
			case <-together:
			case <-time.After(engineBudget):
				t.Fatalf("four messages published at once did not reach four workers at once within %v", engineBudget)
			}
			waitFor(t, "four handled", func() bool { return handled.Load() == 4 })
			cancel()
			if err := wait(); err != nil {
				t.Fatalf("Consume() = %v", err)
			}
			if !clk.Now().Equal(epoch) {
				t.Fatalf("the clock moved to %v; the deliveries must not have needed it", clk.Now())
			}
		})
	}
}
