package queue_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/kitsunium/sdk/pkg/v1/data/queue"
	"github.com/kitsunium/sdk/pkg/v1/data/sql"
	"github.com/kitsunium/sdk/pkg/v1/errs"
)

// spyTransactor is a transactor of the caller's making with the two siblings
// NewSQL asks for. NewSQL sends no statement, so it is never called.
type spyTransactor struct{}

// Transact runs fn with no transaction at all.
func (spyTransactor) Transact(ctx context.Context, _ sql.TxOptions, fn sql.TxFunc) error {
	return fn(ctx, nil)
}

// Join answers no executor and no transaction.
func (spyTransactor) Join(context.Context) (sql.Executor, bool) { return nil, false }

// Defer holds nothing.
func (spyTransactor) Defer(context.Context, func()) bool { return false }

// TestTheFacadeNamesAndBuildsTheSQLBroker names and builds every type ADR 0151
// added with no internal import — issue #259 was a public API a downstream
// module could not spell — and checks the SQL broker carries every sibling.
func TestTheFacadeNamesAndBuildsTheSQLBroker(t *testing.T) {
	t.Parallel()
	cfg := queue.SQLConfig{
		Transactor: spyTransactor{}, Dialect: sql.DialectPostgres, Table: "app__jobs",
		Policy: queue.Policy{VisibilityTimeout: time.Minute, MaxDeliveries: 3, RetryDelay: time.Second, MaxRetryDelay: time.Minute},
	}
	broker, err := queue.NewSQL(cfg)
	if err != nil {
		t.Fatalf("NewSQL() = %v, want nil", err)
	}
	for sibling, carried := range map[string]bool{
		"DeadLetterReader":  carries[queue.DeadLetterReader](broker),
		"LeaseExtender":     carries[queue.LeaseExtender](broker),
		"Waker":             carries[queue.Waker](broker),
		"Rejecter":          carries[queue.Rejecter](broker),
		"DeadLetterManager": carries[queue.DeadLetterManager](broker),
	} {
		if !carried {
			t.Errorf("the SQL broker does not implement queue.%s", sibling)
		}
	}
	cfg.Table = "App; DROP"
	if _, refused := queue.NewSQL(cfg); !errors.Is(refused, queue.SQLQueueMisconfigured) ||
		!errs.HasCode(refused, queue.CodeSQLQueueMisconfigured) {
		t.Fatalf("NewSQL(a table that is not one) = %v, want SQLQueueMisconfigured", refused)
	}
	if len("app__jobs") > queue.MaxSQLTableLen {
		t.Fatal("MaxSQLTableLen is shorter than a table name the package doc uses")
	}
}

// carries reports whether broker implements the capability T.
func carries[T any](broker queue.Broker) bool {
	_, ok := broker.(T)
	return ok
}

// TestTheFacadeBuildsTheSQLMigration pins SQLMigration through the public
// names: a migration the caller's Migrator runs, whose one statement creates
// the table and does nothing when it exists.
func TestTheFacadeBuildsTheSQLMigration(t *testing.T) {
	t.Parallel()
	migration, err := queue.SQLMigration(sql.DialectSQLite, "app__jobs", 20260930120000)
	if err != nil {
		t.Fatalf("SQLMigration() = %v, want nil", err)
	}
	if migration.Version != 20260930120000 || migration.Name != "queue app__jobs" {
		t.Fatalf("migration = %d %q", migration.Version, migration.Name)
	}
	if validateErr := migration.Validate(); validateErr != nil {
		t.Fatalf("Validate() = %v, want nil", validateErr)
	}
}

// TestTheFacadeMarksAFailureNoRetryCanFix pins DoNotRetry and its recognition
// through the public names: errs.HasCode with CodeNotRetryable finds the mark
// on an SDK cause — whose own code stays the origin — and on a foreign one.
func TestTheFacadeMarksAFailureNoRetryCanFix(t *testing.T) {
	t.Parallel()
	own := errs.New(0x40_01_01_01, "ORDER_UNDECODABLE", "the order does not decode", "private half")
	for name, cause := range map[string]error{
		"an SDK cause":    own,
		"a foreign cause": errors.New("unexpected end of JSON input"), //nolint:err113 // a foreign error is the case
		"no cause at all": nil,
	} {
		marked := queue.DoNotRetry(cause)
		if !errs.HasCode(marked, queue.CodeNotRetryable) {
			t.Errorf("%s: DoNotRetry() does not carry CodeNotRetryable: %v", name, marked)
		}
	}
	if reason, _ := errs.ReasonOf(queue.DoNotRetry(own)); reason != "ORDER_UNDECODABLE" {
		t.Errorf("the marked SDK cause records %q, want its own reason", reason)
	}
	if !errors.Is(queue.DoNotRetry(nil), queue.NotRetryable) {
		t.Error("DoNotRetry(nil) is not NotRetryable")
	}
}

// TestEveryFacadeBrokerDecidesWhatBecomesOfADeadLetter drives the two new
// siblings through the public names on the memory and file brokers: a
// rejected message is dead at once, a replay brings it back as a first
// delivery, a deletion is final, and a second decision is DeadLetterNotFound.
func TestEveryFacadeBrokerDecidesWhatBecomesOfADeadLetter(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"memory", "file"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			broker := facadeBroker(t, name)
			published, err := broker.Publish(t.Context(), []byte("poison"))
			if err != nil {
				t.Fatalf("Publish() = %v", err)
			}
			batch, err := broker.Receive(t.Context(), 1)
			if err != nil || len(batch) != 1 {
				t.Fatalf("Receive() = %d, %v", len(batch), err)
			}
			if rejectErr := broker.(queue.Rejecter).Reject(t.Context(), batch[0].Lease.Receipt, queue.DoNotRetry(nil)); rejectErr != nil {
				t.Fatalf("Reject() = %v", rejectErr)
			}
			dead, err := broker.(queue.DeadLetterReader).DeadLetters(t.Context(), 10)
			if err != nil || len(dead) != 1 || dead[0].Reason != "NOT_RETRYABLE" || dead[0].Deliveries != 1 {
				t.Fatalf("DeadLetters() = %+v, %v; want one NOT_RETRYABLE after 1 delivery", dead, err)
			}
			manager := broker.(queue.DeadLetterManager)
			if replayErr := manager.ReplayDeadLetter(t.Context(), published.ID); replayErr != nil {
				t.Fatalf("ReplayDeadLetter() = %v", replayErr)
			}
			again, err := broker.Receive(t.Context(), 1)
			if err != nil || len(again) != 1 || again[0].Message.ID != published.ID || again[0].Deliveries != 1 {
				t.Fatalf("after the replay Receive() = %+v, %v; want the message on its first delivery", again, err)
			}
			if rejectErr := broker.(queue.Rejecter).Reject(t.Context(), again[0].Lease.Receipt, nil); rejectErr != nil {
				t.Fatalf("Reject() = %v", rejectErr)
			}
			if deleteErr := manager.DeleteDeadLetter(t.Context(), published.ID); deleteErr != nil {
				t.Fatalf("DeleteDeadLetter() = %v", deleteErr)
			}
			for _, decide := range []func(context.Context, string) error{manager.DeleteDeadLetter, manager.ReplayDeadLetter} {
				if decideErr := decide(t.Context(), published.ID); !errors.Is(decideErr, queue.DeadLetterNotFound) ||
					!errs.HasCode(decideErr, queue.CodeDeadLetterNotFound) {
					t.Errorf("a second decision = %v, want DeadLetterNotFound", decideErr)
				}
			}
		})
	}
}

// TestTheFacadeRefusesACeilingNoRetryDelayCanGrowTo keeps MaxRetryDelay's
// refusal visible where a consumer meets it, and its zero harmless.
func TestTheFacadeRefusesACeilingNoRetryDelayCanGrowTo(t *testing.T) {
	t.Parallel()
	for _, policy := range []queue.Policy{
		{VisibilityTimeout: time.Minute, MaxDeliveries: 3, MaxRetryDelay: time.Minute},
		{VisibilityTimeout: time.Minute, MaxDeliveries: 3, RetryDelay: time.Minute, MaxRetryDelay: time.Second},
	} {
		if _, err := queue.NewMemory(queue.MemoryConfig{Policy: policy}); !errors.Is(err, queue.QueueMisconfigured) {
			t.Errorf("NewMemory(%+v) = %v, want QueueMisconfigured", policy, err)
		}
	}
	if _, err := queue.NewMemory(queue.MemoryConfig{Policy: queue.Policy{
		VisibilityTimeout: time.Minute, MaxDeliveries: 3, RetryDelay: time.Second,
	}}); err != nil {
		t.Errorf("NewMemory(no ceiling) = %v, want nil — zero keeps the constant delay", err)
	}
}
