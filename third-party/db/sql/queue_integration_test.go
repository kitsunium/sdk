//go:build integration

// Package sql_test — the queue's SQL broker (ADR 0151) on each real engine:
// the lease, the retry on a growing delay, the dead letter with its cause, the
// replay and the deletion, a rejected message, the caller's transaction joined
// and rolled back, payload bytes as published, the driver's text withheld, and
// sixteen consumers draining one queue without ever sharing a message — SKIP
// LOCKED on PostgreSQL and MySQL, the one write lock on SQLite.
package sql_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kitsunium/sdk/pkg/v1/clock"
	"github.com/kitsunium/sdk/pkg/v1/errs"
	"github.com/kitsunium/sdk/pkg/v1/queue"
	"github.com/kitsunium/sdk/pkg/v1/sql"
)

// queueEpoch anchors the brokers' ManualClocks: every instant the SQL broker
// writes is its own clock's, never the database's NOW().
var queueEpoch = time.Date(2026, time.September, 30, 12, 0, 0, 0, time.UTC)

// queueFixture is one case's SQL broker, its transactor, and its table.
type queueFixture struct {
	tm     sql.Transactor
	broker queue.Broker
	table  string
}

// openQueue migrates a fresh table on e and opens a broker over it.
func openQueue(tb testing.TB, e *engine, base queue.SQLConfig) *queueFixture {
	tb.Helper()
	table := uniqueName("jobs__")
	migration, err := queue.SQLMigration(e.dialect, table, 1)
	must(tb, err)
	runner, err := sql.NewMigrator(sql.Config{DB: e.db, Dialect: e.dialect, Pool: sql.PoolConfig{MaxOpen: 16}},
		sql.MigrateConfig{Migrations: []sql.Migration{migration}, VersionTable: uniqueName("versions_")})
	must(tb, err)
	must(tb, runner.Up(tb.Context()))
	tm := transactor(tb, e)
	cfg := base
	cfg.Transactor, cfg.Dialect, cfg.Table = tm, e.dialect, table
	broker, err := queue.NewSQL(cfg)
	must(tb, err)
	return &queueFixture{tm: tm, broker: broker, table: table}
}

// queuePolicy grows the retry delay from one second to three, over three
// attempts.
func queuePolicy() queue.Policy {
	return queue.Policy{VisibilityTimeout: 10 * time.Second, RetryDelay: time.Second, MaxRetryDelay: 3 * time.Second, MaxDeliveries: 3}
}

// leaseOne leases exactly one message.
func leaseOne(t *testing.T, broker queue.Broker) queue.Delivery {
	t.Helper()
	batch, err := broker.Receive(t.Context(), 1)
	must(t, err)
	if len(batch) != 1 {
		t.Fatalf("Receive(1) returned %d deliveries, want 1", len(batch))
	}
	return batch[0]
}

// leaseNone asserts nothing is receivable.
func leaseNone(t *testing.T, broker queue.Broker) {
	t.Helper()
	batch, err := broker.Receive(t.Context(), 10)
	must(t, err)
	if len(batch) != 0 {
		t.Fatalf("Receive(10) returned %d deliveries, want none", len(batch))
	}
}

// TestTheSQLQueueLifecycleOnEveryEngine walks one message through every state
// on each engine: queued, leased, retried on the growing delay, redelivered
// after a lapsed lease, dead-lettered with its cause after its last attempt,
// replayed as a first delivery, rejected at once, and deleted — its payload's
// bytes, a NUL and a non-UTF-8 byte among them, unchanged throughout.
func TestTheSQLQueueLifecycleOnEveryEngine(t *testing.T) {
	t.Parallel()
	eachEngine(t, func(t *testing.T, e *engine) {
		clk := clock.NewManualClock(queueEpoch)
		fx := openQueue(t, e, queue.SQLConfig{Clock: clk, Policy: queuePolicy()})
		ctx, payload := t.Context(), []byte("order\x00\xff42")
		published, err := fx.broker.Publish(ctx, payload)
		must(t, err)

		first := leaseOne(t, fx.broker)
		if first.Message.ID != published.ID || string(first.Message.Payload) != string(payload) || first.Deliveries != 1 {
			t.Fatalf("first delivery = %+v, want the published message, byte for byte", first)
		}
		leaseNone(t, fx.broker)
		verdict, err := fx.broker.Nack(ctx, first.Lease.Receipt, nil)
		must(t, err)
		if verdict.VisibleAt.Sub(clk.Now()) != time.Second {
			t.Fatalf("the first retry is due in %v, want 1s", verdict.VisibleAt.Sub(clk.Now()))
		}
		clk.Advance(time.Second)

		second := leaseOne(t, fx.broker)
		if second.Deliveries != 2 {
			t.Fatalf("second delivery counted %d, want 2", second.Deliveries)
		}
		clk.Advance(10 * time.Second) // the consumer "died": its lease lapses
		third := leaseOne(t, fx.broker)
		if third.Deliveries != 3 {
			t.Fatalf("the redelivery after a lapsed lease counted %d, want 3", third.Deliveries)
		}
		if ackErr := fx.broker.Ack(ctx, second.Lease.Receipt); !errors.Is(ackErr, queue.LeaseExpired) {
			t.Fatalf("Ack(lapsed) = %v, want LEASE_EXPIRED", ackErr)
		}
		cause := errs.New(0x40_01_01_01, "DOWNSTREAM_REFUSED", "the downstream refused the order", "private half")
		verdict, err = fx.broker.Nack(ctx, third.Lease.Receipt, cause)
		must(t, err)
		if !verdict.DeadLettered {
			t.Fatal("the last attempt was not dead-lettered")
		}
		dead, err := fx.broker.(queue.DeadLetterReader).DeadLetters(ctx, 10)
		must(t, err)
		if len(dead) != 1 || dead[0].Reason != "DOWNSTREAM_REFUSED" || dead[0].Cause != "the downstream refused the order" ||
			dead[0].Code != 0x40_01_01_01 || dead[0].Deliveries != 3 || string(dead[0].Message.Payload) != string(payload) {
			t.Fatalf("DeadLetters() = %+v, want the message with its cause", dead)
		}

		manager := fx.broker.(queue.DeadLetterManager)
		must(t, manager.ReplayDeadLetter(ctx, published.ID))
		replayed := leaseOne(t, fx.broker)
		if replayed.Message.ID != published.ID || replayed.Deliveries != 1 || !replayed.Message.EnqueuedAt.Equal(published.EnqueuedAt) {
			t.Fatalf("the replayed delivery = %+v, want the message on its first delivery", replayed)
		}
		must(t, fx.broker.(queue.Rejecter).Reject(ctx, replayed.Lease.Receipt, queue.DoNotRetry(nil)))
		dead, err = fx.broker.(queue.DeadLetterReader).DeadLetters(ctx, 10)
		must(t, err)
		if len(dead) != 1 || dead[0].Reason != "NOT_RETRYABLE" || dead[0].Deliveries != 1 {
			t.Fatalf("after Reject, DeadLetters() = %+v, want one NOT_RETRYABLE after 1 delivery", dead)
		}
		must(t, manager.DeleteDeadLetter(ctx, published.ID))
		if again := manager.DeleteDeadLetter(ctx, published.ID); !errors.Is(again, queue.DeadLetterNotFound) {
			t.Fatalf("a second DeleteDeadLetter = %v, want DeadLetterNotFound", again)
		}
		clk.Advance(time.Hour)
		leaseNone(t, fx.broker)
	})
}

// TestAnExtendedLeaseAndAnEmptyPayloadOnEveryEngine pins Extend — a new
// receipt, the old one void, the lease held past its first deadline — and an
// empty payload read back empty, whatever the driver binds it as.
func TestAnExtendedLeaseAndAnEmptyPayloadOnEveryEngine(t *testing.T) {
	t.Parallel()
	eachEngine(t, func(t *testing.T, e *engine) {
		clk := clock.NewManualClock(queueEpoch)
		fx := openQueue(t, e, queue.SQLConfig{Clock: clk, Policy: queuePolicy()})
		ctx := t.Context()
		for _, payload := range [][]byte{nil, {}} {
			_, err := fx.broker.Publish(ctx, payload)
			must(t, err)
			clk.Advance(time.Millisecond)
		}
		for range 2 {
			delivery := leaseOne(t, fx.broker)
			if len(delivery.Message.Payload) != 0 {
				t.Fatalf("payload = %q, want empty", delivery.Message.Payload)
			}
			renewed, err := fx.broker.(queue.LeaseExtender).Extend(ctx, delivery.Lease.Receipt, time.Hour)
			must(t, err)
			if ackErr := fx.broker.Ack(ctx, delivery.Lease.Receipt); !errors.Is(ackErr, queue.LeaseExpired) {
				t.Fatalf("Ack(the replaced receipt) = %v, want LEASE_EXPIRED", ackErr)
			}
			clk.Advance(time.Minute)
			must(t, fx.broker.Ack(ctx, renewed.Receipt))
		}
		leaseNone(t, fx.broker)
	})
}

// TestAPublicationJoinsTheCallersTransactionOnEveryEngine is the reason the
// SQL broker exists: published inside the caller's transaction, the message
// exists if and only if that transaction commits.
func TestAPublicationJoinsTheCallersTransactionOnEveryEngine(t *testing.T) {
	t.Parallel()
	eachEngine(t, func(t *testing.T, e *engine) {
		fx := openQueue(t, e, queue.SQLConfig{Clock: clock.NewManualClock(queueEpoch), Policy: queuePolicy()})
		errRolledBack := errors.New("the business write failed")
		err := sql.Transact(t.Context(), fx.tm, func(ctx context.Context, _ sql.Executor) error {
			_, pubErr := fx.broker.Publish(ctx, []byte("rolled back"))
			must(t, pubErr)
			return errRolledBack
		})
		if !errors.Is(err, errRolledBack) {
			t.Fatalf("Transact() = %v, want the function's error", err)
		}
		leaseNone(t, fx.broker)
		must(t, sql.Transact(t.Context(), fx.tm, func(ctx context.Context, _ sql.Executor) error {
			_, pubErr := fx.broker.Publish(ctx, []byte("committed"))
			return pubErr
		}))
		if got := leaseOne(t, fx.broker); string(got.Message.Payload) != "committed" {
			t.Fatalf("received %q, want the committed publication", got.Message.Payload)
		}
	})
}

// TestConsumersNeverShareAMessageOnEveryEngine drains 200 messages with
// sixteen concurrent consumers on the real clock: every message is delivered
// exactly once — no failure, and a visibility timeout no handler outlives —
// whatever the engine's exclusion is.
func TestConsumersNeverShareAMessageOnEveryEngine(t *testing.T) {
	t.Parallel()
	const messages, consumers int = 200, 16
	eachEngine(t, func(t *testing.T, e *engine) {
		fx := openQueue(t, e, queue.SQLConfig{Policy: queue.Policy{VisibilityTimeout: time.Minute, MaxDeliveries: 1}})
		for range messages {
			_, err := fx.broker.Publish(t.Context(), []byte("m"))
			must(t, err)
		}
		var mu sync.Mutex
		seen := map[string]int{}
		var wg sync.WaitGroup
		for range consumers {
			wg.Go(func() {
				for {
					batch, err := fx.broker.Receive(t.Context(), 4)
					if err != nil {
						t.Errorf("Receive() = %v", err)
						return
					}
					if len(batch) == 0 {
						return
					}
					for _, d := range batch {
						mu.Lock()
						seen[d.Message.ID]++
						mu.Unlock()
						if ackErr := fx.broker.Ack(t.Context(), d.Lease.Receipt); ackErr != nil {
							t.Errorf("Ack() = %v", ackErr)
						}
					}
				}
			})
		}
		wg.Wait()
		if len(seen) != messages {
			t.Fatalf("delivered %d distinct messages, want %d", len(seen), messages)
		}
		for id, times := range seen {
			if times != 1 {
				t.Fatalf("message %s was delivered %d times, want once", id, times)
			}
		}
	})
}

// TestConsumeRejectsADoNotRetryFailureOnEveryEngine drives the engine end to
// end on each database: the handler says no retry can fix it, and the message
// is in the dead-letter store after ONE of its three attempts.
func TestConsumeRejectsADoNotRetryFailureOnEveryEngine(t *testing.T) {
	t.Parallel()
	eachEngine(t, func(t *testing.T, e *engine) {
		fx := openQueue(t, e, queue.SQLConfig{Policy: queuePolicy()})
		_, err := fx.broker.Publish(t.Context(), []byte("not json"))
		must(t, err)
		ctx, cancel := context.WithCancel(t.Context())
		var wg sync.WaitGroup
		wg.Go(func() {
			consumeErr := queue.Consume(ctx, fx.broker, queue.ConsumerConfig{
				Handler: func(context.Context, queue.Delivery) error {
					return queue.DoNotRetry(errs.New(0x40_01_01_02, "ORDER_UNDECODABLE", "the order does not decode", "private"))
				},
				HandlerIsIdempotent: true, PollInterval: 10 * time.Millisecond,
			})
			if consumeErr != nil {
				t.Errorf("Consume() = %v", consumeErr)
			}
		})
		defer func() { cancel(); wg.Wait() }()
		deadline := time.Now().Add(30 * time.Second)
		for time.Now().Before(deadline) {
			dead, deadErr := fx.broker.(queue.DeadLetterReader).DeadLetters(t.Context(), 10)
			must(t, deadErr)
			if len(dead) == 1 {
				if dead[0].Reason != "ORDER_UNDECODABLE" || dead[0].Deliveries != 1 {
					t.Fatalf("dead letter = %q after %d deliveries, want ORDER_UNDECODABLE after 1", dead[0].Reason, dead[0].Deliveries)
				}
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatal("the rejected message never reached the dead-letter store")
	})
}

// TestLapsedLeasesAndTheStreamsOrderOnEveryEngine sends the statements the
// lifecycle case does not: a lease lapsed with no attempt left, buried with
// LEASE_EXPIRED by the read that finds it; a batch that does not fill, which
// asks when the next message is due; and the order — publication order for
// one consumer with no failure, and a single retry stepping aside.
func TestLapsedLeasesAndTheStreamsOrderOnEveryEngine(t *testing.T) {
	t.Parallel()
	eachEngine(t, func(t *testing.T, e *engine) {
		clk := clock.NewManualClock(queueEpoch)
		fx := openQueue(t, e, queue.SQLConfig{Clock: clk, Policy: queue.Policy{VisibilityTimeout: 10 * time.Second, RetryDelay: time.Second, MaxDeliveries: 1}})
		ctx := t.Context()
		for _, payload := range []string{"first", "second", "third"} {
			clk.Advance(time.Millisecond)
			_, err := fx.broker.Publish(ctx, []byte(payload))
			must(t, err)
		}
		batch, err := fx.broker.Receive(ctx, 10)
		must(t, err)
		if len(batch) != 3 || string(batch[0].Message.Payload) != "first" || string(batch[2].Message.Payload) != "third" {
			t.Fatalf("Receive(10) = %d deliveries, want the three in publication order", len(batch))
		}
		due := fx.broker.(queue.Waker).Wake()
		if !due.Scheduled || due.In != 10*time.Second {
			t.Fatalf("after a partial batch, Wake = %+v, want the leases' deadline in 10s", due)
		}
		clk.Advance(10 * time.Second)
		leaseNone(t, fx.broker) // the read that finds the three lapsed leases buries them
		dead, err := fx.broker.(queue.DeadLetterReader).DeadLetters(ctx, 10)
		must(t, err)
		if len(dead) != 3 {
			t.Fatalf("DeadLetters() returned %d, want the three lapsed leases", len(dead))
		}
		for _, letter := range dead {
			if letter.Reason != "LEASE_EXPIRED" || letter.Deliveries != 1 {
				t.Fatalf("dead letter = %q after %d deliveries, want LEASE_EXPIRED after 1", letter.Reason, letter.Deliveries)
			}
		}

		retrying := openQueue(t, e, queue.SQLConfig{Clock: clk, Policy: queuePolicy()})
		for _, payload := range []string{"first", "second", "third"} {
			clk.Advance(time.Millisecond)
			_, err := retrying.broker.Publish(ctx, []byte(payload))
			must(t, err)
		}
		failed := leaseOne(t, retrying.broker)
		_, err = retrying.broker.Nack(ctx, failed.Lease.Receipt, nil)
		must(t, err)
		clk.Advance(time.Second)
		var order []string
		for range 3 {
			delivery := leaseOne(t, retrying.broker)
			order = append(order, string(delivery.Message.Payload))
			must(t, retrying.broker.Ack(ctx, delivery.Lease.Receipt))
		}
		if strings.Join(order, ",") != "second,third,first" {
			t.Fatalf("drained %v, want second, third, first — a single retry steps aside", order)
		}
	})
}

// TestACommittedPublicationWakesAnIdleConsumerOnEveryEngine pins the wake on
// real engines: a consumer asleep on an hour's poll, a clock that never moves,
// and a publication inside the caller's transaction — delivered once, and only
// once, that transaction commits.
func TestACommittedPublicationWakesAnIdleConsumerOnEveryEngine(t *testing.T) {
	t.Parallel()
	eachEngine(t, func(t *testing.T, e *engine) {
		clk := clock.NewManualClock(queueEpoch)
		fx := openQueue(t, e, queue.SQLConfig{Clock: clk, Policy: queuePolicy()})
		handled := make(chan string, 1)
		ctx, cancel := context.WithCancel(t.Context())
		var wg sync.WaitGroup
		wg.Go(func() {
			consumeErr := queue.Consume(ctx, fx.broker, queue.ConsumerConfig{
				Handler: func(_ context.Context, d queue.Delivery) error {
					handled <- string(d.Message.Payload)
					return nil
				},
				Clock: clk, HandlerIsIdempotent: true, PollInterval: time.Hour,
			})
			if consumeErr != nil {
				t.Errorf("Consume() = %v", consumeErr)
			}
		})
		defer func() { cancel(); wg.Wait() }()
		clk.BlockUntil(1) // the consumer found nothing and sleeps on its hour
		must(t, sql.Transact(t.Context(), fx.tm, func(ctx context.Context, _ sql.Executor) error {
			_, err := fx.broker.Publish(ctx, []byte("after the commit"))
			return err
		}))
		select {
		case got := <-handled:
			if got != "after the commit" {
				t.Fatalf("handled %q", got)
			}
		case <-time.After(30 * time.Second):
			t.Fatal("the committed publication did not wake the idle consumer")
		}
	})
}

// TestADatabaseFailureOfTheQueueWithholdsTheDriversText pins
// QUEUE_BACKEND_FAILED on a real driver: a table that was never created fails
// the probe, the driver's error is reachable through errors.As, and its words —
// which name the relation — are in no rendering.
func TestADatabaseFailureOfTheQueueWithholdsTheDriversText(t *testing.T) {
	t.Parallel()
	eachEngine(t, func(t *testing.T, e *engine) {
		table := uniqueName("missing__")
		broker, err := queue.NewSQL(queue.SQLConfig{Transactor: transactor(t, e), Dialect: e.dialect, Table: table, Policy: queuePolicy()})
		must(t, err)
		_, err = broker.Receive(t.Context(), 1)
		if !errors.Is(err, queue.QueueBackendFailed) {
			t.Fatalf("Receive() on a missing table = %v, want QUEUE_BACKEND_FAILED", err)
		}
		if rendered := err.Error(); strings.Contains(rendered, table) {
			t.Fatalf("the error renders the driver's words: %s", rendered)
		}
	})
}
