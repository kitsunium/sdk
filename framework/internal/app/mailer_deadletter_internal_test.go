package app

import (
	"context"
	"io"
	"testing"
	"time"

	"github.com/kitsunium/sdk/framework/model"
	"github.com/kitsunium/sdk/pkg/v1/data/sql"
)

// A mail an earlier run gave up on, in an outbox on the database, is in the
// next run's mailbox with why: the SQL broker keeps its trail, where the
// spool's MAIL_REJECTED stands behind the transport's SEND_REFUSED, in the
// trail column of the sealed outbox's table (issue #500). The clock never
// moves: a 5xx gives the mail up at its first attempt.
func TestAMailAnEarlierRunGaveUpOnTheDatabaseKeepsWhy(t *testing.T) {
	needsFileStore(t)
	t.Setenv("MAILROOM_SECRETS", "memory")
	t.Setenv("MAILROOM_DATA_KEY", "a data key of exactly 32 bytes..")
	t.Setenv("MAILROOM_INDEX_KEY", "an index key of more than 16 bytes")
	t.Setenv("MAILROOM_MAIN_URL", "fake://kit:pw@db.internal:5432/main?tls=verify-full")
	t.Setenv("MAILROOM_SMTP_URL", "smtp://"+StartRefusingRelay(t, "550 5.1.1 no such user")+"?tls=none")
	db, dir := NewFakeDB(sql.DialectPostgres), t.TempDir()
	run := func() *mailroom {
		svc := NewService("mailroom", "Notes, mailed.")
		r := &mailroom{db: db}
		r.notes = svc.Store("notes", func(n mailNote) string { return n.ID })
		r.mailer = svc.Mailer("notify", From("Mailroom", "mailroom@example.com"), MailAttempts(5))
		r.app = NewApp("mailroom", svc).With(DataDir(dir), Listen("127.0.0.1:0"), Env(EnvDev), Analyze(false), Logs(io.Discard),
			Database("main", db.Engine()))
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

	first := run()
	var id string
	must(t, Transact(t.Context(), func(ctx context.Context) error {
		var err error
		id, err = first.send(ctx, "refused")
		return err
	}))
	deadline := time.NewTimer(10 * time.Second)
	defer deadline.Stop()
	for dead := false; !dead; {
		select {
		case e := <-first.events:
			dead = e.Type == model.EventMail && e.Mail != nil && e.Mail.ID == id && e.Mail.Status == model.MailDead
		case <-deadline.C:
			t.Fatalf("the mail %s was never given up", id)
		}
	}
	// The mailbox hears of the mail given up before the queue buries it; the
	// stop waits for the delivery, whose verdict the queue applies past the
	// stop (#487).
	must(t, first.app.Stop(t.Context()))

	next := run()
	mails := next.mailer.mails(10)
	var found *model.MailSummary
	for i := range mails {
		if mails[i].ID == id {
			found = &mails[i]
		}
	}
	if found == nil {
		t.Fatalf("the next run's mailbox %+v lacks the mail %s the first run gave up", mails, id)
	}
	if found.Status != model.MailDead || found.GivenUp != model.MailRejected || found.Attempts != 1 || found.Subject != "Note refused" {
		t.Errorf("the mail an earlier run gave up %+v, want dead and rejected after one attempt", *found)
	}
	if info := next.app.Graph().Node("mailroom/mailer/notify").Mailer; info.DeadLetters == nil || *info.DeadLetters != 1 {
		t.Errorf("the next run counts %v dead letters, want 1", info.DeadLetters)
	}
}
