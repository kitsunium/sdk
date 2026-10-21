package queue_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/kitsunium/sdk/internal/kernel/clock"
)

// TestSQLAReservationExistsIfAndOnlyIfItsTransactionCommits is ADR 0151's
// outbox applied to PublishOnce (ADR 0183): inside the caller's transaction,
// the key's reservation and the message are one savepoint of it — a rollback
// takes both back, so the next publication under the key goes out, and a
// commit keeps both, so the one after is a duplicate.
func TestSQLAReservationExistsIfAndOnlyIfItsTransactionCommits(t *testing.T) {
	t.Parallel()
	eachDialect(t, func(t *testing.T, fx *sqlFixture, _ *clock.ManualClock) {
		errRolledBack := errors.New("the business write failed")
		err := inTransaction(t, fx, func(ctx context.Context) error {
			if _, published, pubErr := oncePublisher(t, fx.broker).PublishOnce(ctx, "order-1", []byte("paid"), time.Minute); pubErr != nil || !published {
				t.Fatalf("PublishOnce() inside the transaction = (%v, %v)", published, pubErr)
			}
			return errRolledBack
		})
		if !errors.Is(err, errRolledBack) {
			t.Fatalf("Transact() = %v, want the function's own error", err)
		}
		if fx.engine.rows() != 0 {
			t.Fatalf("a rolled-back PublishOnce left %d rows", fx.engine.rows())
		}
		if err := inTransaction(t, fx, func(ctx context.Context) error {
			_, published, pubErr := oncePublisher(t, fx.broker).PublishOnce(ctx, "order-1", []byte("paid"), time.Minute)
			if pubErr == nil && !published {
				t.Fatal("PublishOnce() after the rollback published nothing")
			}
			return pubErr
		}); err != nil {
			t.Fatalf("Transact() = %v, want nil", err)
		}
		if _, published := publishOnce(t, fx.broker, "order-1", "paid", time.Minute); published {
			t.Fatal("PublishOnce() after the commit published again")
		}
	})
}

// TestSQLAReservationIsNoMessage pins that the reservation row is invisible to
// everything that reads messages: Receive, MessageReader and DeadLetterReader
// see the one message and nothing of its key, and acknowledging the message
// leaves the reservation holding the key.
func TestSQLAReservationIsNoMessage(t *testing.T) {
	t.Parallel()
	eachDialect(t, func(t *testing.T, fx *sqlFixture, _ *clock.ManualClock) {
		message, _ := publishOnce(t, fx.broker, "k", "x", time.Minute)
		if fx.engine.reservations() != 1 {
			t.Fatalf("the table holds %d reservations, want 1", fx.engine.reservations())
		}
		live, err := messageReader(t, fx.broker).Messages(t.Context(), "", 10)
		if err != nil || len(live) != 1 || live[0].Message.ID != message.ID {
			t.Fatalf("Messages() = (%+v, %v), want the message alone", live, err)
		}
		if dead := deadLetters(t, fx.broker, 10); len(dead) != 0 {
			t.Fatalf("DeadLetters() = %+v, want none", dead)
		}
		delivery := receiveOne(t, fx.broker)
		if err := fx.broker.Ack(t.Context(), delivery.Lease.Receipt); err != nil {
			t.Fatalf("Ack() = %v", err)
		}
		receiveNone(t, fx.broker)
		if _, published := publishOnce(t, fx.broker, "k", "x", time.Minute); published {
			t.Fatal("the acknowledged message's key was free again inside its window")
		}
	})
}
