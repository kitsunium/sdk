//go:build integration

// Package sql_test — a dead letter's trail on each real engine (issue #500):
// the trail column SQLMigration creates and SQLTrailMigration adds to a table
// made without it, every statement SQLConfig.Trail changes parsed and run, and
// a broker without Trail over the same table.
package sql_test

import (
	"slices"
	"testing"
	"time"

	"github.com/kitsunium/sdk/pkg/v1/clock"
	"github.com/kitsunium/sdk/pkg/v1/data/queue"
	"github.com/kitsunium/sdk/pkg/v1/data/sql"
	"github.com/kitsunium/sdk/pkg/v1/errs"
)

// The codes of a failure shaped as a mail spool shapes one: the origin, and
// the code a wrapper adds to its trail.
const (
	trailOrigin errs.Code = 0x40_01_01_02
	trailMark   errs.Code = 0x40_01_01_03
)

// trailedFailure is a failure whose origin is trailOrigin and whose trail
// holds trailMark, then the queue's NOT_RETRYABLE.
func trailedFailure() error {
	origin := errs.New(trailOrigin, "SEND_REFUSED", "the relay refused the mail", "private half")
	return queue.DoNotRetry(errs.Wrap(origin, errs.WrapParams{Code: trailMark, Reason: "MAIL_REJECTED", Public: "rejected", Private: "private"}))
}

// migrate applies migrations, in order, under a version table of their own.
func migrate(t *testing.T, e *engine, migrations ...sql.Migration) {
	t.Helper()
	runner, err := sql.NewMigrator(sql.Config{DB: e.db, Dialect: e.dialect, Pool: sql.PoolConfig{MaxOpen: 16}},
		sql.MigrateConfig{Migrations: migrations, VersionTable: uniqueName("versions_")})
	must(t, err)
	must(t, runner.Up(t.Context()))
}

// TestTheTrailColumnOnEveryEngine runs SQLTrailMigration on a table that is
// not there yet, then SQLMigration's table — which has the column —, then
// drops the column with SQLTrailMigration's Down and adds it back with its Up,
// twice: a table an earlier release made, migrated. Over it a broker with
// Trail buries a rejected message, a last nack and a lapsed lease, writes a
// dead letter with sixteen codes, and replays one that dies again with
// nothing of its first death; a broker without Trail over the same table
// reads every dead letter with its cause and no trail.
func TestTheTrailColumnOnEveryEngine(t *testing.T) {
	t.Parallel()
	eachEngine(t, func(t *testing.T, e *engine) {
		ctx, table := t.Context(), uniqueName("jobs__")
		trail, err := queue.SQLTrailMigration(e.dialect, table, 1)
		must(t, err)
		create, err := queue.SQLMigration(e.dialect, table, 2)
		must(t, err)
		migrate(t, e, trail, create)
		must(t, trail.Down(ctx, e.db))
		for range 2 {
			must(t, trail.Up(ctx, e.db))
		}
		tm := transactor(t, e)

		clk := clock.NewManualClock(queueEpoch)
		policy := queue.Policy{VisibilityTimeout: 10 * time.Second, RetryDelay: time.Second, MaxDeliveries: 1}
		broker, err := queue.NewSQL(queue.SQLConfig{Transactor: tm, Dialect: e.dialect, Table: table, Policy: policy, Clock: clk, Trail: true})
		must(t, err)
		want := []uint32{uint32(trailMark), uint32(queue.CodeNotRetryable)}

		rejected, err := broker.Publish(ctx, []byte("rejected"))
		must(t, err)
		must(t, broker.(queue.Rejecter).Reject(ctx, leaseOne(t, broker).Lease.Receipt, trailedFailure()))
		nacked, err := broker.Publish(ctx, []byte("nacked"))
		must(t, err)
		if verdict, err := broker.Nack(ctx, leaseOne(t, broker).Lease.Receipt, trailedFailure()); err != nil || !verdict.DeadLettered {
			t.Fatalf("the last nack = %+v, %v", verdict, err)
		}
		lapsed, err := broker.Publish(ctx, []byte("lapsed"))
		must(t, err)
		leaseOne(t, broker)
		clk.Advance(10 * time.Second)
		leaseNone(t, broker)
		long := make([]uint32, 16)
		for i := range long {
			long[i] = 0xFFFF_FFF0 + uint32(i)
		}
		written, err := broker.(queue.DeadLetterWriter).PutDeadLetter(ctx, queue.DeadLetter{
			Message:  queue.Message{Payload: []byte("written"), EnqueuedAt: queueEpoch},
			FailedAt: queueEpoch, Deliveries: 2, Reason: "CUSTOM", Code: 7, Trail: long,
		})
		must(t, err)

		byID := deadByID(t, broker)
		for id, trail := range map[string][]uint32{rejected.ID: want, nacked.ID: want, lapsed.ID: nil, written: long} {
			got, found := byID[id]
			if !found || !slices.Equal(got.Trail, trail) || (trail == nil) != (got.Trail == nil) {
				t.Errorf("dead letter %s (found %v): trail %#x, want %#x", id, found, got.Trail, trail)
			}
		}
		if got := byID[rejected.ID]; got.Reason != "SEND_REFUSED" || got.Code != uint32(trailOrigin) {
			t.Errorf("the rejected message's cause = %q %#x, want the origin's", got.Reason, got.Code)
		}

		must(t, broker.(queue.DeadLetterManager).ReplayDeadLetter(ctx, rejected.ID))
		leaseOne(t, broker)
		clk.Advance(10 * time.Second)
		leaseNone(t, broker)
		if again := deadByID(t, broker)[rejected.ID]; again.Reason != "LEASE_EXPIRED" || again.Trail != nil {
			t.Errorf("the replayed message died again as %q with trail %#x, want LEASE_EXPIRED and none", again.Reason, again.Trail)
		}

		without, err := queue.NewSQL(queue.SQLConfig{Transactor: tm, Dialect: e.dialect, Table: table, Policy: policy, Clock: clk})
		must(t, err)
		for id, dead := range deadByID(t, without) {
			if dead.Trail != nil || dead.Reason == "" {
				t.Errorf("without Trail, dead letter %s = %+v, want its cause and no trail", id, dead)
			}
		}
	})
}

// deadByID reads broker's dead letters by identifier.
func deadByID(t *testing.T, broker queue.Broker) map[string]queue.DeadLetter {
	t.Helper()
	dead, err := broker.(queue.DeadLetterReader).DeadLetters(t.Context(), 100)
	must(t, err)
	out := make(map[string]queue.DeadLetter, len(dead))
	for _, d := range dead {
		out[d.Message.ID] = d
	}
	return out
}
