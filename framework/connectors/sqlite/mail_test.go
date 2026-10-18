package sqlite_test

import (
	"bytes"
	"context"
	stdsql "database/sql"
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/kitsunium/sdk/framework/app"
	_ "github.com/kitsunium/sdk/framework/app/mail"
	"github.com/kitsunium/sdk/framework/model"
	"github.com/kitsunium/sdk/pkg/v1/app/mail"
)

// A mailer's outbox on the database that keeps its service (ADR 0178 C7
// item 7), on a real SQLite file: a mail sent in app.Transact is written in
// the transaction, sealed under kit's data-key — the table holds no address,
// no subject, no body —, and delivered once it commits; rolled back, it
// never was. A mail the outbox refuses fails the transaction. Without a
// database the outbox is the spool's own, as before.

// notice is what the post office keeps of a mail it sends.
type notice struct {
	ID string `json:"id"`
}

// postOffice is a product whose one service keeps notices and mails them.
type postOffice struct {
	app     *app.App
	notices *app.Store[notice]
	mailer  *app.Mailer
}

// outboxTable is the post office's outbox, as kit names it.
const outboxTable = "app_queue__post__notify__outbox"

// The mail's words, which must never rest in clear in the outbox's table.
const (
	mailTo      = "reader@example.com"
	mailSubject = "Your notice is ready"
	mailText    = "The confidential body of the notice."
)

// newPostOffice declares the post office afresh, its data in dir, on a
// SQLite database unless the options say otherwise.
func newPostOffice(t *testing.T, dir string, opts ...app.AppOption) *postOffice {
	t.Helper()
	t.Setenv("POST_SECRETS", "memory")
	t.Setenv("POST_DATA_KEY", "a data key of exactly 32 bytes..")
	t.Setenv("POST_INDEX_KEY", "an index key of more than 16 bytes")
	svc := app.NewService("post", "Notices, mailed.")
	p := &postOffice{}
	p.notices = svc.Store("notices", func(n notice) string { return n.ID })
	p.mailer = svc.Mailer("notify", app.From("Post", "post@example.com"), app.MailAttempts(1))
	p.app = app.NewApp("post", svc).With(append(bureauOptions(dir), opts...)...)
	return p
}

// letter is the mail the post office sends about the notice id.
func letter(id string) mail.Message {
	return mail.Message{To: []mail.Address{{Addr: mailTo}}, Subject: mailSubject + " " + id, Text: mailText}
}

// send keeps the notice id and mails it, in the transaction ctx carries,
// and returns the mail's outbox ID.
func (p *postOffice) send(ctx context.Context, id string) (string, error) {
	if err := p.notices.Insert(ctx, notice{ID: id}); err != nil {
		return "", err
	}
	return p.mailer.Send(ctx, letter(id))
}

// delivered waits until the capture transport kept the mail whose subject
// names id.
func (p *postOffice) delivered(t *testing.T, id string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if p.sent(id) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("the mail about %s was not delivered", id)
}

// sent reports whether the capture transport kept the mail about id.
func (p *postOffice) sent(id string) bool {
	return slices.ContainsFunc(p.mailer.Captured(), func(m model.MailMessage) bool { return m.Subject == mailSubject+" "+id })
}

// payloads are the payloads the outbox's table holds, waiting or dead.
func payloads(t *testing.T, db *stdsql.DB) [][]byte {
	t.Helper()
	rows, err := db.QueryContext(t.Context(), `SELECT payload FROM "`+outboxTable+`"`)
	if err != nil {
		t.Fatalf("the outbox's table: %v", err)
	}
	defer rows.Close()
	var out [][]byte
	for rows.Next() {
		var p []byte
		if err := rows.Scan(&p); err != nil {
			t.Fatal(err)
		}
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

// A mail sent in a transaction on the outbox's database is there once it
// commits — sealed: its addresses, subject and body are nowhere in the
// table — and its delivery opens it. The relay refuses, so the mail stays,
// dead, for the table to be read.
func TestAMailCommittedIsSealedInTheOutbox(t *testing.T) {
	needsFileStore(t)
	t.Setenv("POST_SMTP_URL", "smtp://127.0.0.1:1?tls=none")
	dir := t.TempDir()
	p := newPostOffice(t, dir, onSQLite())
	run(t, p.app)
	if got := p.app.Graph().Node("post/mailer/notify").Mailer.Outbox; got != "sql" {
		t.Fatalf("the outbox is %q; want sql", got)
	}
	if err := app.Transact(t.Context(), func(ctx context.Context) error {
		_, err := p.send(ctx, "kept")
		return err
	}); err != nil {
		t.Fatalf("the committed transaction: %v", err)
	}
	db := open(t, filepath.Join(dir, "main.sqlite"))
	var rows [][]byte
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if rows = payloads(t, db); len(rows) == 1 && dead(p) == 1 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if len(rows) != 1 || dead(p) != 1 {
		t.Fatalf("the outbox holds %d mails, %d dead; want the one mail, dead after its one attempt", len(rows), dead(p))
	}
	if !bytes.HasPrefix(rows[0], []byte(`"sealed:v1:root:`)) {
		t.Errorf("the mail rests unsealed: %.80q", rows[0])
	}
	for _, word := range []string{mailTo, mailSubject, mailText, "post@example.com"} {
		if bytes.Contains(rows[0], []byte(word)) {
			t.Errorf("the outbox's table holds %q in clear", word)
		}
	}
}

// dead is how many dead letters the post office's mailer counts.
func dead(p *postOffice) int {
	if n := p.app.Graph().Node("post/mailer/notify").Mailer.DeadLetters; n != nil {
		return *n
	}
	return -1
}

// A mail sent in a transaction that commits is delivered; one sent in a
// transaction that rolls back never was — the table holds nothing of it,
// and it is never delivered —, while the next mail goes through.
func TestAMailFollowsItsTransactionOnTheDatabase(t *testing.T) {
	needsFileStore(t)
	dir := t.TempDir()
	p := newPostOffice(t, dir, onSQLite())
	run(t, p.app)
	ctx := t.Context()
	db := open(t, filepath.Join(dir, "main.sqlite"))
	if err := app.Transact(ctx, func(ctx context.Context) error {
		_, err := p.send(ctx, "gone")
		return errors.Join(err, errUndo)
	}); !errors.Is(err, errUndo) {
		t.Fatalf("the rolled back transaction: %v", err)
	}
	if n := len(payloads(t, db)); n != 0 {
		t.Fatalf("a rolled back transaction left %d mails in the outbox", n)
	}
	if err := app.Transact(ctx, func(ctx context.Context) error {
		_, err := p.send(ctx, "kept")
		return err
	}); err != nil {
		t.Fatalf("the committed transaction: %v", err)
	}
	p.delivered(t, "kept")
	if p.sent("gone") {
		t.Error("a rolled back mail was delivered")
	}
	if _, err := p.notices.Get(ctx, "gone"); err == nil {
		t.Error("the rolled back notice was kept")
	}
}

// A mail the outbox refuses in the transaction — larger than it accepts —
// fails the transaction: Transact returns the refusal, the caller's mistake,
// and the notice written before it is not kept.
func TestARefusedMailFailsTheTransaction(t *testing.T) {
	needsFileStore(t)
	p := newPostOffice(t, t.TempDir(), onSQLite())
	run(t, p.app)
	ctx := t.Context()
	huge := letter("huge")
	huge.Text = strings.Repeat("x", 32<<20)
	err := app.Transact(ctx, func(ctx context.Context) error {
		if err := p.notices.Insert(ctx, notice{ID: "before"}); err != nil {
			return err
		}
		_, err := p.mailer.Send(ctx, huge)
		return err
	})
	if ke, ok := errors.AsType[*app.Error](err); !ok || ke.Code != app.WireInvalid {
		t.Fatalf("a transaction whose outbox refused its mail: %v; want the refusal, invalid", err)
	}
	if _, err := p.notices.Get(ctx, "before"); err == nil {
		t.Error("the write before the refused mail was kept")
	}
}

// Without a database the outbox is the spool's own on disk, unsealed, as
// before: a mail held by its transaction leaves after the commit.
func TestAMailWithoutADatabaseLeavesAfterTheCommit(t *testing.T) {
	needsFileStore(t)
	dir := t.TempDir()
	p := newPostOffice(t, dir)
	run(t, p.app)
	if got := p.app.Graph().Node("post/mailer/notify").Mailer.Outbox; got != "file" {
		t.Fatalf("the outbox is %q; want file", got)
	}
	if err := app.Transact(t.Context(), func(ctx context.Context) error {
		_, err := p.send(ctx, "kept")
		return err
	}); err != nil {
		t.Fatalf("the committed transaction: %v", err)
	}
	p.delivered(t, "kept")
}
