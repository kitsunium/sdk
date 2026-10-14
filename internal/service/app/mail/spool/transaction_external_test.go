// Package spool_test — a mail queued in its caller's transaction (ADR 0178):
// it exists if and only if the transaction commits, its EventQueued is told
// after the commit and before any event of its delivery, and a rollback tells
// nobody and leaves its identifier free.
//
// # The stack under test
//
// The transaction manager is the SDK's own (internal/service/data/sql), over
// a driver that answers every statement — this module ships none (ADR 0055
// §D2), and the outcome of a transaction is the manager's, not the engine's.
// The queue is txBroker: a memory broker whose publication, made inside a
// transaction, is held with that manager's Deferrer until the commit and
// dropped by a rollback — what queue.NewSQL's row does, without its SQL — and
// whose wake fires at the commit BEFORE the spool's announcement, as the SQL
// broker's does (ADR 0151 §D2). That order is the one the spool must survive:
// the consumer can lease the mail before the observer heard it was queued.
package spool_test

import (
	"context"
	stdsql "database/sql"
	"database/sql/driver"
	"errors"
	"testing"
	"time"

	coremail "github.com/kitsunium/sdk/internal/core/app/mail"
	corespool "github.com/kitsunium/sdk/internal/core/app/mail/spool"
	corequeue "github.com/kitsunium/sdk/internal/core/data/queue"
	coresql "github.com/kitsunium/sdk/internal/core/data/sql"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	svcmail "github.com/kitsunium/sdk/internal/service/app/mail"
	"github.com/kitsunium/sdk/internal/service/app/mail/spool"
	svcqueue "github.com/kitsunium/sdk/internal/service/data/queue"
	svcsql "github.com/kitsunium/sdk/internal/service/data/sql"
)

// errRolledBack is the failure a case's unit of work returns to roll back.
var errRolledBack = errors.New("the unit of work failed")

// errNotPrepared answers a statement the transaction manager never prepares.
var errNotPrepared = errors.New("the test driver prepares nothing")

// acceptingConnector opens connections that accept every statement and every
// transaction: the manager's BEGIN, SAVEPOINT, RELEASE, ROLLBACK TO and
// COMMIT all succeed, so each outcome is the unit of work's.
type acceptingConnector struct{}

// Connect returns a connection.
func (acceptingConnector) Connect(context.Context) (driver.Conn, error) { return acceptingConn{}, nil }

// Driver returns the driver, which opens the same connection.
func (acceptingConnector) Driver() driver.Driver { return acceptingDriver{} }

// acceptingDriver is the connector's driver.
type acceptingDriver struct{}

// Open returns a connection.
func (acceptingDriver) Open(string) (driver.Conn, error) { return acceptingConn{}, nil }

// acceptingConn accepts everything.
type acceptingConn struct{}

// Prepare is never reached: the connection executes directly.
func (acceptingConn) Prepare(string) (driver.Stmt, error) { return nil, errNotPrepared }

// Close releases nothing.
func (acceptingConn) Close() error { return nil }

// Begin opens a transaction that commits and rolls back.
func (acceptingConn) Begin() (driver.Tx, error) { return acceptingTx{}, nil }

// ExecContext accepts the statement.
func (acceptingConn) ExecContext(context.Context, string, []driver.NamedValue) (driver.Result, error) {
	return driver.RowsAffected(0), nil
}

// acceptingTx commits and rolls back.
type acceptingTx struct{}

// Commit succeeds.
func (acceptingTx) Commit() error { return nil }

// Rollback succeeds.
func (acceptingTx) Rollback() error { return nil }

// txBroker is a memory broker whose publication joins the caller's
// transaction: held until the commit, dropped by a rollback.
type txBroker struct {
	corequeue.Broker
	// deferrer holds a publication until the commit.
	deferrer coresql.Deferrer
}

// Publish publishes at once outside a transaction of the manager's, and at
// its commit inside one.
func (b *txBroker) Publish(ctx context.Context, payload []byte) (corequeue.MessageValue, error) {
	if b.deferrer.Defer(ctx, func() {
		if _, err := b.Broker.Publish(context.WithoutCancel(ctx), payload); err != nil {
			panic(err)
		}
	}) {
		return corequeue.MessageValue{}, nil
	}
	return b.Broker.Publish(ctx, payload)
}

// Wake forwards the memory broker's wake, so the consumer sleeps on it.
func (b *txBroker) Wake() corequeue.WakeValue { return b.Broker.(corequeue.Waker).Wake() }

// Extend forwards the memory broker's lease extension, which parks a retry.
func (b *txBroker) Extend(ctx context.Context, receipt corequeue.ReceiptValue, by time.Duration) (corequeue.LeaseValue, error) {
	return b.Broker.(corequeue.LeaseExtender).Extend(ctx, receipt, by)
}

// transactional builds the SDK's transaction manager over the accepting
// driver, and a txBroker over it.
func transactional(t *testing.T) (coresql.Transactor, *txBroker) {
	t.Helper()
	db := stdsql.OpenDB(acceptingConnector{})
	t.Cleanup(func() { _ = db.Close() })
	tm, err := svcsql.NewTransactor(svcsql.Config{DB: db, Dialect: coresql.DialectSQLite, Pool: svcsql.PoolConfig{MaxOpen: 4}})
	if err != nil {
		t.Fatalf("NewTransactor() = %v", err)
	}
	memory, err := svcqueue.NewMemory(svcqueue.MemoryConfig{Policy: corequeue.PolicyValue{
		VisibilityTimeout: 2 * time.Minute, RetryDelay: time.Second, MaxDeliveries: 3, MaxMessageBytes: 1 << 20,
	}})
	if err != nil {
		t.Fatalf("NewMemory() = %v", err)
	}
	return tm, &txBroker{Broker: memory, deferrer: tm.(coresql.Deferrer)}
}

// txSpool is a running spool over a txBroker, its manager, its observer and
// the mails its transport accepted.
type txSpool struct {
	spool     *spool.Spool
	tm        coresql.Transactor
	events    *recorder
	transport *scripted
}

// newTxSpool builds a spool whose mails join the manager's transactions.
func newTxSpool(t *testing.T) *txSpool {
	t.Helper()
	tm, broker := transactional(t)
	transport := &scripted{deliver: svcmail.NewCapture(0)}
	s, _, rec := newSpool(t, spool.Config{
		Transport: transport, Broker: broker, Deferrer: tm.(coresql.Deferrer),
		From: coremail.AddressValue{Addr: "app@example.com"},
	})
	return &txSpool{spool: s, tm: tm, events: rec, transport: transport}
}

// silent fails the case when the observer has heard anything yet.
func (x *txSpool) silent(t *testing.T, when string) {
	t.Helper()
	select {
	case event := <-x.events.events:
		t.Fatalf("%s, the observer heard %v of %s", when, event.Kind, event.ID)
	default:
	}
}

// delivered waits until every mail of want — identifier to subject — was
// sent, and fails the case on an event of any other mail, on a subject that
// is not the mail's, and on a mail sent before it was queued.
func (x *txSpool) delivered(t *testing.T, want map[string]string) {
	t.Helper()
	queued := map[string]bool{}
	for sent := 0; sent < len(want); {
		event := x.events.next(t)
		subject, expected := want[event.ID]
		if !expected || event.Message.Subject != subject {
			t.Fatalf("%v of %s %q, want only %v", event.Kind, event.ID, event.Message.Subject, want)
		}
		switch event.Kind {
		case spool.EventQueued:
			queued[event.ID] = true
		case spool.EventSent:
			if !queued[event.ID] {
				t.Fatalf("%s was sent before it was queued", event.ID)
			}
			sent++
		default:
			t.Fatalf("%v of %s, want queued then sent", event.Kind, event.ID)
		}
	}
}

// subjects returns the subject of every mail the transport accepted.
func (x *txSpool) subjects() []string {
	messages, _ := x.transport.seen()
	out := make([]string, 0, len(messages))
	for _, msg := range messages {
		out = append(out, msg.Subject)
	}
	return out
}

// TestAMailQueuedInACommittedTransactionIsDelivered is the ordinary path:
// nothing reaches the observer while the transaction is open, and after the
// commit the mail is queued, then sent — queued first although the commit
// wakes the consumer before the announcement runs.
func TestAMailQueuedInACommittedTransactionIsDelivered(t *testing.T) {
	t.Parallel()
	x := newTxSpool(t)
	err := x.tm.Transact(t.Context(), coresql.TxOptionsValue{}, func(ctx context.Context, _ coresql.Executor) error {
		if err := x.spool.SendWithID(ctx, "mail_committed", message("Committed")); err != nil {
			return err
		}
		x.silent(t, "before the commit")
		return nil
	})
	if err != nil {
		t.Fatalf("Transact() = %v", err)
	}
	x.delivered(t, map[string]string{"mail_committed": "Committed"})
	if got := x.subjects(); len(got) != 1 || got[0] != "Committed" {
		t.Fatalf("the transport accepted %v, want the committed mail once", got)
	}
}

// TestAMailQueuedInARolledBackTransactionNeverExists pins both ways a root
// transaction is undone — its unit of work fails, it panics — and, for each,
// that the mail is never delivered, the observer hears nothing of it, and its
// identifier, which a rollback leaves free, carries the next mail: before the
// Aborter, the registration a rollback left open made that delivery, and
// every one behind it, wait forever.
func TestAMailQueuedInARolledBackTransactionNeverExists(t *testing.T) {
	t.Parallel()
	for name, undo := range map[string]func() error{
		"the unit of work fails": func() error { return errRolledBack },
		"a panic":                func() error { panic("unit of work") },
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			x := newTxSpool(t)
			func() {
				defer func() { _ = recover() }()
				err := x.tm.Transact(t.Context(), coresql.TxOptionsValue{}, func(ctx context.Context, _ coresql.Executor) error {
					if err := x.spool.SendWithID(ctx, "mail_1", message("Taken back")); err != nil {
						t.Errorf("SendWithID() = %v", err)
					}
					return undo()
				})
				if !errors.Is(err, errRolledBack) {
					t.Errorf("Transact() = %v, want the unit of work's failure", err)
				}
			}()
			x.silent(t, "after the rollback")
			if err := x.spool.SendWithID(t.Context(), "mail_1", message("Sent")); err != nil {
				t.Fatalf("SendWithID() after the rollback = %v", err)
			}
			if err := x.spool.SendWithID(t.Context(), "mail_2", message("Behind it")); err != nil {
				t.Fatalf("SendWithID() = %v", err)
			}
			x.delivered(t, map[string]string{"mail_1": "Sent", "mail_2": "Behind it"})
			if got := x.subjects(); len(got) != 2 || got[0] != "Sent" || got[1] != "Behind it" {
				t.Fatalf("the transport accepted %v, want the two mails sent after the rollback and never the one it took back", got)
			}
		})
	}
}

// TestAMailInARolledBackSavepointIsUndoneAlone pins a Send inside a nested
// scope: its savepoint rolls back, its mail is taken back, and the mail the
// transaction around it queued is delivered once that commits.
func TestAMailInARolledBackSavepointIsUndoneAlone(t *testing.T) {
	t.Parallel()
	x := newTxSpool(t)
	err := x.tm.Transact(t.Context(), coresql.TxOptionsValue{}, func(ctx context.Context, _ coresql.Executor) error {
		caught := x.tm.Transact(ctx, coresql.TxOptionsValue{}, func(nested context.Context, _ coresql.Executor) error {
			if err := x.spool.SendWithID(nested, "mail_nested", message("Undone")); err != nil {
				return err
			}
			return errRolledBack
		})
		if !errors.Is(caught, errRolledBack) {
			t.Errorf("the savepoint's Transact() = %v, want its failure", caught)
		}
		return x.spool.SendWithID(ctx, "mail_outer", message("Kept"))
	})
	if err != nil {
		t.Fatalf("Transact() = %v", err)
	}
	x.delivered(t, map[string]string{"mail_outer": "Kept"})
	if got := x.subjects(); len(got) != 1 || got[0] != "Kept" {
		t.Fatalf("the transport accepted %v, want only the outer mail", got)
	}
	x.silent(t, "after the outer mail")
}

// TestWithoutABrokerASendInATransactionIsQueuedAtOnce pins that nothing
// changes for a spool over its own queue: a Send inside a transaction is in
// the spool when it returns, told at once, and delivered whatever the
// transaction does — the queue the spool builds joins no transaction.
func TestWithoutABrokerASendInATransactionIsQueuedAtOnce(t *testing.T) {
	t.Parallel()
	tm, _ := transactional(t)
	capture := svcmail.NewCapture(0)
	s, _, rec := newSpool(t, spool.Config{Transport: capture, From: coremail.AddressValue{Addr: "app@example.com"}})
	err := tm.Transact(t.Context(), coresql.TxOptionsValue{}, func(ctx context.Context, _ coresql.Executor) error {
		if err := s.SendWithID(ctx, "mail_own_queue", message("Own queue")); err != nil {
			return err
		}
		if event := rec.expect(t, spool.EventQueued); event.ID != "mail_own_queue" {
			t.Errorf("queued %s, want mail_own_queue", event.ID)
		}
		return errRolledBack
	})
	if !errors.Is(err, errRolledBack) {
		t.Fatalf("Transact() = %v, want the unit of work's failure", err)
	}
	if event := rec.expect(t, spool.EventSent); event.ID != "mail_own_queue" {
		t.Fatalf("sent %s, want mail_own_queue", event.ID)
	}
}

// aborterless is a Deferrer that is not an Aborter.
type aborterless struct{}

// Defer holds nothing.
func (aborterless) Defer(context.Context, func()) bool { return false }

// TestNewRefusesWhatABrokerMakesInert pins the four refusals a caller's
// queue adds: a directory or a bound beside it, a Deferrer without it, and a
// Deferrer that cannot take a mail's announcement back.
func TestNewRefusesWhatABrokerMakesInert(t *testing.T) {
	t.Parallel()
	tm, broker := transactional(t)
	transport := svcmail.NewCapture(0)
	for _, c := range []struct {
		name, setting string
		cfg           spool.Config
	}{
		{"a directory beside a Broker", "Dir", spool.Config{Broker: broker, Dir: t.TempDir()}},
		{"a bound beside a Broker", "MaxMessageBytes", spool.Config{Broker: broker, MaxMessageBytes: 1 << 20}},
		{"a Deferrer without a Broker", "Deferrer", spool.Config{Deferrer: tm.(coresql.Deferrer)}},
		{"a Deferrer that is not an Aborter", "Deferrer", spool.Config{Broker: broker, Deferrer: aborterless{}}},
	} {
		c.cfg.Transport, c.cfg.MaxAttempts = transport, 3
		s, err := spool.New(c.cfg)
		if !errs.HasCode(err, corespool.CodeSpoolMisconfigured) || s != nil {
			t.Errorf("%s: New() = %v, %v, want SPOOL_MISCONFIGURED", c.name, s, err)
			continue
		}
		if setting := settingOf(err); setting != c.setting {
			t.Errorf("%s: the refusal names %q, want %s", c.name, setting, c.setting)
		}
	}
}

// settingOf returns the setting a SpoolMisconfigured names.
func settingOf(err error) string {
	for _, field := range errs.FieldsOf(err) {
		if field.Key() == "setting" {
			return field.StringValue()
		}
	}
	return ""
}
