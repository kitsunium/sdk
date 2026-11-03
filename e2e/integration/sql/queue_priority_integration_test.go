//go:build integration

// Package sql_test — a message's level on each real engine (issue #1045):
// SQLPriorityMigration on a table SQLMigration made and an earlier broker
// filled, every statement SQLConfig.Priority changes parsed and run, the old
// rows read at the normal level, and the highest level received first.
package sql_test

import (
	"strings"
	"testing"
	"time"

	"github.com/kitsunium/sdk/pkg/v1/clock"
	"github.com/kitsunium/sdk/pkg/v1/data/queue"
)

// TestThePriorityColumnOnEveryEngine runs SQLPriorityMigration before the
// table exists (nothing to do), then SQLMigration's table, filled by a broker
// without Priority — a queued row and a dead one —, then the priority
// migration twice, down and up again. A broker with Priority reads the old
// rows at the normal level, publishes at the extreme levels, replays the old
// dead letter, copies a message with its level, and receives the highest
// level first, the old rows in their order, the lowest last.
func TestThePriorityColumnOnEveryEngine(t *testing.T) {
	t.Parallel()
	eachEngine(t, func(t *testing.T, e *engine) {
		ctx, table := t.Context(), uniqueName("jobs__")
		levels, err := queue.SQLPriorityMigration(e.dialect, table, 1)
		must(t, err)
		create, err := queue.SQLMigration(e.dialect, table, 2)
		must(t, err)
		migrate(t, e, levels, create)
		tm := transactor(t, e)
		clk := clock.NewManualClock(queueEpoch)
		policy := queue.Policy{VisibilityTimeout: 10 * time.Second, RetryDelay: time.Second, MaxDeliveries: 3}

		before, err := queue.NewSQL(queue.SQLConfig{Transactor: tm, Dialect: e.dialect, Table: table, Policy: policy, Clock: clk})
		must(t, err)
		_, err = before.Publish(ctx, []byte("old queued"))
		must(t, err)
		clk.Advance(time.Millisecond)
		old, err := before.Publish(ctx, []byte("old dead"))
		must(t, err)
		clk.Advance(time.Millisecond)
		leased, err := before.Receive(ctx, 2)
		must(t, err)
		if len(leased) != 2 {
			t.Fatalf("Receive(2) = %d deliveries, want 2", len(leased))
		}
		for _, delivery := range leased {
			if delivery.Message.ID == old.ID {
				must(t, before.(queue.Rejecter).Reject(ctx, delivery.Lease.Receipt, nil))
				continue
			}
			must(t, before.(queue.Releaser).Release(ctx, delivery.Lease.Receipt))
		}

		must(t, levels.Up(ctx, e.db))
		must(t, levels.Down(ctx, e.db))
		for range 2 {
			must(t, levels.Up(ctx, e.db))
		}
		broker, err := queue.NewSQL(queue.SQLConfig{
			Transactor: tm, Dialect: e.dialect, Table: table, Policy: policy, Clock: clk, Trail: true, Priority: true,
		})
		must(t, err)
		for _, dead := range deadByID(t, broker) {
			if dead.Priority != queue.PriorityNormal {
				t.Errorf("an old dead letter reads at level %d, want the normal one", dead.Priority)
			}
		}
		publisher := broker.(queue.PriorityPublisher)
		_, err = publisher.PublishPriority(ctx, []byte("lowest"), queue.PriorityLowest)
		must(t, err)
		_, err = publisher.PublishPriority(ctx, []byte("highest"), queue.PriorityHighest)
		must(t, err)
		_, err = broker.(queue.MessageWriter).PutMessage(ctx, queue.LiveMessage{
			VisibleAt: clk.Now(), Message: queue.Message{Payload: []byte("copied high"), EnqueuedAt: clk.Now()}, Priority: queue.PriorityHigh,
		})
		must(t, err)
		must(t, broker.(queue.DeadLetterManager).ReplayDeadLetter(ctx, old.ID))
		clk.Advance(time.Millisecond)

		var got []string
		for range 5 {
			got = append(got, string(leaseOne(t, broker).Message.Payload))
		}
		if strings.Join(got, ", ") != "highest, copied high, old queued, old dead, lowest" {
			t.Fatalf("received %q, want the levels highest first and the old rows at the normal one", got)
		}
	})
}
