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
func TestTheAckedWindowAcknowledgesARedeliveryWithoutRunningTheHandler(t *testing.T) {
	t.Parallel()
	for _, factory := range everyBroker() {
		for _, window := range []int{0, 8} {
			t.Run(factory.name+"/"+map[int]string{0: "none", 8: "eight"}[window], func(t *testing.T) {
				t.Parallel()
				clk := clock.NewManualClock(epoch)
				broker := factory.make(t, clk, defaultPolicy())
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
					HandlerIsIdempotent: true, PollInterval: fastPoll, Clock: clk, AckedWindow: window,
				}, func() bool {
					stats := statsOf(t, broker)
					return stats.Queued == 0 && stats.Leased == 0 && runs.Load() >= 1
				})
				want := int64(2)
				if window > 0 {
					want = 1
				}
				if got := runs.Load(); got != want {
					t.Fatalf("with AckedWindow %d the handler ran %d times, want %d", window, got, want)
				}
				if dead := deadLetters(t, broker, 10); len(dead) != 0 {
					t.Fatalf("the redelivery was dead-lettered: %+v", dead)
				}
			})
		}
	}
}
