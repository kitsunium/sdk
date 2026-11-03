package sqlite_test

import (
	stdsql "database/sql"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kitsunium/sdk/pkg/v1/clock"
	"github.com/kitsunium/sdk/pkg/v1/data/queue"
	"github.com/kitsunium/sdk/pkg/v1/data/sql"
)

// The priority column (ADR 0054, amended 2026-11-03, issue #1045) on a real
// SQLite file: rows written by a broker that knew nothing of levels — a
// queued message and a dead letter, in the table SQLMigration created —,
// then SQLPriorityMigration, then a broker with SQLConfig.Priority over the
// same table, which reads the old rows at the normal level and hands out the
// highest level first. The statements are parsed and run by SQLite, which
// the SDK's fake engine never does.

// TestOldRowsReadAtTheNormalLevelAfterThePriorityMigration is the
// compatibility row of the issue for the SQL broker.
func TestOldRowsReadAtTheNormalLevelAfterThePriorityMigration(t *testing.T) {
	start := time.Date(2026, time.November, 3, 12, 0, 0, 0, time.UTC)
	clk := clock.NewManualClock(start)
	ctx := t.Context()
	db, err := stdsql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "queue.sqlite")+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	tm, err := sql.NewTransactor(sql.Config{DB: db, Dialect: sql.DialectSQLite, Pool: sql.PoolConfig{MaxOpen: 1}})
	if err != nil {
		t.Fatalf("NewTransactor: %v", err)
	}
	table, err := queue.SQLMigration(sql.DialectSQLite, "jobs", 1)
	if err != nil {
		t.Fatalf("SQLMigration: %v", err)
	}
	if err := table.Up(ctx, db); err != nil {
		t.Fatalf("the table: %v", err)
	}
	policy := queue.Policy{VisibilityTimeout: time.Minute, MaxDeliveries: 3}
	before, err := queue.NewSQL(queue.SQLConfig{Transactor: tm, Dialect: sql.DialectSQLite, Table: "jobs", Policy: policy, Clock: clk})
	if err != nil {
		t.Fatalf("NewSQL without Priority: %v", err)
	}
	if _, err := before.Publish(ctx, []byte("old queued")); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	clk.Advance(time.Millisecond)
	if _, err := before.Publish(ctx, []byte("old dead")); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	if _, err := before.(queue.PriorityPublisher).PublishPriority(ctx, []byte("refused"), queue.PriorityHigh); err == nil {
		t.Fatal("PublishPriority(high) without the column = nil, want a refusal")
	}
	clk.Advance(time.Second)
	batch, err := before.Receive(ctx, 2)
	if err != nil || len(batch) != 2 {
		t.Fatalf("Receive(2) = %d, %v", len(batch), err)
	}
	for _, delivery := range batch {
		if string(delivery.Message.Payload) == "old dead" {
			if err := before.(queue.Rejecter).Reject(ctx, delivery.Lease.Receipt, nil); err != nil {
				t.Fatalf("Reject: %v", err)
			}
			continue
		}
		if err := before.(queue.Releaser).Release(ctx, delivery.Lease.Receipt); err != nil {
			t.Fatalf("Release: %v", err)
		}
	}

	levels, err := queue.SQLPriorityMigration(sql.DialectSQLite, "jobs", 2)
	if err != nil {
		t.Fatalf("SQLPriorityMigration: %v", err)
	}
	for range 2 {
		if err := levels.Up(ctx, db); err != nil {
			t.Fatalf("the priority column, applied twice: %v", err)
		}
	}
	broker, err := queue.NewSQL(queue.SQLConfig{
		Transactor: tm, Dialect: sql.DialectSQLite, Table: "jobs", Policy: policy, Clock: clk, Priority: true,
	})
	if err != nil {
		t.Fatalf("NewSQL with Priority: %v", err)
	}
	live, err := broker.(queue.MessageReader).Messages(ctx, "", 10)
	if err != nil || len(live) != 1 || live[0].Priority != queue.PriorityNormal || string(live[0].Message.Payload) != "old queued" {
		t.Fatalf("Messages over an old row = %+v, %v, want it at the normal level", live, err)
	}
	dead, err := broker.(queue.DeadLetterReader).DeadLetters(ctx, 10)
	if err != nil || len(dead) != 1 || dead[0].Priority != queue.PriorityNormal {
		t.Fatalf("DeadLetters over an old row = %+v, %v, want it at the normal level", dead, err)
	}
	publisher := broker.(queue.PriorityPublisher)
	for _, level := range []queue.Priority{queue.PriorityLow, queue.PriorityHighest} {
		if _, err := publisher.PublishPriority(ctx, []byte(strings.Repeat("x", int(level)+3)), level); err != nil {
			t.Fatalf("PublishPriority(%d): %v", level, err)
		}
	}
	if err := broker.(queue.DeadLetterManager).ReplayDeadLetter(ctx, dead[0].Message.ID); err != nil {
		t.Fatalf("ReplayDeadLetter: %v", err)
	}
	clk.Advance(time.Second)
	var got []string
	for range 4 {
		one, err := broker.Receive(ctx, 1)
		if err != nil || len(one) != 1 {
			t.Fatalf("Receive(1) = %d, %v", len(one), err)
		}
		got = append(got, string(one[0].Message.Payload))
	}
	if strings.Join(got, " ") != "xxxxx old queued old dead xx" {
		t.Fatalf("received %q, want the highest, the two old rows as normal in their order, then the low one", got)
	}
	if err := levels.Down(ctx, db); err != nil {
		t.Fatalf("the priority column dropped: %v", err)
	}
}
