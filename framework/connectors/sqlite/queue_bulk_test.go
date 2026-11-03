package sqlite_test

import (
	"context"
	stdsql "database/sql"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/kitsunium/sdk/pkg/v1/clock"
	"github.com/kitsunium/sdk/pkg/v1/data/queue"
	"github.com/kitsunium/sdk/pkg/v1/data/sql"
)

// The SQL broker's statements of the siblings that act on a whole queue (ADR
// 0054 and ADR 0151, amended 2026-11-02) on a real SQLite file, which the
// SDK's fake engine cannot be: it recognises the statements by their text and
// never parses them, so a statement SQLite refuses — a RETURNING, a
// sub-select of the table an UPDATE changes, a GROUP BY over an expression —
// fails here first.

// sqliteQueue is the SQL broker over a fresh SQLite file, its table created,
// on clk, with policy.
func sqliteQueue(t *testing.T, clk clock.Clock, policy queue.Policy) queue.Broker {
	t.Helper()
	db, err := stdsql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "queue.sqlite")+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	tm, err := sql.NewTransactor(sql.Config{DB: db, Dialect: sql.DialectSQLite, Pool: sql.PoolConfig{MaxOpen: 1}})
	if err != nil {
		t.Fatalf("NewTransactor: %v", err)
	}
	migration, err := queue.SQLMigration(sql.DialectSQLite, "jobs", 1)
	if err != nil {
		t.Fatalf("SQLMigration: %v", err)
	}
	if err := migration.Up(t.Context(), db); err != nil {
		t.Fatalf("the migration: %v", err)
	}
	broker, err := queue.NewSQL(queue.SQLConfig{
		Transactor: tm, Dialect: sql.DialectSQLite, Table: "jobs", Policy: policy, Clock: clk, Trail: true,
	})
	if err != nil {
		t.Fatalf("NewSQL: %v", err)
	}
	return broker
}

// TestTheSQLBrokersBulkStatementsRunOnSQLite runs every statement the new
// siblings send on a real SQLite file: a batch published over several
// INSERTs, counted, partly acknowledged by one DELETE … RETURNING, the rest
// dead-lettered, paged, replayed and purged in bulk, a message aged out and a
// dead letter past its retention swept by a Receive, and the queue purged.
func TestTheSQLBrokersBulkStatementsRunOnSQLite(t *testing.T) {
	start := time.Date(2026, time.November, 2, 12, 0, 0, 0, time.UTC)
	clk := clock.NewManualClock(start)
	broker := sqliteQueue(t, clk, queue.Policy{
		VisibilityTimeout: time.Minute, MaxDeliveries: 1, MaxAge: time.Hour, DeadLetterRetention: 24 * time.Hour,
	})
	ctx := t.Context()
	payloads := make([][]byte, 300)
	for i := range payloads {
		payloads[i] = []byte{byte(i)}
	}
	messages, err := broker.(queue.BatchPublisher).PublishMany(ctx, payloads)
	if err != nil || len(messages) != 300 {
		t.Fatalf("PublishMany(300) = %d, %v", len(messages), err)
	}
	if stats, err := broker.(queue.Counter).Stats(ctx); err != nil || stats.Queued != 300 || !stats.OldestEnqueuedAt.Equal(start) {
		t.Fatalf("Stats() = %+v, %v, want 300 queued since %v", stats, err, start)
	}
	batch, err := broker.Receive(ctx, 200)
	if err != nil || len(batch) != 200 {
		t.Fatalf("Receive(200) = %d, %v", len(batch), err)
	}
	receipts := make([]queue.Receipt, 0, 101)
	for _, delivery := range batch[:100] {
		receipts = append(receipts, delivery.Lease.Receipt)
	}
	result, err := broker.(queue.Acker).AckMany(ctx, append(receipts, "not-a-receipt"))
	if err != nil || len(result.Outcomes) != 101 || result.Outcomes[0] != queue.AckAcknowledged || result.Outcomes[100] != queue.AckUnknownReceipt {
		t.Fatalf("AckMany(100 + 1) = %v, %v", err, len(result.Outcomes))
	}
	rejecter := broker.(queue.Rejecter)
	for _, delivery := range batch[100:] {
		if err := rejecter.Reject(ctx, delivery.Lease.Receipt, nil); err != nil {
			t.Fatalf("Reject: %v", err)
		}
	}
	page, err := broker.(queue.DeadLetterPager).DeadLettersAfter(ctx, "", 60)
	if err != nil || len(page) != 60 {
		t.Fatalf("DeadLettersAfter(\"\", 60) = %d, %v", len(page), err)
	}
	rest, err := broker.(queue.DeadLetterPager).DeadLettersAfter(ctx, page[59].Message.ID, 100)
	if err != nil || len(rest) != 40 || rest[0].Message.ID <= page[59].Message.ID {
		t.Fatalf("DeadLettersAfter(the 60th, 100) = %d, %v", len(rest), err)
	}
	bulk := broker.(queue.DeadLetterBulkManager)
	if replayed, err := bulk.ReplayDeadLetters(ctx, 30); err != nil || replayed != 30 {
		t.Fatalf("ReplayDeadLetters(30) = %d, %v", replayed, err)
	}
	clk.Advance(time.Minute)
	if removed, err := bulk.PurgeDeadLetters(ctx, clk.Now(), 20); err != nil || removed != 20 {
		t.Fatalf("PurgeDeadLetters(now, 20) = %d, %v", removed, err)
	}
	stats, err := broker.(queue.Counter).Stats(ctx)
	if err != nil || stats.Queued != 130 || stats.Dead != 50 || stats.Leased != 0 {
		t.Fatalf("Stats() = %+v, %v, want 130 queued and 50 dead", stats, err)
	}
	clk.Advance(time.Hour)
	if delivered, err := broker.Receive(ctx, 10); err != nil || len(delivered) != 0 {
		t.Fatalf("Receive past MaxAge = %d, %v, want none", len(delivered), err)
	}
	if stats, err := broker.(queue.Counter).Stats(ctx); err != nil || stats.Queued != 0 || stats.Dead != 180 {
		t.Fatalf("after MaxAge Stats() = %+v, %v, want 180 dead", stats, err)
	}
	clk.Advance(25 * time.Hour)
	if _, err := broker.Receive(ctx, 1); err != nil {
		t.Fatalf("Receive past the retention: %v", err)
	}
	if dead, err := broker.(queue.DeadLetterReader).DeadLetters(ctx, 10); err != nil || len(dead) != 0 {
		t.Fatalf("past the retention the store holds %d, %v, want none", len(dead), err)
	}
	if _, err := broker.(queue.BatchPublisher).PublishMany(ctx, [][]byte{[]byte("a"), []byte("b")}); err != nil {
		t.Fatalf("PublishMany(2): %v", err)
	}
	if _, err := broker.Receive(ctx, 1); err != nil {
		t.Fatalf("Receive: %v", err)
	}
	if purged, err := broker.(queue.Purger).Purge(ctx); err != nil || purged != (queue.Purged{Queued: 1, Leased: 1}) {
		t.Fatalf("Purge() = %+v, %v, want one queued and one leased", purged, err)
	}
	var listed []string
	for live, err := range broker.(queue.MessageLister).AllMessages(context.Background(), "") {
		if err != nil {
			t.Fatalf("AllMessages: %v", err)
		}
		listed = append(listed, live.Message.ID)
	}
	if !slices.Equal(listed, nil) {
		t.Fatalf("AllMessages after Purge = %v, want none", listed)
	}
}
