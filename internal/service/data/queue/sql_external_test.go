package queue_test

import (
	"context"
	"errors"
	"math"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/kitsunium/sdk/internal/kernel/clock"
	"github.com/kitsunium/sdk/internal/kernel/errs"

	corequeue "github.com/kitsunium/sdk/internal/core/data/queue"
	coresql "github.com/kitsunium/sdk/internal/core/data/sql"
	svcqueue "github.com/kitsunium/sdk/internal/service/data/queue"
)

// sqlDialects are the three engines every SQL case runs on.
var sqlDialects = []coresql.Dialect{coresql.DialectPostgres, coresql.DialectMySQL, coresql.DialectSQLite}

// eachDialect runs fn once per dialect, in parallel, over a fresh fixture.
func eachDialect(t *testing.T, fn func(t *testing.T, fx *sqlFixture, clk *clock.ManualClock)) {
	t.Helper()
	for _, dialect := range sqlDialects {
		t.Run(dialect.String(), func(t *testing.T) {
			t.Parallel()
			clk := clock.NewManualClock(epoch)
			fn(t, newSQLFixture(t, dialect, clk, defaultPolicy()), clk)
		})
	}
}

// inTransaction runs fn inside a transaction of fx's transactor, and returns
// what the transaction returned.
func inTransaction(t *testing.T, fx *sqlFixture, fn func(ctx context.Context) error) error {
	t.Helper()
	return fx.tm.Transact(t.Context(), coresql.TxOptionsValue{}, func(ctx context.Context, _ coresql.Executor) error {
		return fn(ctx)
	})
}

// TestSQLAPublicationExistsIfAndOnlyIfItsTransactionCommits is the reason the
// SQL broker exists (ADR 0151): published inside the caller's transaction, the
// message is invisible to every consumer until that transaction commits, and a
// rollback leaves no message at all — the transactional outbox. Its consumers
// are woken once it has committed, and not by a publication rolled back.
func TestSQLAPublicationExistsIfAndOnlyIfItsTransactionCommits(t *testing.T) {
	t.Parallel()
	eachDialect(t, func(t *testing.T, fx *sqlFixture, _ *clock.ManualClock) {
		waker, _ := fx.broker.(corequeue.Waker)
		before := waker.Wake().Signal
		errRolledBack := errors.New("the business write failed")
		err := inTransaction(t, fx, func(ctx context.Context) error {
			if _, pubErr := fx.broker.Publish(ctx, []byte("rolled back")); pubErr != nil {
				t.Fatalf("Publish() inside the transaction = %v", pubErr)
			}
			return errRolledBack
		})
		if !errors.Is(err, errRolledBack) {
			t.Fatalf("Transact() = %v, want the function's own error", err)
		}
		receiveNone(t, fx.broker)
		if fx.engine.rows() != 0 {
			t.Fatalf("a rolled-back publication left %d rows", fx.engine.rows())
		}
		assertOpen(t, before, "a rolled-back Publish")

		var published corequeue.MessageValue
		err = inTransaction(t, fx, func(ctx context.Context) error {
			var pubErr error
			published, pubErr = fx.broker.Publish(ctx, []byte("committed"))
			if pubErr != nil {
				return pubErr
			}
			//: another consumer, outside the transaction, sees nothing yet.
			receiveNone(t, fx.broker)
			assertOpen(t, before, "a Publish whose transaction has not committed")
			return nil
		})
		if err != nil {
			t.Fatalf("Transact() = %v, want nil", err)
		}
		assertClosed(t, before, "the commit of a Publish")
		if got := receiveOne(t, fx.broker); got.Message.ID != published.ID {
			t.Fatalf("received %q, want the committed %q", got.Message.ID, published.ID)
		}
	})
}

// assertOpen fails when signal has been closed.
func assertOpen(t *testing.T, signal <-chan struct{}, by string) {
	t.Helper()
	select {
	case <-signal:
		t.Errorf("%s closed the wake signal", by)
	default:
	}
}

// TestSQLAFailedPublicationLeavesTheCallersTransactionUsable pins the
// savepoint around a joined publication: a statement the database fails —
// on PostgreSQL one that leaves the transaction aborted — is undone alone, and
// a caller that catches it publishes again and commits.
func TestSQLAFailedPublicationLeavesTheCallersTransactionUsable(t *testing.T) {
	t.Parallel()
	eachDialect(t, func(t *testing.T, fx *sqlFixture, _ *clock.ManualClock) {
		fx.engine.failNext("insert", errors.New("the statement was cancelled"))
		err := inTransaction(t, fx, func(ctx context.Context) error {
			if _, failed := fx.broker.Publish(ctx, []byte("failed")); !errs.HasCode(failed, corequeue.CodeQueueBackendFailed) {
				t.Fatalf("the failed Publish = %v, want CodeQueueBackendFailed", failed)
			}
			_, pubErr := fx.broker.Publish(ctx, []byte("kept"))
			return pubErr
		})
		if err != nil {
			t.Fatalf("Transact() = %v, want nil — the caller caught the failure", err)
		}
		if got := receiveOne(t, fx.broker); string(got.Message.Payload) != "kept" {
			t.Fatalf("received %q, want only the publication that succeeded", got.Message.Payload)
		}
		receiveNone(t, fx.broker)
	})
}

// TestSQLAStorageFailureWithholdsTheDriversText pins QUEUE_BACKEND_FAILED on
// every call that reaches the database: the verdict names the table and the
// step, the driver's own error stays reachable through errors.As, and its
// text — which quotes the rows a statement touched — is in no rendering.
func TestSQLAStorageFailureWithholdsTheDriversText(t *testing.T) {
	t.Parallel()
	eachDialect(t, func(t *testing.T, fx *sqlFixture, _ *clock.ManualClock) {
		secret := "SECRET-PAYLOAD-BYTES"
		for role, call := range map[string]func() error{
			"insert":   func() error { _, err := fx.broker.Publish(t.Context(), []byte("m")); return err },
			"probe":    func() error { _, err := fx.broker.Receive(t.Context(), 1); return err },
			"deadList": func() error { _, err := deadLetterReader(t, fx.broker).DeadLetters(t.Context(), 1); return err },
			"replay":   func() error { return manager(t, fx.broker).ReplayDeadLetter(t.Context(), "id") },
		} {
			fx.engine.failNext(role, errDuplicate{key: secret})
			err := call()
			if !errs.HasCode(err, corequeue.CodeQueueBackendFailed) {
				t.Fatalf("%s: %v, want CodeQueueBackendFailed", role, err)
			}
			rendered := err.Error()
			for _, field := range errs.FieldsOf(err) {
				rendered += " " + field.StringValue()
			}
			if strings.Contains(rendered, secret) {
				t.Fatalf("%s: the error renders the driver's text: %s", role, rendered)
			}
			if dup, reachable := errors.AsType[errDuplicate](err); !reachable || dup.key != secret {
				t.Fatalf("%s: the driver's own error is not reachable through errors.As", role)
			}
		}
	})
}

// deadLetterReader asserts the DeadLetterReader capability and returns it.
func deadLetterReader(t *testing.T, broker corequeue.Broker) corequeue.DeadLetterReader {
	t.Helper()
	reader, ok := broker.(corequeue.DeadLetterReader)
	if !ok {
		t.Fatalf("%T does not implement queue.DeadLetterReader", broker)
	}
	return reader
}

// TestSQLStatementsPerCall pins what each call sends, which is what it costs:
// ONE read for an idle Receive and none of SQLite's write lock; a transaction
// of the broker's own — READ COMMITTED on MySQL, the write lock taken first on
// SQLite — for a Receive that leases, which asks when the next message is due
// only when its batch is not full; one statement for every call that ends or
// renews a lease; a savepoint of the caller's for a publication inside it.
func TestSQLStatementsPerCall(t *testing.T) {
	t.Parallel()
	eachDialect(t, func(t *testing.T, fx *sqlFixture, _ *clock.ManualClock) {
		begin, lock := "BEGIN", []string(nil)
		switch fx.dialect() {
		case coresql.DialectMySQL:
			begin = "BEGIN READ COMMITTED"
		case coresql.DialectSQLite:
			lock = []string{"lock"}
		default:
		}
		expect := func(what string, want []string, call func()) {
			t.Helper()
			fx.engine.resetLog()
			call()
			if got := fx.engine.roles(); !slices.Equal(got, want) {
				t.Fatalf("%s sent %v, want %v", what, got, want)
			}
		}
		expect("an idle Receive", []string{"probe"}, func() { receiveNone(t, fx.broker) })
		expect("a Publish", []string{"insert"}, func() { publish(t, fx.broker, "m") })
		var delivery corequeue.DeliveryValue
		expect("a Receive that fills its batch", slices.Concat([]string{"probe", begin}, lock, []string{"pick", "leaseRows", "COMMIT"}),
			func() { delivery = receiveOne(t, fx.broker) })
		expect("a Nack", []string{"retry"}, func() { nack(t, fx.broker, delivery.Lease.Receipt, nil) })
		publish(t, fx.broker, "m")
		//: a batch that is not full asks when the next message is due.
		expect("a Receive that does not fill its batch", slices.Concat([]string{"probe", begin}, lock, []string{"pick", "leaseRows", "next", "COMMIT"}),
			func() { delivery = receive(t, fx.broker, 10)[0] })
		expect("an Ack", []string{"ack"}, func() {
			if err := fx.broker.Ack(t.Context(), delivery.Lease.Receipt); err != nil {
				t.Fatalf("Ack() = %v", err)
			}
		})
		expect("a Publish inside the caller's transaction", []string{"BEGIN", "SAVEPOINT ktn_sp_1", "insert", "RELEASE SAVEPOINT ktn_sp_1", "COMMIT"},
			func() {
				if err := inTransaction(t, fx, func(ctx context.Context) error {
					_, err := fx.broker.Publish(ctx, []byte("m"))
					return err
				}); err != nil {
					t.Fatalf("Transact() = %v", err)
				}
			})
	})
}

// dialect returns the fixture's engine dialect.
func (fx *sqlFixture) dialect() coresql.Dialect {
	return fx.engine.dialect
}

// TestSQLALargeBatchIsLeasedInBoundedStatements pins that a batch larger than
// one statement may name — 600 messages, 500 identifiers a statement — is
// leased in one transaction and two statements, under SQLite's 999 bound
// parameters, and handed out oldest first.
func TestSQLALargeBatchIsLeasedInBoundedStatements(t *testing.T) {
	t.Parallel()
	const count int = 600
	clk := clock.NewManualClock(epoch)
	fx := newSQLFixture(t, coresql.DialectSQLite, clk, defaultPolicy())
	for range count {
		clk.Advance(1)
		publish(t, fx.broker, "m")
	}
	fx.engine.resetLog()
	batch := receive(t, fx.broker, count)
	if len(batch) != count {
		t.Fatalf("Receive(%d) returned %d", count, len(batch))
	}
	for index := 1; index < count; index++ {
		if !batch[index-1].Message.EnqueuedAt.Before(batch[index].Message.EnqueuedAt) {
			t.Fatalf("delivery %d was published before delivery %d; want oldest first", index, index-1)
		}
	}
	want := []string{"probe", "BEGIN", "lock", "pick", "leaseRows", "leaseRows", "COMMIT"}
	if got := fx.engine.roles(); !slices.Equal(got, want) {
		t.Fatalf("a batch of %d sent %v, want %v", count, got, want)
	}
}

// TestSQLAReceiveInsideTheCallersTransactionStandsOrFallsWithIt pins that a
// lease is a write like any other: taken inside the caller's transaction, a
// rollback gives the message back as if it had never been delivered.
func TestSQLAReceiveInsideTheCallersTransactionStandsOrFallsWithIt(t *testing.T) {
	t.Parallel()
	eachDialect(t, func(t *testing.T, fx *sqlFixture, _ *clock.ManualClock) {
		publish(t, fx.broker, "m")
		errRolledBack := errors.New("the handler's write failed")
		err := inTransaction(t, fx, func(ctx context.Context) error {
			batch, receiveErr := fx.broker.Receive(ctx, 1)
			if receiveErr != nil || len(batch) != 1 {
				t.Fatalf("Receive() inside the transaction = %d, %v", len(batch), receiveErr)
			}
			return errRolledBack
		})
		if !errors.Is(err, errRolledBack) {
			t.Fatalf("Transact() = %v", err)
		}
		if again := receiveOne(t, fx.broker); again.Deliveries != 1 {
			t.Fatalf("after the rollback the message counted %d, want 1 — the lease never happened", again.Deliveries)
		}
	})
}

// TestSQLTwoBrokersOverOneTableAreOneQueue pins the inter-process claim within
// one process: a receipt minted by one broker is acknowledged through another
// over the same database and table, and a publication through either wakes
// the consumers of both — the wake follows the queue, not the value.
func TestSQLTwoBrokersOverOneTableAreOneQueue(t *testing.T) {
	t.Parallel()
	eachDialect(t, func(t *testing.T, fx *sqlFixture, clk *clock.ManualClock) {
		other, err := svcqueue.NewSQL(svcqueue.SQLConfig{
			Transactor: fx.tm, Dialect: fx.dialect(), Table: jobsTable, Policy: defaultPolicy(), Clock: clk,
		})
		if err != nil {
			t.Fatalf("NewSQL() = %v", err)
		}
		before := other.(corequeue.Waker).Wake().Signal
		publish(t, fx.broker, "through one")
		assertClosed(t, before, "a Publish through the other broker")
		delivery := receiveOne(t, other)
		if ackErr := fx.broker.Ack(t.Context(), delivery.Lease.Receipt); ackErr != nil {
			t.Fatalf("Ack() of a receipt the other broker minted = %v, want nil", ackErr)
		}
		receiveNone(t, other)
	})
}

// TestSQLAReceiptOfAnotherGrammarIsUnknown pins the SQL broker's receipt
// grammar: a file broker's in-flight name, a memory broker's receipt and a
// receipt whose count was edited to zero are not receipts this broker writes.
func TestSQLAReceiptOfAnotherGrammarIsUnknown(t *testing.T) {
	t.Parallel()
	eachDialect(t, func(t *testing.T, fx *sqlFixture, _ *clock.ManualClock) {
		publish(t, fx.broker, "m")
		held := receiveOne(t, fx.broker).Lease.Receipt
		id, _, _ := strings.Cut(string(held), ".")
		for _, forged := range []corequeue.ReceiptValue{
			"1757505600000000000.1757505600000000000.0123456789abcdef.001.01234567.msg",
			"6e1c2f9a8b7d3c4e-1",
			corequeue.ReceiptValue(id + ".000." + strings.Repeat("a", 16)),
			corequeue.ReceiptValue(id + ".001." + strings.Repeat("A", 16)),
		} {
			if err := fx.broker.Ack(t.Context(), forged); !errs.HasCode(err, corequeue.CodeUnknownReceipt) {
				t.Errorf("Ack(%q) = %v, want CodeUnknownReceipt", forged, err)
			}
		}
		//: a well-formed receipt whose lease is not the one held lost it.
		stranger := corequeue.ReceiptValue(id + ".001." + strings.Repeat("b", 16))
		if err := fx.broker.Ack(t.Context(), stranger); !errs.HasCode(err, corequeue.CodeLeaseExpired) {
			t.Errorf("Ack(another lease) = %v, want CodeLeaseExpired", err)
		}
		if err := fx.broker.Ack(t.Context(), held); err != nil {
			t.Fatalf("Ack(held) = %v, want nil — the forgeries changed nothing", err)
		}
	})
}

// TestSQLConfigurationRefusals pins what NewSQL refuses before it builds a
// broker, naming the setting and the problem: no transactor, one that cannot
// join or defer, a dialect it cannot spell, and every table name it cannot
// interpolate safely — and a policy, through the shared guard.
func TestSQLConfigurationRefusals(t *testing.T) {
	t.Parallel()
	tm := sqlTransactor(t, coresql.DialectPostgres)
	postgres, policy := coresql.DialectPostgres, defaultPolicy()
	table := func(name string) svcqueue.SQLConfig {
		return svcqueue.SQLConfig{Transactor: tm, Dialect: postgres, Table: name, Policy: policy}
	}
	for name, tc := range map[string]struct {
		cfg     svcqueue.SQLConfig
		setting string
	}{
		"no transactor":        {svcqueue.SQLConfig{Dialect: postgres, Table: jobsTable, Policy: policy}, "Transactor"},
		"a bare transactor":    {svcqueue.SQLConfig{Transactor: bareTransactor{}, Dialect: postgres, Table: jobsTable, Policy: policy}, "Transactor"},
		"no dialect":           {svcqueue.SQLConfig{Transactor: tm, Table: jobsTable, Policy: policy}, "Dialect"},
		"an upper-case table":  {table("Jobs"), "Table"},
		"an injected table":    {table("jobs; DROP TABLE x"), "Table"},
		"a derived-like table": {table("members___ix"), "Table"},
		"a reserved table":     {table("sqlite_jobs"), "Table"},
		"a table too long":     {table(strings.Repeat("j", svcqueue.MaxSQLTableLen+1)), "Table"},
		"an empty table":       {table(""), "Table"},
	} {
		_, err := svcqueue.NewSQL(tc.cfg)
		if !errs.HasCode(err, corequeue.CodeSQLQueueMisconfigured) {
			t.Errorf("%s: NewSQL() = %v, want CodeSQLQueueMisconfigured", name, err)
			continue
		}
		if got := fieldValue(err, "setting"); got != tc.setting {
			t.Errorf("%s: setting = %q, want %q", name, got, tc.setting)
		}
	}
	if _, err := svcqueue.NewSQL(table(strings.Repeat("j", svcqueue.MaxSQLTableLen))); err != nil {
		t.Errorf("NewSQL(a table of exactly MaxSQLTableLen) = %v, want nil", err)
	}
	if _, err := svcqueue.NewSQL(svcqueue.SQLConfig{Transactor: tm, Dialect: postgres, Table: jobsTable}); !errs.HasCode(
		err, corequeue.CodeQueueMisconfigured) {
		t.Errorf("NewSQL(zero policy) = %v, want CodeQueueMisconfigured", err)
	}
}

// bareTransactor is a Transactor that is neither a Joiner nor a Deferrer.
type bareTransactor struct{}

// Transact runs fn with no transaction at all.
func (bareTransactor) Transact(ctx context.Context, _ coresql.TxOptionsValue, fn coresql.TxFunc) error {
	return fn(ctx, nil)
}

// TestSQLAnEmptyAndANilPayloadReadBackEmpty pins the payload column's NULL:
// a nil payload binds as NULL and an empty one as empty, and both are
// delivered as a message with no bytes.
func TestSQLAnEmptyAndANilPayloadReadBackEmpty(t *testing.T) {
	t.Parallel()
	eachDialect(t, func(t *testing.T, fx *sqlFixture, clk *clock.ManualClock) {
		for _, payload := range [][]byte{nil, {}} {
			if _, err := fx.broker.Publish(t.Context(), payload); err != nil {
				t.Fatalf("Publish(%v) = %v", payload, err)
			}
			clk.Advance(time.Millisecond)
			if got := receiveOne(t, fx.broker); len(got.Message.Payload) != 0 {
				t.Fatalf("payload = %q, want empty", got.Message.Payload)
			}
		}
	})
}

// TestSQLAMessageAtTheUnixEpochIsReceivable pins that the probe tells an empty
// queue — MIN(due) is NULL — from a message due at instant zero: both used to
// read as zero, and a message published at 1970-01-01T00:00:00Z was never
// leased, however far the clock then moved.
func TestSQLAMessageAtTheUnixEpochIsReceivable(t *testing.T) {
	t.Parallel()
	for _, dialect := range sqlDialects {
		t.Run(dialect.String(), func(t *testing.T) {
			t.Parallel()
			clk := clock.NewManualClock(time.Unix(0, 0))
			fx := newSQLFixture(t, dialect, clk, defaultPolicy())
			publish(t, fx.broker, "at the epoch")
			if got := receiveOne(t, fx.broker); string(got.Message.Payload) != "at the epoch" {
				t.Fatalf("received %q", got.Message.Payload)
			}
		})
	}
}

// TestSQLAClockItCannotWriteDownIsRefused pins the instants the table can
// hold: 64-bit Unix nanoseconds, 1970 to 2262. A clock before 1970 would mint
// an identifier its own receipts are refused under, and one so late that a
// lease's deadline passes 2262 would wrap it negative and hand the message to
// the next consumer at once — so both are refused before any statement.
func TestSQLAClockItCannotWriteDownIsRefused(t *testing.T) {
	t.Parallel()
	last := time.Unix(0, math.MaxInt64)
	for name, tc := range map[string]struct {
		at   time.Time
		call func(corequeue.Broker) error
	}{
		"a Publish before 1970": {time.Unix(0, -1), func(b corequeue.Broker) error {
			_, err := b.Publish(t.Context(), []byte("m"))
			return err
		}},
		"a Receive whose lease would pass 2262": {last.Add(-testVisibility / 2), func(b corequeue.Broker) error {
			_, err := b.Receive(t.Context(), 1)
			return err
		}},
	} {
		fx := newSQLFixture(t, coresql.DialectPostgres, clock.NewManualClock(tc.at), defaultPolicy())
		fx.engine.resetLog()
		err := tc.call(fx.broker)
		if !errs.HasCode(err, corequeue.CodeQueueMisconfigured) || fieldValue(err, "field") != "Clock" {
			t.Errorf("%s = %v, want QueueMisconfigured naming the clock", name, err)
		}
		if sent := fx.engine.roles(); len(sent) != 0 {
			t.Errorf("%s sent %v; a refused call sends nothing", name, sent)
		}
	}
}
