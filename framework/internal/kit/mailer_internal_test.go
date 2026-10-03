package kit

import (
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/kitsunium/sdk/framework/model"
	"github.com/kitsunium/sdk/pkg/v1/app/mail"
)

func TestParseSMTPURL(t *testing.T) {
	for _, c := range []struct {
		raw               string
		server, tls, user string
		password, problem string
		mode              mail.TLSMode
	}{
		{raw: "smtp://mail.example.com", server: "mail.example.com:587", tls: "starttls", mode: mail.TLSStartTLS},
		{raw: "smtps://mail.example.com", server: "mail.example.com:465", tls: "implicit", mode: mail.TLSImplicit},
		{raw: "smtp://relay.internal?tls=none", server: "relay.internal:25", tls: "none", mode: mail.TLSDisabled},
		{raw: "SMTP://u%40x:p%2Fw%3Fd@[::1]:2525?tls=IMPLICIT", server: "[::1]:2525", tls: "implicit", user: "u@x", password: "p/w?d", mode: mail.TLSImplicit},
		{raw: "smtp://user:pass@mail.example.com:587/", server: "mail.example.com:587", tls: "starttls", user: "user", password: "pass", mode: mail.TLSStartTLS},
		{raw: "smtp://mail.example.com:0", problem: "is not usable: has a port that is not a number from 1 to 65535"},
		{raw: "smtp:mail.example.com", problem: "is not usable: not a URL of the form smtp://user:password@host:port"},
		{raw: "smtp://mail.example.com#frag", problem: "is not usable: has a path or a fragment"},
		{raw: "smtp://u:p@relay.internal?tls=none", problem: "is refused: Credentials cannot be sent over an unencrypted mail session (Username is set and TLS is disabled)"},
	} {
		t.Run(c.raw, func(t *testing.T) {
			cfg, said := parseSMTPURL(c.raw)
			if problem := said.String(); problem != c.problem {
				t.Fatalf("problem %q, want %q", problem, c.problem)
			}
			if !said.empty() {
				return
			}
			if smtpServer(&cfg) != c.server || tlsName(cfg.TLS) != c.tls || cfg.TLS != c.mode || cfg.Username != c.user || cfg.Password != c.password {
				t.Errorf("parsed %s %s %v %q, want %s %s %v %q", smtpServer(&cfg), tlsName(cfg.TLS), cfg.TLS, cfg.Username, c.server, c.tls, c.mode, c.user)
			}
		})
	}
}

func TestMailBackoff(t *testing.T) {
	var got []time.Duration
	for attempt := 1; attempt <= 12; attempt++ {
		got = append(got, mailRetry.Delay(attempt))
	}
	want := []time.Duration{1, 2, 4, 8, 16, 32, 64, 128, 256, 300, 300, 300}
	for i := range want {
		want[i] *= time.Second
	}
	if !slices.Equal(got, want) {
		t.Fatalf("backoff %v, want %v", got, want)
	}
}

func TestMailboxRing(t *testing.T) {
	m := &Mailer{}
	m.ring.reset(true)
	for i := range mailboxSize + 5 {
		m.ring.add(model.MailMessage{ID: fmt.Sprint(i), Status: model.MailQueued, Text: "body"})
	}
	if len(m.ring.order) != mailboxSize || m.ring.mails["0"] != nil || m.ring.mails["4"] != nil || m.ring.mails["5"] == nil {
		t.Fatalf("the ring kept %d mails, oldest %s", len(m.ring.order), m.ring.order[0])
	}
	var ids []string
	for _, s := range m.mails(3) {
		ids = append(ids, s.ID)
	}
	if !slices.Equal(ids, []string{"204", "203", "202"}) {
		t.Errorf("mails(3) = %v, want the newest first", ids)
	}
	if got, ok := m.mail("204"); !ok || got.Text != "body" {
		t.Errorf("mail(204) = %+v %v", got, ok)
	}
	if _, ok := m.mail("0"); ok {
		t.Error("a forgotten mail was found")
	}
	if n := len(m.mails(0)); n != 0 {
		t.Errorf("mails(0) returned %d", n)
	}

	// Without the capture transport, the ring keeps summaries only.
	env := &mail.SpoolEvent{ID: "mail_1", QueuedAt: time.Now(), Message: mail.Message{
		From: mail.Address{Name: "A", Addr: "a@example.com"}, To: []mail.Address{{Addr: "b@example.com"}},
		Bcc: []mail.Address{{Addr: "hidden@example.com"}}, Subject: "S", Text: "secret body", HTML: "<p>secret</p>",
	}}
	smtp := (&Mailer{}).messageOf(env, false)
	if smtp.Text != "" || smtp.HTML != "" || smtp.Headers != nil || smtp.From != "A <a@example.com>" || !slices.Equal(smtp.To, []string{"b@example.com"}) {
		t.Errorf("an SMTP record %+v", smtp)
	}
	full := (&Mailer{}).messageOf(env, true)
	for k, v := range full.Headers {
		if k == "Bcc" || v == "hidden@example.com" {
			t.Errorf("a blind recipient reached the headers: %s: %s", k, v)
		}
	}
}
