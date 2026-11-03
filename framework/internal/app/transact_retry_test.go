package app_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	app "github.com/kitsunium/sdk/framework/internal/app"
	"github.com/kitsunium/sdk/pkg/v1/data/sql"
)

// serializationFailure is a driver's error naming SQLSTATE 40001 through a
// method, as pgx's does: what the SDK's Classify reads.
type serializationFailure struct{}

func (serializationFailure) Error() string {
	return "ERROR: could not serialize access (SQLSTATE 40001)"
}
func (serializationFailure) SQLState() string { return "40001" }

// TestATransientCommitIsRetried is #1000 on the fake database: a COMMIT the
// database refuses with a serialization failure runs the whole transaction
// again — the function from its start, in a new transaction — and the second
// attempt commits; with <name>-tx-retries at 0 the failure is the outcome,
// and a failure that is not transient is never run again.
func TestATransientCommitIsRetried(t *testing.T) {
	//: a data directory: where the SDK's file store refuses the platform, skip after asserting it.
	needsFileStore(t)
	db := app.NewFakeDB(sql.DialectPostgres)
	t.Setenv("LEDGER_DATABASE_URL", verifiedURL())
	t.Setenv("LEDGER_DATABASE_TX_RETRY_DELAY", "1ms")
	var logs syncBuffer
	l := newLedger()
	a := ledgerApp(t, l, &logs, app.Database("database", db.Engine()))
	run(t, a)
	ctx := t.Context()
	var runs atomic.Int32
	db.FailCommits(serializationFailure{})
	err := app.Transact(ctx, func(ctx context.Context) error {
		runs.Add(1)
		return l.entries.Insert(ctx, entry{ID: "retried"})
	})
	if err != nil {
		t.Fatalf("Transact = %v, want the second attempt's commit", err)
	}
	if got := runs.Load(); got != 2 {
		t.Fatalf("the function ran %d times, want 2", got)
	}
	if _, err := l.entries.Get(ctx, "retried"); err != nil {
		t.Fatalf("the retried write: %v", err)
	}
	runs.Store(0)
	err = app.Transact(ctx, func(ctx context.Context) error {
		runs.Add(1)
		return errors.Join(l.entries.Insert(ctx, entry{ID: "refused"}), errors.ErrUnsupported)
	})
	if !errors.Is(err, errors.ErrUnsupported) || runs.Load() != 1 {
		t.Fatalf("a failure that is not transient: %v after %d runs, want one run", err, runs.Load())
	}
}

// TestNoRetryWhenTheSettingSaysNone pins <name>-tx-retries at 0: the
// transient failure is the outcome, after one run.
func TestNoRetryWhenTheSettingSaysNone(t *testing.T) {
	//: a data directory: where the SDK's file store refuses the platform, skip after asserting it.
	needsFileStore(t)
	db := app.NewFakeDB(sql.DialectPostgres)
	t.Setenv("LEDGER_DATABASE_URL", verifiedURL())
	t.Setenv("LEDGER_DATABASE_TX_RETRIES", "0")
	var logs syncBuffer
	l := newLedger()
	a := ledgerApp(t, l, &logs, app.Database("database", db.Engine()))
	run(t, a)
	ctx := t.Context()
	var runs atomic.Int32
	db.FailCommits(serializationFailure{})
	err := app.Transact(ctx, func(ctx context.Context) error {
		runs.Add(1)
		return l.entries.Insert(ctx, entry{ID: "once"})
	})
	if err == nil || runs.Load() != 1 {
		t.Fatalf("Transact = %v after %d runs, want the failure after one", err, runs.Load())
	}
	if !errors.As(err, new(serializationFailure)) {
		t.Fatalf("Transact = %v, want the driver's failure kept", err)
	}
}

// retriedLedger runs the ledger on a fake database, its retries a
// millisecond apart, with svc beside it and opts after its own.
func retriedLedger(t *testing.T, db *app.FakeDB, svc *app.Service, opts ...app.AppConfigurer) (ledger, *app.App) {
	t.Helper()
	//: a data directory: where the SDK's file store refuses the platform, skip after asserting it.
	needsFileStore(t)
	t.Setenv("LEDGER_DATABASE_URL", verifiedURL())
	t.Setenv("LEDGER_DATABASE_TX_RETRY_DELAY", "1ms")
	t.Setenv("LEDGER_SECRETS", "memory")
	l := newLedger()
	a := app.NewApp("ledger", l.books, l.audit, svc).With(append([]app.AppConfigurer{
		app.Listen("127.0.0.1:0"), app.Env(app.EnvProduction), app.DataDir(t.TempDir()), app.Logs(&syncBuffer{}),
		app.Database("database", db.Engine()),
	}, opts...)...)
	run(t, a)
	return l, a
}

// TestAHeldPublishOfARetriedTransactionIsReleasedOnce is #1000's held
// effect: a publication inside a transaction whose first commit the
// database refuses as transient is dropped with that attempt, published
// again by the second, and delivered once.
func TestAHeldPublishOfARetriedTransactionIsReleasedOnce(t *testing.T) {
	db := app.NewFakeDB(sql.DialectPostgres)
	news := app.NewService("news", "What the ledger announces.")
	posted := news.Topic[entry]("posted")
	var delivered atomic.Int32
	news.Subscribe("count", posted, func(context.Context, entry) error { delivered.Add(1); return nil })
	l, _ := retriedLedger(t, db, news)
	ctx := t.Context()
	var runs atomic.Int32
	db.FailCommits(serializationFailure{})
	err := app.Transact(ctx, func(ctx context.Context) error {
		runs.Add(1)
		if err := posted.Publish(ctx, entry{ID: "once"}); err != nil {
			return err
		}
		return l.entries.Insert(ctx, entry{ID: "once"})
	})
	if err != nil || runs.Load() != 2 {
		t.Fatalf("Transact = %v after %d runs, want the second run's commit", err, runs.Load())
	}
	for deadline := time.Now().Add(5 * time.Second); delivered.Load() == 0 && time.Now().Before(deadline); {
		time.Sleep(10 * time.Millisecond)
	}
	time.Sleep(100 * time.Millisecond)
	if n := delivered.Load(); n != 1 {
		t.Fatalf("the held publication was delivered %d times, want once", n)
	}
}

// TestATransactionAGoBodyOpensRunsOnce pins #1000's exclusion: a
// transaction a Service.Go body opens is never run again — the framework
// cannot know the body may run its function twice —, so a transient
// failure is its outcome after one run.
func TestATransactionAGoBodyOpensRunsOnce(t *testing.T) {
	db := app.NewFakeDB(sql.DialectPostgres)
	var l ledger
	var runs atomic.Int32
	start, done := make(chan struct{}), make(chan error, 1)
	worker := app.NewService("worker", "A routine that writes the ledger.")
	worker.Go("write", func(ctx context.Context) error {
		select {
		case <-start:
		case <-ctx.Done():
			return nil
		}
		done <- app.Transact(ctx, func(ctx context.Context) error {
			runs.Add(1)
			return l.entries.Insert(ctx, entry{ID: "routine"})
		})
		<-ctx.Done()
		return nil
	})
	l, _ = retriedLedger(t, db, worker)
	db.FailCommits(serializationFailure{})
	close(start)
	select {
	case err := <-done:
		if !errors.As(err, new(serializationFailure)) || runs.Load() != 1 {
			t.Fatalf("a Go body's transaction = %v after %d runs, want the failure after one", err, runs.Load())
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the routine's transaction never ended")
	}
}
