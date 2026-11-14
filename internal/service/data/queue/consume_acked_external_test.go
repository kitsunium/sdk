// Package queue_test — a handler that acknowledges its delivery itself, in its
// own transaction, and the engine's bounded memory of what it handled (issue
// #1046).
package queue_test

import (
	"context"
	"errors"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kitsunium/sdk/internal/kernel/clock"

	corequeue "github.com/kitsunium/sdk/internal/core/data/queue"
	svcqueue "github.com/kitsunium/sdk/internal/service/data/queue"
)

// errRolledBack is what a handler's transaction returns to roll back.
var errRolledBack = errors.New("the handler's transaction rolled back")

// TestConsumeAcknowledgedIsNeverNacked pins Acknowledged on every broker: a
// handler that acknowledged its delivery itself and says so is neither
// acknowledged again nor handed back; one that only says so leaves its
// message leased — never nacked, never dead-lettered — for its lease to
// lapse.
func TestConsumeAcknowledgedIsNeverNacked(t *testing.T) {
	t.Parallel()
	for _, factory := range everyBroker() {
		t.Run(factory.name, func(t *testing.T) {
			t.Parallel()
			clk := clock.NewManualClock(epoch)
			broker := factory.make(t, clk, defaultPolicy())
			publishInOrder(t, clk, broker, "acked", "claimed", "plain")
			var handled atomic.Int64
			runEngine(t, broker, svcqueue.ConsumerConfig{
				Handler: func(ctx context.Context, delivery corequeue.DeliveryValue) error {
					handled.Add(1)
					switch string(delivery.Message.Payload) {
					case "acked":
						if err := broker.Ack(ctx, delivery.Lease.Receipt); err != nil {
							return err
						}
						return corequeue.Acknowledged
					case "claimed":
						return corequeue.Acknowledged
					default:
						return nil
					}
				},
				HandlerIsIdempotent: true, PollInterval: fastPoll, Clock: clk,
			}, func() bool { return handled.Load() == 3 && statsOf(t, broker).Leased == 1 })
			stats := statsOf(t, broker)
			if stats.Queued != 0 || stats.Leased != 1 || stats.Dead != 0 {
				t.Fatalf("Stats() = %+v, want only the message the handler claimed and never acknowledged, leased", stats)
			}
			clk.Advance(testVisibility)
			if again := receiveOne(t, broker); string(again.Message.Payload) != "claimed" || again.Deliveries != 2 {
				t.Fatalf("after its lease, Receive() = %q delivery %d, want the claimed one's second", again.Message.Payload, again.Deliveries)
			}
		})
	}
}

// TestSQLAHandlerAcknowledgesInItsOwnTransaction pins the inbox half of the
// outbox (issue #1046): an acknowledgement inside the handler's transaction
// is the only one sent, and a rolled-back one leaves the message to come
// back as its second delivery.
func TestSQLAHandlerAcknowledgesInItsOwnTransaction(t *testing.T) {
	t.Parallel()
	eachDialect(t, func(t *testing.T, fx *sqlFixture, clk *clock.ManualClock) {
		publish(t, fx.broker, "committed")
		fx.engine.resetLog()
		var runs atomic.Int64
		runEngine(t, fx.broker, svcqueue.ConsumerConfig{
			Handler: func(_ context.Context, delivery corequeue.DeliveryValue) error {
				runs.Add(1)
				if err := inTransaction(t, fx, func(ctx context.Context) error {
					return fx.broker.Ack(ctx, delivery.Lease.Receipt)
				}); err != nil {
					return err
				}
				return corequeue.Acknowledged
			},
			HandlerIsIdempotent: true, PollInterval: fastPoll, Clock: clk,
		}, func() bool { return runs.Load() == 1 && fx.engine.rows() == 0 })
		if acks := countRole(fx.engine.roles(), "ack"); acks != 1 {
			t.Fatalf("the engine sent %d acknowledgements, want only the handler's, in its transaction", acks)
		}

		publish(t, fx.broker, "rolled back")
		var deliveries []int
		var seen atomic.Int64
		runEngine(t, fx.broker, svcqueue.ConsumerConfig{
			Handler: func(_ context.Context, delivery corequeue.DeliveryValue) error {
				deliveries = append(deliveries, delivery.Deliveries)
				if seen.Add(1) == 1 {
					_ = inTransaction(t, fx, func(ctx context.Context) error {
						if err := fx.broker.Ack(ctx, delivery.Lease.Receipt); err != nil {
							return err
						}
						return errRolledBack
					})
					clk.Advance(testVisibility)
					return corequeue.Acknowledged
				}
				return nil
			},
			HandlerIsIdempotent: true, PollInterval: fastPoll, Clock: clk,
		}, func() bool { return seen.Load() == 2 && fx.engine.rows() == 0 })
		if !slices.Equal(deliveries, []int{1, 2}) {
			t.Fatalf("a rolled-back acknowledgement was delivered as %v, want [1 2]", deliveries)
		}
	})
}

// countRole counts the statements of role in a log.
func countRole(log []string, role string) int {
	n := 0
	for _, sent := range log {
		if sent == role {
			n++
		}
	}
	return n
}

// TestTheAckedWindowAcknowledgesARedeliveryWithoutRunningTheHandler pins
// ConsumerConfig.AckedWindow on every broker (issue #1046): a message whose
// lease lapsed while its handler ran — the acknowledgement refused — comes
// back, and with a window it is acknowledged without a second run; without
// one the handler runs again.
//
// The engine runs until the broker accepted an acknowledgement, the
// redelivery's, and the queue is counted once it has stopped (issue #1369).
// An empty Stats taken while the engine ran was not an end: the file broker
// counts its queued directory, then its in-flight one, and the lapsed lease
// the next Receive renames from flight back into the queue between the two
// readings is in neither — a snapshot that races the queue, as Counter says
// —, so the run stopped before the redelivery and counted one run, not two.
func TestTheAckedWindowAcknowledgesARedeliveryWithoutRunningTheHandler(t *testing.T) {
	t.Parallel()
	type tc struct {
		factory brokerFactory
		window  int
		want    int64
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		clk := clock.NewManualClock(epoch)
		broker := &ackSpy{Broker: c.factory.make(t, clk, defaultPolicy())}
		publish(t, broker, "slow")
		var runs atomic.Int64
		runEngine(t, broker, svcqueue.ConsumerConfig{
			Handler: func(context.Context, corequeue.DeliveryValue) error {
				//: the first run outlives its lease: the work is done,
				//: its acknowledgement will be refused.
				if runs.Add(1) == 1 {
					clk.Advance(testVisibility + time.Second)
				}
				return nil
			},
			HandlerIsIdempotent: true, PollInterval: fastPoll, Clock: clk, AckedWindow: c.window,
		}, func() bool { return broker.acks.Load() == 1 })
		if got := runs.Load(); got != c.want {
			t.Fatalf("with AckedWindow %d the handler ran %d times, want %d", c.window, got, c.want)
		}
		if stats := statsOf(t, broker.Broker); stats.Queued != 0 || stats.Leased != 0 {
			t.Fatalf("Stats() = %+v once acknowledged, want an empty queue", stats)
		}
		if dead := deadLetters(t, broker.Broker, 10); len(dead) != 0 {
			t.Fatalf("the redelivery was dead-lettered: %+v", dead)
		}
	}
	var cases []tc
	for _, factory := range everyBroker() {
		cases = append(cases, tc{factory: factory, window: 0, want: 2}, tc{factory: factory, window: 8, want: 1})
	}
	for _, c := range cases {
		t.Run(c.factory.name+"/"+map[int]string{0: "none", 8: "eight"}[c.window], func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// ackSpy counts the acknowledgements its broker accepted, and forwards every
// sibling the engine asks a broker for, so the engine runs as on the broker
// itself.
type ackSpy struct {
	corequeue.Broker
	acks atomic.Int64
}

// Ack delegates, and counts an acknowledgement the broker accepted.
func (s *ackSpy) Ack(ctx context.Context, receipt corequeue.ReceiptValue) error {
	err := s.Broker.Ack(ctx, receipt)
	if err == nil {
		s.acks.Add(1)
	}
	return err
}

// Wake is the broker's.
func (s *ackSpy) Wake() corequeue.WakeValue {
	return s.Broker.(corequeue.Waker).Wake()
}

// Release is the broker's.
func (s *ackSpy) Release(ctx context.Context, receipt corequeue.ReceiptValue) error {
	return s.Broker.(corequeue.Releaser).Release(ctx, receipt)
}

// Reject is the broker's.
func (s *ackSpy) Reject(ctx context.Context, receipt corequeue.ReceiptValue, cause error) error {
	return s.Broker.(corequeue.Rejecter).Reject(ctx, receipt, cause)
}

// NackAfter is the broker's.
func (s *ackSpy) NackAfter(ctx context.Context, receipt corequeue.ReceiptValue, cause error, delay time.Duration) (corequeue.NackValue, error) {
	return s.Broker.(corequeue.DelayedNacker).NackAfter(ctx, receipt, cause, delay)
}
