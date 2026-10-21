package app_test

import (
	"context"
	"io"
	"net"
	"net/textproto"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kitsunium/sdk/framework/internal/app"
	"github.com/kitsunium/sdk/framework/model"
	"github.com/kitsunium/sdk/pkg/v1/app/mail"
	"github.com/kitsunium/sdk/pkg/v1/clock"
	"github.com/kitsunium/sdk/pkg/v1/observe/logger"
)

// The mail a bounce test sends, and the words a refusing relay answers with,
// which must never reach a message the framework shows.
const (
	bounceTo      = "nobody@example.org"
	refusalWords  = "no such user nobody@example.org"
	bounceMailer  = "bounce/mailer/mail"
	bounceSubject = "Hello"
)

// startRefusingRelay is an SMTP server for the tests that answers reply —
// "550 5.1.1 …", "451 4.3.0 …" — to every RCPT: the mail is refused there.
func startRefusingRelay(t *testing.T, reply string) string {
	t.Helper()
	ln, lnErr := net.Listen("tcp", "127.0.0.1:0")
	if lnErr != nil {
		t.Fatal(lnErr)
	}
	var wg sync.WaitGroup
	wg.Go(func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			wg.Go(func() { refuse(c, reply) })
		}
	})
	t.Cleanup(func() {
		ln.Close()
		wg.Wait()
	})
	return ln.Addr().String()
}

// refuse speaks SMTP on c until QUIT, answering reply to RCPT.
func refuse(c net.Conn, reply string) {
	defer c.Close()
	if err := c.SetDeadline(time.Now().Add(10 * time.Second)); err != nil {
		return
	}
	tp := textproto.NewConn(c)
	if err := tp.PrintfLine("220 relay.test ESMTP"); err != nil {
		return
	}
	for {
		line, err := tp.ReadLine()
		if err != nil {
			return
		}
		verb, _, _ := strings.Cut(line, " ")
		answer := "250 OK"
		switch strings.ToUpper(verb) {
		case "EHLO", "HELO":
			answer = "250 relay.test"
		case "RCPT":
			answer = reply
		case "QUIT":
			_ = tp.PrintfLine("221 bye")
			return
		}
		if err := tp.PrintfLine("%s", answer); err != nil {
			return
		}
	}
}

// bounceApp runs a service of one mailer, configured by opts, delivering to
// the relay at addr on clk, with the app's own options after.
func bounceApp(t *testing.T, addr string, clk clock.Timed, mailer []app.MailerConfigurer, opts ...app.AppConfigurer) (*app.App, *app.Mailer) {
	t.Helper()
	t.Setenv("BOUNCE_SMTP_URL", "smtp://"+addr+"?tls=none")
	svc := app.NewService("bounce", "A mailer whose mail bounces.")
	m := svc.Mailer("mail", append([]app.MailerConfigurer{app.From("Bounce", "bounce@example.com")}, mailer...)...)
	a := app.NewApp("bounce", svc).With(append([]app.AppConfigurer{
		app.InMemory(), app.Listen("127.0.0.1:0"), app.Env(app.EnvDev), app.Analyze(false), app.Logs(io.Discard), app.Clock(clk),
	}, opts...)...)
	if err := a.Start(t.Context()); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := a.Stop(ctx); err != nil {
			t.Errorf("stop: %v", err)
		}
	})
	return a, m
}

// hooked is a bounce hook that hands each bounce to the test.
func hooked() (app.MailerConfigurer, <-chan *app.BounceEvent) {
	got := make(chan *app.BounceEvent, 4)
	return app.OnBounce(func(_ context.Context, b *app.BounceEvent) { got <- b }), got
}

// bounceOf waits for the hook to hear of a bounce.
func bounceOf(t *testing.T, got <-chan *app.BounceEvent) *app.BounceEvent {
	t.Helper()
	select {
	case b := <-got:
		return b
	case <-time.After(10 * time.Second):
		t.Fatal("the bounce hook heard of nothing")
		return nil
	}
}

// sendBounce sends the test's mail through m.
func sendBounce(t *testing.T, m *app.Mailer) string {
	t.Helper()
	id, err := m.Send(t.Context(), mail.Message{To: []mail.Address{{Addr: bounceTo}}, Subject: bounceSubject, Text: "Hi"})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// newBounceClock is a manual clock, still unless the test moves it.
func newBounceClock() *clock.ManualClock {
	return clock.NewManualClock(time.Date(2026, 10, 21, 9, 0, 0, 0, time.UTC))
}

// A 5xx reply gives the mail up at its first attempt, with attempts to
// spare and the clock still: the mailbox says rejected and keeps the reply's
// code, never its words, and the product's hook gets the reply itself.
func TestARejectedMailIsGivenUpAtOnce(t *testing.T) {
	hook, got := hooked()
	a, m := bounceApp(t, startRefusingRelay(t, "550 5.1.1 "+refusalWords), newBounceClock(), []app.MailerConfigurer{app.MailAttempts(5), hook})
	stream := events(t, a)
	id := sendBounce(t, m)
	statuses := mailStatuses(t, stream, id, model.MailDead, model.MailSent)
	dead := statuses[len(statuses)-1]
	if dead.Status != model.MailDead || dead.Attempts != 1 || dead.GivenUp != model.MailRejected || dead.Reply != 550 {
		t.Fatalf("the dead mail %+v, want rejected after one attempt with reply 550", dead)
	}
	if strings.Contains(dead.Error, "no such user") || dead.Error == "" {
		t.Errorf("the mailbox's error %q: empty, or the server's words", dead.Error)
	}
	b := bounceOf(t, got)
	if b.ID != id || b.Mailer != bounceMailer || b.GivenUp != model.MailRejected || b.Attempts != 1 || b.Message.Subject != bounceSubject {
		t.Errorf("the bounce %+v", b)
	}
	if b.Reply == nil || b.Reply.Code != 550 || !strings.Contains(b.Reply.Msg, refusalWords) {
		t.Errorf("the hook's reply %+v, want the server's 550 and its words", b.Reply)
	}
	if b.Err == nil || !b.QueuedAt.Equal(dead.QueuedAt) || b.At.IsZero() {
		t.Errorf("the bounce's failure %v, queued %v, at %v", b.Err, b.QueuedAt, b.At)
	}
	if info := a.Graph().Node(bounceMailer).Mailer; *info.DeadLetters != 1 || *info.Queued != 0 {
		t.Errorf("mailer info %+v", info)
	}
}

// A 4xx reply is retried, and the attempt that fails once the mail is older
// than the mailer's max-age — a setting, here 2s — gives it up as expired,
// with attempts to spare. The clock moves only between attempts.
func TestAMailOlderThanItsMaxAgeIsGivenUp(t *testing.T) {
	hook, got := hooked()
	clk := newBounceClock()
	a, m := bounceApp(t, startRefusingRelay(t, "451 4.3.0 try later"), clk, []app.MailerConfigurer{app.MailAttempts(5), hook},
		app.Set("mail-max-age", "2s"))
	stream := events(t, a)
	id := sendBounce(t, m)
	for attempt, backoff := range []time.Duration{time.Second, 2 * time.Second} {
		s := mailStatuses(t, stream, id, model.MailRetrying, model.MailDead)
		if last := s[len(s)-1]; last.Status != model.MailRetrying || last.Attempts != attempt+1 || last.Reply != 451 {
			t.Fatalf("attempt %d: %+v, want retrying with reply 451", attempt+1, last)
		}
		clk.Advance(backoff)
	}
	s := mailStatuses(t, stream, id, model.MailDead, model.MailSent)
	dead := s[len(s)-1]
	if dead.Status != model.MailDead || dead.Attempts != 3 || dead.GivenUp != model.MailExpired || dead.Reply != 451 {
		t.Fatalf("the dead mail %+v, want expired after three attempts", dead)
	}
	if b := bounceOf(t, got); b.ID != id || b.GivenUp != model.MailExpired || b.Attempts != 3 || b.Reply == nil || b.Reply.Code != 451 {
		t.Errorf("the bounce %+v", b)
	}
}

// With <mailer>-retry-permanent, an operator's setting, a 5xx is retried like
// a 4xx, and the mail is given up as exhausted after its last attempt.
func TestRetryPermanentRetriesARejectedMail(t *testing.T) {
	hook, got := hooked()
	clk := newBounceClock()
	a, m := bounceApp(t, startRefusingRelay(t, "550 5.7.1 "+refusalWords), clk, []app.MailerConfigurer{app.MailAttempts(2), hook},
		app.Set("mail-retry-permanent", true))
	stream := events(t, a)
	id := sendBounce(t, m)
	s := mailStatuses(t, stream, id, model.MailRetrying, model.MailDead)
	if last := s[len(s)-1]; last.Status != model.MailRetrying || last.Attempts != 1 || last.GivenUp != "" {
		t.Fatalf("the first attempt: %+v, want retrying", last)
	}
	clk.Advance(time.Second)
	s = mailStatuses(t, stream, id, model.MailDead, model.MailSent)
	if dead := s[len(s)-1]; dead.Status != model.MailDead || dead.Attempts != 2 || dead.GivenUp != model.MailExhausted || dead.Reply != 550 {
		t.Fatalf("the dead mail %+v, want exhausted after two attempts", dead)
	}
	if b := bounceOf(t, got); b.GivenUp != model.MailExhausted || b.Attempts != 2 {
		t.Errorf("the bounce %+v", b)
	}
}

// A hook may send mail: it runs out of the spool's observer, so the mail it
// sends is queued — and, refused in turn, given up and told to the hook —
// instead of waiting forever on the observer the bounce came from. Its context
// is on the mailer's node.
func TestABounceHookMaySendMail(t *testing.T) {
	got := make(chan *app.BounceEvent, 4)
	var logs syncBuffer
	var m *app.Mailer
	hook := app.OnBounce(func(ctx context.Context, b *app.BounceEvent) {
		logger.Info(ctx, app.Log(ctx), "the hook heard of "+b.Message.Subject)
		if b.Message.Subject == bounceSubject {
			if _, err := m.Send(ctx, mail.Message{To: []mail.Address{{Addr: bounceTo}}, Subject: "Fallback", Text: "Hi again"}); err != nil {
				panic(err)
			}
		}
		got <- b
	})
	a, mailer := bounceApp(t, startRefusingRelay(t, "550 5.1.1 "+refusalWords), newBounceClock(), []app.MailerConfigurer{hook}, app.Logs(&logs))
	m = mailer
	stream := events(t, a)
	id := sendBounce(t, m)
	mailStatuses(t, stream, id, model.MailDead, model.MailSent)
	first, second := bounceOf(t, got), bounceOf(t, got)
	if first.Message.Subject != bounceSubject || second.Message.Subject != "Fallback" || second.GivenUp != model.MailRejected {
		t.Errorf("the bounces %+v then %+v", first, second)
	}
	for _, line := range strings.Split(logs.String(), "\n") {
		if strings.Contains(line, "the hook heard of") && !strings.Contains(line, `node="`+bounceMailer+`"`) {
			t.Errorf("the hook's log is on no mailer's node: %s", line)
		}
	}
	if n := strings.Count(logs.String(), "the hook heard of"); n != 2 {
		t.Errorf("the hook logged %d lines through app.Log, want 2:\n%s", n, logs.String())
	}
}

// Without a hook of the product's, a mail given up is logged as an error that
// names why and the reply's code, never the server's words; a hook that
// panics is logged, and the outbox goes on.
func TestABounceIsLoggedUnlessTheProductSaysOtherwise(t *testing.T) {
	t.Run("the framework's hook", func(t *testing.T) {
		var logs syncBuffer
		a, m := bounceApp(t, startRefusingRelay(t, "550 5.1.1 "+refusalWords), newBounceClock(), nil, app.Logs(&logs))
		stream := events(t, a)
		id := sendBounce(t, m)
		mailStatuses(t, stream, id, model.MailDead, model.MailSent)
		eventually(t, "the bounce to be logged", func() bool { return strings.Contains(logs.String(), "a mail was given up: rejected") })
		line := logs.String()
		if !strings.Contains(line, "reply=550") || !strings.Contains(line, id) {
			t.Errorf("the log lacks the reply or the mail:\n%s", line)
		}
		if strings.Contains(line, "no such user") {
			t.Errorf("the log quotes the server's words:\n%s", line)
		}
	})
	t.Run("a hook that panics", func(t *testing.T) {
		var logs syncBuffer
		a, m := bounceApp(t, startRefusingRelay(t, "550 5.1.1 "+refusalWords), newBounceClock(),
			[]app.MailerConfigurer{app.OnBounce(func(context.Context, *app.BounceEvent) { panic("the product's bug") })}, app.Logs(&logs))
		stream := events(t, a)
		first := sendBounce(t, m)
		mailStatuses(t, stream, first, model.MailDead, model.MailSent)
		second := sendBounce(t, m)
		mailStatuses(t, stream, second, model.MailDead, model.MailSent)
		eventually(t, "the panics to be logged", func() bool {
			return strings.Count(logs.String(), "a mailer's bounce hook panicked") == 2
		})
		if strings.Contains(logs.String(), "a mail was given up") {
			t.Error("the framework's hook ran beside the product's")
		}
	})
}

// A mailer declares <mailer>-max-age, 5 days unless MailMaxAge says
// otherwise, and <mailer>-retry-permanent, false; a negative age is refused,
// declared or configured.
func TestAMailerDeclaresItsSettings(t *testing.T) {
	t.Run("defaults", func(t *testing.T) {
		a, _ := bounceApp(t, startRefusingRelay(t, "550 5.1.1 no"), newBounceClock(), nil)
		cfg := settingsOf(a)
		if cfg["mail-max-age"] != (120*time.Hour).String() || cfg["mail-retry-permanent"] != "false" {
			t.Errorf("the mailer's settings %v", cfg)
		}
	})
	t.Run("MailMaxAge", func(t *testing.T) {
		a, _ := bounceApp(t, startRefusingRelay(t, "550 5.1.1 no"), newBounceClock(), []app.MailerConfigurer{app.MailMaxAge(48 * time.Hour)})
		if got := settingsOf(a)["mail-max-age"]; got != (48 * time.Hour).String() {
			t.Errorf("mail-max-age %q", got)
		}
	})
	for name, c := range map[string]struct {
		mailer []app.MailerConfigurer
		opts   []app.AppConfigurer
	}{
		"declared":   {mailer: []app.MailerConfigurer{app.MailMaxAge(-time.Hour)}},
		"configured": {opts: []app.AppConfigurer{app.Set("mail-max-age", "-1h")}},
	} {
		t.Run("negative, "+name, func(t *testing.T) {
			t.Setenv("BOUNCE_SMTP_URL", "smtp://127.0.0.1:25?tls=none")
			svc := app.NewService("bounce", "")
			svc.Mailer("mail", c.mailer...)
			err := app.NewApp("bounce", svc).With(append([]app.AppConfigurer{app.InMemory(), app.Listen("127.0.0.1:0"), app.Logs(io.Discard)}, c.opts...)...).Start(t.Context())
			if err == nil || !strings.Contains(err.Error(), `its max-age must be a duration of zero or more`) {
				t.Fatalf("Start = %v", err)
			}
		})
	}
}

// settingsOf is each setting of the running app by name, its value as the
// runtime graph renders it.
func settingsOf(a *app.App) map[string]string {
	out := map[string]string{}
	g := a.Graph()
	if g.Runtime == nil {
		return out
	}
	for _, s := range g.Runtime.Config {
		out[s.Key] = s.Value
	}
	return out
}
