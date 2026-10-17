package kit

import (
	"context"
	"errors"
	"io"
	"slices"
	"testing"
	"time"

	"github.com/kitsunium/sdk/framework/internal/kit/plug"
	"github.com/kitsunium/sdk/framework/model"
	"github.com/kitsunium/sdk/pkg/v1/app/mail"
	"github.com/kitsunium/sdk/pkg/v1/data/queue"
	"github.com/kitsunium/sdk/pkg/v1/data/sql"
	"github.com/kitsunium/sdk/pkg/v1/errs"
)

// A mailer's outbox on the database that keeps its service (ADR 0178 C7
// item 7), on kit's fake database: what the mailbox hears of a mail written
// in a transaction — EventQueued after the commit and before the mail is
// sent, nothing after a rollback —, an identifier a rollback freed delivered
// afterwards, and an outbox that refuses a mail failing the transaction.

// mailNote is what the mailroom keeps of a mail it sends.
type mailNote struct {
	ID string `json:"id"`
}

// mailroom is a running app whose one service keeps notes and mails them,
// on the fake database main, and the stream of its events.
type mailroom struct {
	app    *App
	notes  *StoreService[mailNote]
	mailer *Mailer
	db     *FakeDB
	events <-chan model.Event
}

// mailroomTable is the mailroom's outbox, as kit names it.
const mailroomTable = "kit_queue__mailroom__notify__outbox"

// startMailroom runs the mailroom in dev on the fake database main, every
// queue it opens behind wrap when it is set, and listens to its events.
func startMailroom(t *testing.T, wrap func(queue.Broker) queue.Broker) *mailroom {
	t.Helper()
	needsFileStore(t)
	t.Setenv("KIT_SECRETS", "memory")
	t.Setenv("KIT_DATA_KEY", "a data key of exactly 32 bytes..")
	t.Setenv("KIT_INDEX_KEY", "an index key of more than 16 bytes")
	t.Setenv("MAILROOM_MAIN_URL", "fake://kit:pw@db.internal:5432/main?tls=verify-full")
	svc := NewService("mailroom", "Notes, mailed.")
	r := &mailroom{db: NewFakeDB(sql.DialectPostgres)}
	r.notes = svc.Store("notes", func(n mailNote) string { return n.ID })
	r.mailer = svc.Mailer("notify", From("Mailroom", "mailroom@example.com"))
	r.app = NewApp("mailroom", svc).With(DataDir(t.TempDir()), Listen("127.0.0.1:0"), Env(EnvDev), Analyze(false), Logs(io.Discard),
		Database("main", r.db.Engine()))
	r.app.wrapQueue = wrap
	must(t, r.app.Start(t.Context()))
	stopAtCleanup(t, r.app)
	events, stop := r.app.hub.subscribe()
	t.Cleanup(stop)
	r.events = events
	if got := r.app.Graph().Node("mailroom/mailer/notify").Mailer.Outbox; got != "sql" {
		t.Fatalf("the outbox is %q; want sql", got)
	}
	return r
}

// roomLetter is the mail the mailroom sends about the note id.
func roomLetter(id string) mail.Message {
	return mail.Message{To: []mail.Address{{Addr: "reader@example.com"}}, Subject: "Note " + id, Text: "The note " + id + "."}
}

// send keeps the note id and mails it, in the transaction ctx carries.
func (r *mailroom) send(ctx context.Context, id string) (string, error) {
	if err := r.notes.Insert(ctx, mailNote{ID: id}); err != nil {
		return "", err
	}
	return r.mailer.Send(ctx, roomLetter(id))
}

// statuses collects, until the mail id is sent or the wait ends, the
// statuses the mailbox streamed for id, in order.
func (r *mailroom) statuses(t *testing.T, id string, wait time.Duration) []string {
	t.Helper()
	var out []string
	timer := time.NewTimer(wait)
	defer timer.Stop()
	for {
		select {
		case e := <-r.events:
			if e.Type != model.EventMail || e.Mail == nil || e.Mail.ID != id {
				continue
			}
			out = append(out, e.Mail.Status)
			if e.Mail.Status == model.MailSent {
				return out
			}
		case <-timer.C:
			return out
		}
	}
}

// A mail sent in a committed transaction on the outbox's database is told
// queued after the commit, then sent: never sent before it is queued.
func TestAMailOnTheDatabaseIsQueuedBeforeItIsSent(t *testing.T) {
	r := startMailroom(t, nil)
	var id string
	must(t, Transact(t.Context(), func(ctx context.Context) error {
		var err error
		id, err = r.send(ctx, "kept")
		return err
	}))
	if got := r.statuses(t, id, 10*time.Second); !slices.Equal(got, []string{model.MailQueued, model.MailSent}) {
		t.Errorf("the mailbox heard %v; want queued, then sent", got)
	}
}

// A mail sent in a transaction that rolls back is nowhere — the outbox's
// table holds nothing, the mailbox hears nothing —, and its identifier,
// which the rollback freed, carries a mail sent afterwards: the spool does
// not wait for the announcement of a mail that never was.
func TestARolledBackMailFreesItsIdentifier(t *testing.T) {
	r := startMailroom(t, nil)
	var id string
	errUndo := errors.New("the note is taken back")
	if err := Transact(t.Context(), func(ctx context.Context) error {
		var err error
		id, err = r.send(ctx, "gone")
		return errors.Join(err, errUndo)
	}); !errors.Is(err, errUndo) {
		t.Fatalf("the rolled back transaction: %v", err)
	}
	if left := r.db.Messages(mailroomTable); len(left) != 0 {
		t.Fatalf("a rolled back transaction left %d mails in the outbox", len(left))
	}
	r.mailer.mu.Lock()
	spool := r.mailer.spool
	r.mailer.mu.Unlock()
	must(t, spool.SendWithID(t.Context(), id, plug.MailValue{Value: roomLetter("again")}))
	if got := r.statuses(t, id, 10*time.Second); !slices.Equal(got, []string{model.MailQueued, model.MailSent}) {
		t.Errorf("the mailbox heard %v of the identifier a rollback freed; want queued, then sent", got)
	}
}

// refusingOutbox refuses every mail published into it.
type refusingOutbox struct{ queue.Broker }

// Publish refuses.
func (refusingOutbox) Publish(context.Context, []byte) (queue.Message, error) {
	return queue.Message{}, failure(CodeQueueOpen, "QUEUE_BROKEN", "the queue's storage failed", nil)
}

// An outbox that refuses a mail written in the transaction fails it: the
// note written before the mail is not kept, and the mailbox hears nothing.
func TestAnOutboxThatRefusesAMailFailsTheTransaction(t *testing.T) {
	r := startMailroom(t, func(b queue.Broker) queue.Broker { return refusingOutbox{b} })
	var id string
	err := Transact(t.Context(), func(ctx context.Context) error {
		var err error
		id, err = r.send(ctx, "refused")
		return err
	})
	if !errs.HasReason(err, "QUEUE_BROKEN") || !errs.HasCode(err, CodeMailQueue) {
		t.Fatalf("a transaction whose outbox refused its mail: %v; want the refusal", err)
	}
	if _, err := r.notes.Get(t.Context(), "refused"); err == nil {
		t.Error("the note written before the refused mail was kept")
	}
	if got := r.statuses(t, id, 200*time.Millisecond); len(got) != 0 {
		t.Errorf("the mailbox heard %v of a refused mail", got)
	}
}
