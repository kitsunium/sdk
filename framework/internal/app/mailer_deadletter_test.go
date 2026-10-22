package app_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/kitsunium/sdk/framework/internal/app"
	"github.com/kitsunium/sdk/framework/model"
)

// A mail an earlier run gave up is in the mailbox the next run shows, with
// why it was given up: the dead letter the outbox kept on disk carries the
// code the spool added to its failure's trail, which is all that tells a
// rejected or an expired mail from one that ran out of attempts once the
// process that gave it up has gone (issue #500). A 5xx gives the mail up at
// its first attempt; a 4xx is retried, the clock moved only between
// attempts, until the attempt that fails past the mailer's max-age. Either
// is given up through the queue's Rejecter, whose verdict lands past the
// stop that follows (#487).
func TestAMailAnEarlierRunGaveUpKeepsWhy(t *testing.T) {
	for name, c := range map[string]struct {
		reply    string
		code     int
		givenUp  string
		attempts int
		opts     []app.AppConfigurer
	}{
		"rejected": {reply: "550 5.1.1 " + refusalWords, code: 550, givenUp: model.MailRejected, attempts: 1},
		"expired": {
			reply: "451 4.3.0 try later", code: 451, givenUp: model.MailExpired, attempts: 3,
			opts: []app.AppConfigurer{app.Set("mail-max-age", "2s")},
		},
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			relay := startRefusingRelay(t, c.reply)
			mailer := []app.MailerConfigurer{app.MailAttempts(5)}
			clk := newBounceClock()
			first, m := bounceApp(t, relay, clk, mailer, append([]app.AppConfigurer{app.DataDir(dir)}, c.opts...)...)
			stream := events(t, first)
			id := sendBounce(t, m)
			for _, backoff := range []time.Duration{time.Second, 2 * time.Second}[:c.attempts-1] {
				mailStatuses(t, stream, id, model.MailRetrying, model.MailDead)
				clk.Advance(backoff)
			}
			seen := mailStatuses(t, stream, id, model.MailDead, model.MailSent)
			if dead := seen[len(seen)-1]; dead.Status != model.MailDead || dead.GivenUp != c.givenUp || dead.Reply != c.code {
				t.Fatalf("the first run's dead mail %+v, want %s", dead, c.givenUp)
			}
			if err := first.Stop(t.Context()); err != nil {
				t.Fatal(err)
			}

			next, _ := bounceApp(t, relay, newBounceClock(), mailer, append([]app.AppConfigurer{app.DataDir(dir)}, c.opts...)...)
			r := call(t, next, "GET /_app/api/mail", noBody)
			var list []model.MailSummary
			if err := json.Unmarshal(r.body, &list); err != nil || r.status != 200 {
				t.Fatalf("mail: %d %s", r.status, r.body)
			}
			var found *model.MailSummary
			for i := range list {
				if list[i].ID == id {
					found = &list[i]
				}
			}
			if found == nil {
				t.Fatalf("the next run's mailbox %+v lacks the mail %s the first run gave up", list, id)
			}
			if found.Status != model.MailDead || found.GivenUp != c.givenUp || found.Attempts != c.attempts || found.Mailer != bounceMailer ||
				found.Subject != bounceSubject || found.QueuedAt.IsZero() {
				t.Errorf("the mail an earlier run gave up %+v, want dead and %s after %d attempts", *found, c.givenUp, c.attempts)
			}
			if found.Error == "" || strings.Contains(found.Error, "no such user") || strings.Contains(found.Error, "try later") {
				t.Errorf("its error %q: empty, or the server's words", found.Error)
			}
			if info := next.Graph().Node(bounceMailer).Mailer; info.DeadLetters == nil || *info.DeadLetters != 1 {
				t.Errorf("the next run counts %v dead letters, want 1", info.DeadLetters)
			}
		})
	}
}
