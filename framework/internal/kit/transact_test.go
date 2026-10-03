package kit_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kitsunium/sdk/framework/internal/kit"
	"github.com/kitsunium/sdk/framework/model"
	"github.com/kitsunium/sdk/pkg/v1/app/mail"
	"github.com/kitsunium/sdk/pkg/v1/data/sql"
	"github.com/kitsunium/sdk/pkg/v1/errs"
)

// Transactions (ADR 0004): kit.Transact on every backend — memory, the data
// directory, a database (kit's fake one) —, nesting, the one database a
// transaction writes, the data's writer turn, and the effects held until
// the commit. The engine modules run the same on PostgreSQL, MySQL and
// SQLite.

// till is a product of one service, declared afresh for each test: a
// service runs in one app at a time.
type till struct {
	svc         *kit.Service
	cash, notes *kit.StoreService[entry]
	moved       *kit.TopicService[entry]
	mailer      *kit.Mailer
	move, loose *kit.Command[entry, kit.EmptyValue]
	heard       *heardIDs
}

// heardIDs are the messages the till's subscription was delivered.
type heardIDs struct {
	sync.Mutex
	ids []string
}

func (h *heardIDs) add(id string) {
	h.Lock()
	defer h.Unlock()
	h.ids = append(h.ids, id)
}

func (h *heardIDs) list() []string {
	h.Lock()
	defer h.Unlock()
	return slices.Clone(h.ids)
}

// errMove fails a move after it wrote, published and mailed.
var errMove = errors.New("the move failed after it wrote")

func newTill() *till {
	tl := &till{svc: kit.NewService("till", "The till, for the transactions' tests."), heard: &heardIDs{}}
	tl.cash = tl.svc.Store("cash", entry.key)
	tl.notes = tl.svc.Store("notes", entry.key)
	tl.moved = tl.svc.Topic[entry]("moved")
	tl.svc.Subscribe("heard", tl.moved, func(_ context.Context, e entry) error { tl.heard.add(e.ID); return nil })
	tl.mailer = tl.svc.Mailer("mail", kit.From("Till", "till@example.com"))
	handler := func(ctx context.Context, e entry) (kit.EmptyValue, error) {
		if err := tl.cash.Put(ctx, e); err != nil {
			return kit.EmptyValue{}, err
		}
		if err := tl.moved.Publish(ctx, e); err != nil {
			return kit.EmptyValue{}, err
		}
		if strings.HasPrefix(e.ID, "fail") {
			return kit.EmptyValue{}, errMove
		}
		return kit.EmptyValue{}, nil
	}
	tl.move = tl.svc.Command("move", handler)
	tl.loose = tl.svc.Command("move-alone", handler, kit.NoTransaction())
	return tl
}

// backend is where a test runs the till: in memory, in files, on a
// database.
type backend struct {
	name string
	opts func(t *testing.T) []kit.AppConfigurer
}

// backends are the three.
var backends = []backend{
	{"memory", func(*testing.T) []kit.AppConfigurer { return []kit.AppConfigurer{kit.InMemory()} }},
	{"files", func(t *testing.T) []kit.AppConfigurer {
		needsFileStore(t)
		return []kit.AppConfigurer{kit.DataDir(t.TempDir())}
	}},
	{"database", func(t *testing.T) []kit.AppConfigurer {
		needsFileStore(t)
		t.Setenv("TILL_DATABASE_URL", verifiedURL())
		return []kit.AppConfigurer{kit.DataDir(t.TempDir()), kit.Database("database", kit.NewFakeDB(sql.DialectPostgres).Engine())}
	}},
}

// eachBackend runs test on each backend, with a till of its own.
func eachBackend(t *testing.T, test func(t *testing.T, tl *till, app *kit.App)) {
	for _, b := range backends {
		t.Run(b.name, func(t *testing.T) {
			tl := newTill()
			test(t, tl, runTill(t, tl, b.opts(t)...))
		})
	}
}

// runTill runs the till in dev, its mail captured, until the test ends.
func runTill(t *testing.T, tl *till, opts ...kit.AppConfigurer) *kit.App {
	t.Helper()
	t.Setenv("KIT_SMTP_URL", "")
	t.Setenv("KIT_SECRETS", "memory")
	app := kit.NewApp("till", tl.svc).With(append([]kit.AppConfigurer{
		kit.Listen("127.0.0.1:0"), kit.Env(kit.EnvDev), kit.Analyze(false), kit.Logs(io.Discard),
	}, opts...)...)
	run(t, app)
	return app
}

// has reports whether store holds id.
func has(t *testing.T, store *kit.StoreService[entry], id string) bool {
	t.Helper()
	_, err := store.Get(t.Context(), id)
	if ke, ok := errors.AsType[*kit.Error](err); err != nil && (!ok || ke.Code != kit.WireNotFound) {
		t.Fatalf("get %s: %v", id, err)
	}
	return err == nil
}

// A transaction's writes commit together.
func TestATransactionCommits(t *testing.T) {
	eachBackend(t, func(t *testing.T, tl *till, _ *kit.App) {
		err := kit.Transact(t.Context(), func(ctx context.Context) error {
			if err := tl.cash.Insert(ctx, entry{ID: "a"}); err != nil {
				return err
			}
			return tl.notes.Insert(ctx, entry{ID: "b"})
		})
		if err != nil || !has(t, tl.cash, "a") || !has(t, tl.notes, "b") {
			t.Errorf("committed: %v", err)
		}
	})
}

// An error undoes every write of the transaction — an insert, a
// replacement, a deletion —, and the caller gets the error as it was.
func TestATransactionRollsBack(t *testing.T) {
	eachBackend(t, func(t *testing.T, tl *till, _ *kit.App) {
		for _, id := range []string{"kept", "gone"} {
			if err := tl.cash.Put(t.Context(), entry{ID: id}); err != nil {
				t.Fatal(err)
			}
		}
		err := kit.Transact(t.Context(), func(ctx context.Context) error {
			if err := tl.cash.Insert(ctx, entry{ID: "new"}); err != nil {
				return err
			}
			if err := tl.cash.Delete(ctx, "gone"); err != nil {
				return err
			}
			if _, err := tl.cash.Update(ctx, "kept", func(e *entry) error { return nil }); err != nil {
				return err
			}
			if _, err := tl.cash.Get(ctx, "new"); err != nil {
				t.Errorf("inside the transaction, its write is not read: %v", err)
			}
			return errMove
		})
		if !errors.Is(err, errMove) {
			t.Fatalf("the transaction answered %v", err)
		}
		if has(t, tl.cash, "new") || !has(t, tl.cash, "gone") || !has(t, tl.cash, "kept") {
			t.Error("the rollback left the store changed")
		}
	})
}

// A transaction inside another is a savepoint: its failure undoes its own
// writes, and the outer one commits what it wrote before and after.
func TestANestedTransactionIsASavepoint(t *testing.T) {
	eachBackend(t, func(t *testing.T, tl *till, _ *kit.App) {
		err := kit.Transact(t.Context(), func(ctx context.Context) error {
			if err := tl.cash.Insert(ctx, entry{ID: "before"}); err != nil {
				return err
			}
			inner := kit.Transact(ctx, func(ctx context.Context) error {
				if err := tl.cash.Insert(ctx, entry{ID: "inner"}); err != nil {
					return err
				}
				return errMove
			})
			if !errors.Is(inner, errMove) {
				t.Errorf("the savepoint answered %v", inner)
			}
			return tl.cash.Insert(ctx, entry{ID: "after"})
		})
		if err != nil || !has(t, tl.cash, "before") || !has(t, tl.cash, "after") || has(t, tl.cash, "inner") {
			t.Errorf("after the outer commit: %v", err)
		}
	})
}

// A panic undoes the transaction's writes, and goes on.
func TestAPanicRollsTheTransactionBack(t *testing.T) {
	eachBackend(t, func(t *testing.T, tl *till, _ *kit.App) {
		func() {
			defer func() {
				if recover() == nil {
					t.Error("the panic did not go on")
				}
			}()
			err := kit.Transact(t.Context(), func(ctx context.Context) error {
				if err := tl.cash.Insert(ctx, entry{ID: "panicked"}); err != nil {
					return err
				}
				panic("the handler panicked")
			})
			t.Errorf("the transaction returned instead of panicking: %v", err)
		}()
		if has(t, tl.cash, "panicked") {
			t.Error("a panicked transaction's write stayed")
		}
	})
}

// A command is a transaction: one that fails writes and announces
// nothing; one declared kit.NoTransaction keeps what it wrote.
func TestACommandIsATransaction(t *testing.T) {
	eachBackend(t, func(t *testing.T, tl *till, app *kit.App) {
		if _, err := tl.move.Dispatch(t.Context(), entry{ID: "fail-held"}); !errors.Is(err, errMove) {
			t.Fatalf("the failed command answered %v", err)
		}
		if has(t, tl.cash, "fail-held") {
			t.Error("a failed command's write stayed")
		}
		if _, err := tl.loose.Dispatch(t.Context(), entry{ID: "fail-loose"}); !errors.Is(err, errMove) {
			t.Fatalf("the failed command answered %v", err)
		}
		if !has(t, tl.cash, "fail-loose") {
			t.Error("a command without a transaction lost its write")
		}
		if _, err := tl.move.Dispatch(t.Context(), entry{ID: "done"}); err != nil {
			t.Fatal(err)
		}
		eventually(t, "the committed command's message", func() bool { return slices.Contains(tl.heard.list(), "done") })
		if got := tl.heard.list(); slices.Contains(got, "fail-held") || !slices.Contains(got, "fail-loose") {
			t.Errorf("the messages delivered: %v", got)
		}
		g := app.Graph()
		if !slices.ContainsFunc(g.Node("till/command/move").Command.Pipeline, func(m model.Mechanic) bool { return m.Kind == "transaction" }) ||
			slices.ContainsFunc(g.Node("till/command/move-alone").Command.Pipeline, func(m model.Mechanic) bool { return m.Kind == "transaction" }) {
			t.Error("the pipelines do not say which command runs in a transaction")
		}
	})
}

// What leaves the process waits for the commit: a publish and a mail are
// released after it, and a rollback drops them. The mail keeps the outbox
// ID Send returned inside the transaction.
func TestEffectsWaitForTheCommit(t *testing.T) {
	tl := newTill()
	runTill(t, tl, kit.InMemory())
	var mailID string
	err := kit.Transact(t.Context(), func(ctx context.Context) error {
		if err := tl.moved.Publish(ctx, entry{ID: "committed"}); err != nil {
			return err
		}
		id, err := tl.mailer.Send(ctx, mail.Message{To: []mail.Address{{Addr: "ada@example.com"}}, Subject: "moved", Text: "committed"})
		mailID = id
		return err
	})
	if err != nil || mailID == "" {
		t.Fatalf("the transaction: %v, mail %q", err, mailID)
	}
	eventually(t, "the committed message", func() bool { return slices.Contains(tl.heard.list(), "committed") })
	eventually(t, "the committed mail", func() bool {
		return slices.ContainsFunc(tl.mailer.Captured(), func(m model.MailMessage) bool { return m.ID == mailID })
	})
	err = kit.Transact(t.Context(), func(ctx context.Context) error {
		if err := tl.moved.Publish(ctx, entry{ID: "dropped"}); err != nil {
			return err
		}
		if _, err := tl.mailer.Send(ctx, mail.Message{To: []mail.Address{{Addr: "ada@example.com"}}, Subject: "moved", Text: "dropped"}); err != nil {
			return err
		}
		return errMove
	})
	if !errors.Is(err, errMove) {
		t.Fatalf("the rolled back transaction: %v", err)
	}
	if err := tl.moved.Publish(t.Context(), entry{ID: "after"}); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the message after", func() bool { return slices.Contains(tl.heard.list(), "after") })
	if slices.Contains(tl.heard.list(), "dropped") {
		t.Error("a rolled back publish was delivered")
	}
	if slices.ContainsFunc(tl.mailer.Captured(), func(m model.MailMessage) bool { return m.Subject == "moved" && m.ID != mailID }) {
		t.Error("a rolled back mail was sent")
	}
}

// A transaction's mistake returns to its caller when it is made: a mail
// the outbox refuses is refused at the call, not at the commit.
func TestAHeldEffectIsCheckedWhenMade(t *testing.T) {
	tl := newTill()
	runTill(t, tl, kit.InMemory())
	err := kit.Transact(t.Context(), func(ctx context.Context) error {
		_, err := tl.mailer.Send(ctx, mail.Message{Subject: "nobody", Text: "to"})
		return err
	})
	var ke *kit.Error
	if !errors.As(err, &ke) || ke.Status != http.StatusBadRequest {
		t.Errorf("a mail to nobody, in a transaction: %v", err)
	}
}

// A transaction writes one database: writing a store another keeps is
// refused, CodeTransactionSpan, and the data directory counts as one —
// reading them is not.
func TestATransactionWritesOneDatabase(t *testing.T) {
	needsFileStore(t)
	tl := newTill()
	t.Setenv("TILL_MAIN_URL", verifiedURL())
	t.Setenv("TILL_ARCHIVE_URL", verifiedURL())
	runTill(t, tl, kit.DataDir(t.TempDir()),
		kit.Database("main", kit.NewFakeDB(sql.DialectPostgres).Engine(), kit.Keeps(tl.cash)),
		kit.Database("archive", kit.NewFakeDB(sql.DialectPostgres).Engine(), kit.Keeps(tl.notes)))
	if err := tl.notes.Put(t.Context(), entry{ID: "read"}); err != nil {
		t.Fatal(err)
	}
	err := kit.Transact(t.Context(), func(ctx context.Context) error {
		if err := tl.cash.Insert(ctx, entry{ID: "first"}); err != nil {
			return err
		}
		if _, err := tl.notes.Get(ctx, "read"); err != nil {
			t.Errorf("a store of another database is read as usual: %v", err)
		}
		return tl.notes.Insert(ctx, entry{ID: "second"})
	})
	var ke *kit.Error
	if !errs.HasCode(err, kit.CodeTransactionSpan) || !errors.As(err, &ke) || ke.Status != http.StatusBadRequest {
		t.Fatalf("a write to a second database: %v", err)
	}
	if has(t, tl.cash, "first") || has(t, tl.notes, "second") {
		t.Error("the refused transaction left a write")
	}
}

// The data directory counts as one database: a transaction on a database
// refuses a write to a store kept in files.
func TestTheDataDirectoryCountsAsADatabase(t *testing.T) {
	needsFileStore(t)
	tl := newTill()
	t.Setenv("TILL_MAIN_URL", verifiedURL())
	runTill(t, tl, kit.DataDir(t.TempDir()), kit.Database("main", kit.NewFakeDB(sql.DialectPostgres).Engine(), kit.Keeps(tl.cash)))
	err := kit.Transact(t.Context(), func(ctx context.Context) error {
		if err := tl.cash.Insert(ctx, entry{ID: "on the database"}); err != nil {
			return err
		}
		return tl.notes.Insert(ctx, entry{ID: "in files"})
	})
	if !errs.HasCode(err, kit.CodeTransactionSpan) {
		t.Fatalf("a write to the data directory: %v", err)
	}
	err = kit.Transact(t.Context(), func(ctx context.Context) error {
		if err := tl.notes.Insert(ctx, entry{ID: "in files"}); err != nil {
			return err
		}
		return tl.cash.Insert(ctx, entry{ID: "on the database"})
	})
	if !errs.HasCode(err, kit.CodeTransactionSpan) || has(t, tl.notes, "in files") {
		t.Fatalf("a write to the database after the data directory: %v", err)
	}
}

// On the data directory, a transaction takes the writer turn: a second
// transaction, and a write outside any, wait for its end.
//
// Goroutine lifecycle: the transactions run on goroutines the test waits
// for, each reporting on its own channel before the test ends.
func TestDataDirectoryTransactionsTakeTurns(t *testing.T) {
	needsFileStore(t)
	tl := newTill()
	runTill(t, tl, kit.DataDir(t.TempDir()))
	wrote, release := make(chan struct{}), make(chan struct{})
	first := make(chan error, 1)
	go func() {
		first <- kit.Transact(context.Background(), func(ctx context.Context) error {
			if err := tl.cash.Insert(ctx, entry{ID: "first"}); err != nil {
				return err
			}
			close(wrote)
			<-release
			return nil
		})
	}()
	<-wrote
	second, plain := make(chan error, 1), make(chan error, 1)
	go func() {
		second <- kit.Transact(context.Background(), func(ctx context.Context) error {
			return tl.cash.Insert(ctx, entry{ID: "second"})
		})
	}()
	go func() { plain <- tl.notes.Insert(context.Background(), entry{ID: "plain"}) }()
	select {
	case err := <-second:
		t.Fatalf("a second transaction wrote beside the first: %v", err)
	case err := <-plain:
		t.Fatalf("a write outside any transaction ran beside one: %v", err)
	case <-time.After(150 * time.Millisecond):
	}
	close(release)
	for _, ch := range []chan error{first, second, plain} {
		if err := <-ch; err != nil {
			t.Fatal(err)
		}
	}
	if !has(t, tl.cash, "first") || !has(t, tl.cash, "second") || !has(t, tl.notes, "plain") {
		t.Error("a write that waited for the turn was lost")
	}
}

// A transaction is drawn: a span of op transaction on the node it runs in,
// its outcome, its backend, the effects it held.
func TestATransactionIsDrawn(t *testing.T) {
	tl := newTill()
	app := runTill(t, tl, kit.InMemory())
	if _, err := tl.move.Dispatch(t.Context(), entry{ID: "drawn"}); err != nil {
		t.Fatal(err)
	}
	if _, err := tl.move.Dispatch(t.Context(), entry{ID: "fail-drawn"}); !errors.Is(err, errMove) {
		t.Fatal(err)
	}
	var traces []model.Trace
	eventually(t, "the two runs", func() bool {
		call(t, app, "GET /_kit/api/traces?node=till/command/move", noBody).json(t, &traces)
		return len(traces) == 2
	})
	var outcomes []string
	for _, tr := range traces {
		for _, s := range tr.Spans {
			if s.Op == model.OpTransaction {
				if s.Node != "till/command/move" || s.Attrs["backend"] != "memory" || s.Attrs["effects"] != "1" {
					t.Errorf("a transaction's span: %+v", s)
				}
				outcomes = append(outcomes, s.Attrs["outcome"])
			}
			if s.Op == model.OpPublish && s.Attrs["held"] != "commit" {
				t.Errorf("a held publish's span: %+v", s)
			}
		}
	}
	slices.Sort(outcomes)
	if !slices.Equal(outcomes, []string{model.OutcomeCommit, model.OutcomeRollback}) {
		t.Errorf("the outcomes: %v", outcomes)
	}
	if s := app.Graph().Node("till/command/move").Stats; s == nil || s.Count != 2 {
		t.Errorf("a transaction counts as a run of its node: %+v", s)
	}
}
