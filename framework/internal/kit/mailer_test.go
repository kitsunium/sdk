package kit_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/textproto"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kitsunium/sdk/framework/internal/kit"
	"github.com/kitsunium/sdk/framework/model"
	"github.com/kitsunium/sdk/pkg/v1/clock"
	"github.com/kitsunium/sdk/pkg/v1/mail"
)

// relay is an SMTP server for the tests: plain text, no TLS, no AUTH, and it
// accepts every message.
type relay struct {
	ln  net.Listener
	mu  sync.Mutex
	got []string
}

func startRelay(t *testing.T) *relay {
	t.Helper()
	ln, lnErr := net.Listen("tcp", "127.0.0.1:0")
	if lnErr != nil {
		t.Fatal(lnErr)
	}
	r := &relay{ln: ln}
	var wg sync.WaitGroup
	wg.Go(func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			wg.Go(r.serving(c))
		}
	})
	t.Cleanup(func() {
		ln.Close()
		wg.Wait()
	})
	return r
}

// serving is serve on c, as a function a wait group runs.
func (r *relay) serving(c net.Conn) func() { return func() { r.serve(c) } }

func (r *relay) serve(c net.Conn) {
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
		switch strings.ToUpper(verb) {
		case "EHLO", "HELO":
			if err := tp.PrintfLine("250 relay.test"); err != nil {
				return
			}
		case "MAIL", "RCPT", "RSET", "NOOP":
			if err := tp.PrintfLine("250 OK"); err != nil {
				return
			}
		case "DATA":
			if err := tp.PrintfLine("354 go ahead"); err != nil {
				return
			}
			data, err := tp.ReadDotBytes()
			if err != nil {
				return
			}
			r.mu.Lock()
			r.got = append(r.got, string(data))
			r.mu.Unlock()
			if err := tp.PrintfLine("250 OK queued"); err != nil {
				return
			}
		case "QUIT":
			if err := tp.PrintfLine("221 bye"); err != nil {
				return
			}
			return
		default:
			if err := tp.PrintfLine("502 unknown command"); err != nil {
				return
			}
		}
	}
}

func (r *relay) addr() string { return r.ln.Addr().String() }

func (r *relay) messages() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.got)
}

// refusedAddr is an address nothing listens on: every connection is refused
// at once.
func refusedAddr(t *testing.T) string {
	t.Helper()
	ln, lnErr := net.Listen("tcp", "127.0.0.1:0")
	if lnErr != nil {
		t.Fatal(lnErr)
	}
	addr := ln.Addr().String()
	ln.Close()
	return addr
}

// syncBuffer collects logs written from several goroutines.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// mailApp runs the members service alone, with KIT_SMTP_URL set to url.
func mailApp(t *testing.T, url string, opts ...kit.AppConfigurer) *kit.App {
	t.Helper()
	t.Setenv("KIT_SMTP_URL", url)
	app := kit.NewApp("mail", Members).With(append([]kit.AppConfigurer{
		kit.InMemory(), kit.Listen("127.0.0.1:0"), kit.Env(kit.EnvDev), kit.Analyze(false), kit.Logs(io.Discard),
	}, opts...)...)
	if err := app.Start(t.Context()); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := app.Stop(ctx); err != nil {
			t.Errorf("stop: %v", err)
		}
	})
	return app
}

// events streams the app's live events until the test ends.
//
// Goroutine lifecycle: one goroutine reads the event stream into the channel
// and closes it when the stream ends — the test's context ends the request.
func events(t *testing.T, app *kit.App) <-chan model.Event {
	t.Helper()
	req, reqErr := http.NewRequestWithContext(t.Context(), "GET", app.URL()+"/_kit/api/events", nil)
	if reqErr != nil {
		t.Fatal(reqErr)
	}
	resp, respErr := http.DefaultClient.Do(req)
	if respErr != nil {
		t.Fatal(respErr)
	}
	out := make(chan model.Event, 1024)
	go func() {
		defer close(out)
		defer resp.Body.Close()
		dec := &sseReader{r: resp.Body}
		for {
			raw, err := dec.next()
			if err != nil {
				return
			}
			var e model.Event
			if json.Unmarshal(raw, &e) == nil {
				select {
				case out <- e:
				default:
				}
			}
		}
	}()
	if hello := <-out; hello.Type != model.EventHello {
		t.Fatalf("first event %+v", hello)
	}
	return out
}

// mailStatuses reads the statuses a mail goes through, until it reaches one
// of the final ones.
func mailStatuses(t *testing.T, stream <-chan model.Event, id string, final ...string) []model.MailSummary {
	t.Helper()
	var seen []model.MailSummary
	timeout := time.After(10 * time.Second)
	for {
		select {
		case e, ok := <-stream:
			if !ok {
				t.Fatalf("the stream ended after %+v", seen)
			}
			if e.Type != model.EventMail || e.Mail.ID != id {
				continue
			}
			seen = append(seen, *e.Mail)
			if slices.Contains(final, e.Mail.Status) {
				return seen
			}
		case <-timeout:
			t.Fatalf("mail %s: only %+v", id, seen)
		}
	}
}

// advancer is a clock a test moves.
type advancer interface {
	Advance(d time.Duration)
}

// driveClock advances clk by step every few milliseconds until the returned
// function is called — which waits for the driver to stop.
func driveClock(clk advancer, step time.Duration) (stop func()) {
	quit := make(chan struct{})
	var wg sync.WaitGroup
	wg.Go(func() {
		for {
			select {
			case <-quit:
				return
			case <-time.After(3 * time.Millisecond):
				clk.Advance(step)
			}
		}
	})
	var once sync.Once
	return func() {
		once.Do(func() {
			close(quit)
			wg.Wait()
		})
	}
}

func loopNamed(g *model.Graph, name string) *model.Loop {
	if g.Runtime == nil {
		return nil
	}
	for i := range g.Runtime.Loops {
		if g.Runtime.Loops[i].Name == name {
			return &g.Runtime.Loops[i]
		}
	}
	return nil
}

func TestMailIsQueuedThenDelivered(t *testing.T) {
	app := start(t)
	stream := events(t, app)
	token := signIn(t, app, "acc_ann", "ann@x.dev", "Ann")
	r := call(t, app, "POST /invite", InviteInput{Email: "guest@example.org", Subject: "Join us"}, "Cookie", "sid="+token)
	var inv Invited
	r.json(t, &inv)
	if r.status != http.StatusOK || !strings.HasPrefix(inv.Mail, "mail_") {
		t.Fatalf("invite: %d %s", r.status, r.body)
	}
	statuses := mailStatuses(t, stream, inv.Mail, model.MailSent, model.MailDead)
	if len(statuses) != 2 || statuses[0].Status != model.MailQueued || statuses[1].Status != model.MailSent || statuses[1].Attempts != 1 {
		t.Fatalf("statuses %+v", statuses)
	}
	captured := Mail.Captured()
	if len(captured) != 1 {
		t.Fatalf("captured %d mails", len(captured))
	}
	m := captured[0]
	if m.ID != inv.Mail || m.Status != model.MailSent || m.From != "Members <members@example.com>" ||
		!slices.Equal(m.To, []string{"Guest <guest@example.org>"}) || m.Subject != "Join us" || m.SentAt == nil ||
		m.Node != "members/endpoint/Invite" || m.TraceID == "" || m.Mailer != "members/mailer/mail" {
		t.Errorf("captured %+v", m.MailSummary)
	}
	if !strings.Contains(m.Text, "join?token=tok_42") || !strings.Contains(m.HTML, "Ann invites you") {
		t.Errorf("bodies %q %q", m.Text, m.HTML)
	}
	wantID := "<" + inv.Mail + "@example.com>"
	if m.Headers["Message-ID"] != wantID || !strings.Contains(m.Raw, "Message-Id: "+wantID) && !strings.Contains(m.Raw, "Message-ID: "+wantID) {
		t.Errorf("Message-ID: header %q, raw:\n%s", m.Headers["Message-ID"], m.Raw)
	}
	if !strings.Contains(m.Raw, "Subject: Join us") {
		t.Errorf("raw:\n%s", m.Raw)
	}

	g := app.Graph()
	info := g.Node("members/mailer/mail").Mailer
	if info.Transport != model.TransportCapture || info.From != "Members <members@example.com>" || info.MaxAttempts != 3 ||
		info.Outbox != "memory" || info.Sent == nil || *info.Sent != 1 || *info.Queued != 0 || info.DeadLetters == nil || *info.DeadLetters != 0 {
		t.Errorf("mailer info %+v", info)
	}
	if e := g.Edge("members/endpoint/Invite|sends|members/mailer/mail"); e == nil || e.Observed == nil {
		t.Error("the send is not drawn")
	}
	loop := loopNamed(g, "members/mailer/mail outbox")
	if loop == nil || loop.Kind != model.LoopConsumer || loop.Provenance != model.ProvenanceLibrary || loop.Library != "sdk/v1/mail" ||
		loop.Runs != 1 || loop.State != model.LoopWaiting {
		t.Errorf("outbox loop %+v", loop)
	}
	// The delivery continues the trace of the request that sent the mail,
	// which stays the request's.
	r = call(t, app, "GET /_kit/api/traces?root=members/endpoint/Invite", noBody)
	var traces []model.Trace
	r.json(t, &traces)
	if len(traces) != 1 || traces[0].TraceID != m.TraceID {
		t.Fatalf("traces of /invite: %s", r.body)
	}
	ops := map[string]model.Span{}
	for _, s := range traces[0].Spans {
		ops[s.Op] = s
	}
	if s := ops[model.OpSend]; s.Node != "members/mailer/mail" || s.From != "members/endpoint/Invite" || s.Edge != model.EdgeSends || s.Attrs["mail"] != inv.Mail {
		t.Errorf("send span %+v", s)
	}
	if s := ops[model.OpDeliverMail]; s.Node != "members/mailer/mail" || s.Status != model.StatusOK || s.Attrs["attempt"] != "1" {
		t.Errorf("delivery span %+v", s)
	}
}

func TestSendRefusesBadMail(t *testing.T) {
	app := start(t)
	token := signIn(t, app, "acc_bad", "bad@x.dev", "Bad")
	for _, c := range []struct {
		name string
		in   InviteInput
		want string
	}{
		{"header injection", InviteInput{Email: "guest@example.org", Subject: "Hi\r\nBcc: evil@example.com"}, "A message header contains a line break and was refused"},
		{"unusable address", InviteInput{Email: "not an address", Subject: "Hi"}, "A mail address in that message is not usable"},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := call(t, app, "POST /invite", c.in, "Cookie", "sid="+token)
			if r.status != http.StatusBadRequest || r.errorCode(t) != kit.WireInvalid || !strings.Contains(string(r.body), c.want) {
				t.Fatalf("%d %s", r.status, r.body)
			}
			if strings.Contains(string(r.body), "evil@example.com") || strings.Contains(string(r.body), "not an address") {
				t.Errorf("the refusal echoes the value: %s", r.body)
			}
		})
	}
	_, err := Mail.Send(t.Context(), mail.Message{To: []mail.Address{{Addr: "a@example.org"}}, Subject: "empty"})
	var ke *kit.Error
	if !errors.As(err, &ke) || ke.Code != kit.WireInvalid {
		t.Errorf("an empty body: %v", err)
	}
	time.Sleep(100 * time.Millisecond)
	if n := len(Mail.Captured()); n != 0 {
		t.Errorf("a refused mail was delivered: %d", n)
	}
	if g := app.Graph(); *g.Node("members/mailer/mail").Mailer.Queued != 0 {
		t.Error("a refused mail was queued")
	}
}

// A failing transport: each attempt fails, the next waits a doubling backoff
// on the app's clock, and the last one dead-letters the mail.
func TestMailRetriesThenDeadLetters(t *testing.T) {
	clk := clock.NewManualClock(time.Date(2026, 9, 24, 9, 0, 0, 0, time.UTC))
	app := mailApp(t, "smtp://"+refusedAddr(t)+"?tls=none", kit.Clock(clk))
	stream := events(t, app)
	id, idErr := Mail.Send(t.Context(), mail.Message{To: []mail.Address{{Addr: "guest@example.org"}}, Subject: "Hello", Text: "Hi"})
	if idErr != nil {
		t.Fatal(idErr)
	}
	stop := driveClock(clk, 250*time.Millisecond)
	statuses := mailStatuses(t, stream, id, model.MailDead, model.MailSent)
	stop()
	var got []string
	for _, s := range statuses {
		got = append(got, s.Status)
	}
	if !slices.Equal(got, []string{model.MailQueued, model.MailRetrying, model.MailRetrying, model.MailDead}) {
		t.Fatalf("statuses %v", got)
	}
	last := statuses[len(statuses)-1]
	if last.Attempts != 3 || last.Error != "The mail server could not be reached" {
		t.Errorf("the dead mail %+v", last)
	}
	if !statuses[3].QueuedAt.Equal(statuses[0].QueuedAt) {
		t.Errorf("a retry changed the queue time: %v", statuses[3].QueuedAt)
	}
	g := app.Graph()
	info := g.Node("members/mailer/mail").Mailer
	if info.Transport != model.TransportSMTP || info.TLS != "none" || *info.DeadLetters != 1 || *info.Queued != 0 || *info.Sent != 0 {
		t.Errorf("mailer info %+v", info)
	}
	if loop := loopNamed(g, "members/mailer/mail outbox"); loop == nil || loop.Runs != 3 || loop.Errors != 3 {
		t.Errorf("outbox loop %+v", loop)
	}
}

func TestMailBackoffDoubles(t *testing.T) {
	clk := clock.NewManualClock(time.Date(2026, 9, 24, 9, 0, 0, 0, time.UTC))
	app := mailApp(t, "smtp://"+refusedAddr(t)+"?tls=none", kit.Clock(clk))
	id, idErr := Mail.Send(t.Context(), mail.Message{To: []mail.Address{{Addr: "guest@example.org"}}, Subject: "Hello", Text: "Hi"})
	if idErr != nil {
		t.Fatal(idErr)
	}
	runs := func() int64 { return loopNamed(app.Graph(), "members/mailer/mail outbox").Runs }
	step := func(d time.Duration, want int64) {
		t.Helper()
		deadline := time.Now().Add(2 * time.Second)
		for {
			clk.Advance(d / 10)
			time.Sleep(2 * time.Millisecond)
			if runs() == want {
				return
			}
			if time.Now().After(deadline) {
				t.Fatalf("%s: %d runs, want %d", id, runs(), want)
			}
		}
	}
	step(100*time.Millisecond, 1) // the first attempt, at the next poll
	before := clk.Now()
	step(time.Second, 2)
	if waited := clk.Now().Sub(before); waited < time.Second || waited > 1500*time.Millisecond {
		t.Errorf("the second attempt came %s after the first", waited)
	}
	before = clk.Now()
	step(2*time.Second, 3)
	if waited := clk.Now().Sub(before); waited < 2*time.Second || waited > 2500*time.Millisecond {
		t.Errorf("the third attempt came %s after the second", waited)
	}
}

// With KIT_SMTP_URL, the SDK's SMTP transport delivers to the relay, and the
// mailer keeps summaries only.
func TestMailThroughSMTP(t *testing.T) {
	rel := startRelay(t)
	app := mailApp(t, "smtp://"+rel.addr()+"?tls=none")
	id, idErr := Mail.Send(t.Context(), mail.Message{To: []mail.Address{{Addr: "guest@example.org"}}, Subject: "Hello relay", Text: "Hi"})
	if idErr != nil {
		t.Fatal(idErr)
	}
	eventually(t, "the relay to receive the mail", func() bool { return len(rel.messages()) == 1 })
	raw := rel.messages()[0]
	if !strings.Contains(raw, "Subject: Hello relay") || !strings.Contains(raw, id+"@example.com") {
		t.Errorf("the relay received:\n%s", raw)
	}
	eventually(t, "the mail to be marked sent", func() bool {
		s := app.Graph().Node("members/mailer/mail").Mailer.Sent
		return s != nil && *s == 1
	})
	info := app.Graph().Node("members/mailer/mail").Mailer
	if info.Transport != model.TransportSMTP || info.Server != rel.addr() || info.TLS != "none" {
		t.Errorf("mailer info %+v", info)
	}
	if Mail.Captured() != nil {
		t.Error("SMTP kept the bodies")
	}
}

// The SMTP password never reaches a diagnostic, the graph, an event or a log
// line — refused configuration or failing delivery alike.
func TestMailCredentialsNeverLeak(t *testing.T) {
	const user, secret = "postmaster", "s3cr3t-p4ss"
	rel := startRelay(t)
	leaks := func(where, text string) {
		t.Helper()
		if strings.Contains(text, secret) || strings.Contains(text, user) {
			t.Errorf("%s discloses the credentials:\n%s", where, text)
		}
	}
	leaksJSON := func(where string, v any) {
		t.Helper()
		raw, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		leaks(where, string(raw))
	}

	// Credentials over an unencrypted session: refused before starting.
	var logs syncBuffer
	t.Setenv("KIT_SMTP_URL", "smtp://"+user+":"+secret+"@"+rel.addr()+"?tls=none")
	app := kit.NewApp("mail", Members).With(kit.InMemory(), kit.Listen("127.0.0.1:0"), kit.Env(kit.EnvDev), kit.Analyze(false), kit.Logs(&logs))
	err := app.Start(t.Context())
	if err == nil || !strings.Contains(err.Error(), "Credentials cannot be sent over an unencrypted mail session") {
		t.Fatalf("Start = %v", err)
	}
	leaks("the start error", err.Error())
	leaksJSON("the graph", app.Graph())

	// STARTTLS required, and the relay offers none: every attempt fails.
	t.Setenv("KIT_SMTP_URL", "smtp://"+user+":"+secret+"@"+rel.addr()+"?tls=starttls")
	app = kit.NewApp("mail", Members).With(kit.InMemory(), kit.Listen("127.0.0.1:0"), kit.Env(kit.EnvDev), kit.Analyze(false), kit.Logs(&logs))
	if err := app.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	defer app.Stop(context.Background())
	stream := events(t, app)
	id, idErr := Mail.Send(t.Context(), mail.Message{To: []mail.Address{{Addr: "guest@example.org"}}, Subject: "Hello", Text: "Hi"})
	if idErr != nil {
		t.Fatal(idErr)
	}
	statuses := mailStatuses(t, stream, id, model.MailRetrying, model.MailDead, model.MailSent)
	if last := statuses[len(statuses)-1]; last.Status != model.MailRetrying || last.Error != "The mail server did not offer the encryption this transport requires" {
		t.Fatalf("statuses %+v", statuses)
	}
	g := app.Graph()
	leaksJSON("the graph", g)
	if info := g.Node("members/mailer/mail").Mailer; info.Server != rel.addr() || info.TLS != "starttls" {
		t.Errorf("mailer info %+v", info)
	}
	for _, e := range statuses {
		leaksJSON("an event", e)
	}
	leaks("the logs", logs.String())
	if !strings.Contains(logs.String(), "a mail delivery failed") {
		t.Errorf("the failure was not logged:\n%s", logs.String())
	}
}

// A mail queued in a file outbox outlives the process: the next run delivers
// it.
func TestMailOutboxSurvivesARestart(t *testing.T) {
	dir := t.TempDir()
	run := func(url string, fn func(app *kit.App)) {
		t.Helper()
		t.Setenv("KIT_SMTP_URL", url)
		app := kit.NewApp("mail", Members).With(kit.DataDir(dir), kit.Listen("127.0.0.1:0"), kit.Env(kit.EnvDev), kit.Analyze(false), kit.Logs(io.Discard))
		if err := app.Start(t.Context()); err != nil {
			t.Fatal(err)
		}
		fn(app)
		if err := app.Stop(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	var id string
	run("smtp://"+refusedAddr(t)+"?tls=none", func(app *kit.App) {
		var idErr error
		id, idErr = Mail.Send(t.Context(), mail.Message{To: []mail.Address{{Addr: "guest@example.org"}}, Subject: "Survivor", Text: "Hi"})
		if idErr != nil {
			t.Fatal(idErr)
		}
		eventually(t, "the first attempt", func() bool {
			l := loopNamed(app.Graph(), "members/mailer/mail outbox")
			return l != nil && l.Errors >= 1
		})
		if info := app.Graph().Node("members/mailer/mail").Mailer; info.Outbox != "file" {
			t.Errorf("outbox %q", info.Outbox)
		}
	})
	run("", func(app *kit.App) {
		eventually(t, "the delivery by the next run", func() bool { return len(Mail.Captured()) == 1 })
		m := Mail.Captured()[0]
		if m.ID != id || m.Subject != "Survivor" || m.Attempts != 2 {
			t.Errorf("delivered %+v", m.MailSummary)
		}
	})
}

func TestMailDeclarationProblems(t *testing.T) {
	const secret = "pa55w0rd"
	for _, c := range []struct{ url, want string }{
		{"http://u:" + secret + "@mail.example.com", "scheme is not smtp or smtps"},
		{"smtp://u:" + secret + "@mail.example.com?tls=maybe", "tls parameter that is not starttls, implicit or none"},
		{"smtp://u:" + secret + "@mail.example.com:99999", "port that is not a number from 1 to 65535"},
		{"smtp://:" + secret + "@mail.example.com", "Password is set without a Username"},
		{"smtp://u:" + secret + "@mail.example.com/inbox", "has a path or a fragment"},
		{"smtp://u:" + secret + "@mail.example.com?password=" + secret, "has a query parameter other than tls"},
		{"smtp://u:" + secret + "@mail.example.com:x25", "not a URL of the form"},
		{"smtp://u:" + secret + "@mail.example.com?tls=none", "Credentials cannot be sent over an unencrypted mail session"},
	} {
		t.Run(c.want, func(t *testing.T) {
			t.Setenv("KIT_SMTP_URL", c.url)
			app := kit.NewApp("mail", Members).With(kit.InMemory(), kit.Listen("127.0.0.1:0"), kit.Logs(io.Discard))
			err := app.Start(t.Context())
			if err == nil {
				app.Stop(context.Background())
			}
			if err == nil || !strings.Contains(err.Error(), "mailer members/mailer/mail cannot deliver: KIT_SMTP_URL") || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("Start = %v", err)
			}
			if strings.Contains(err.Error(), secret) {
				t.Errorf("the problem quotes the password: %v", err)
			}
		})
	}
	t.Run("capture outside dev is a warning", func(t *testing.T) {
		t.Setenv("KIT_SMTP_URL", "")
		app := kit.NewApp("mail", Members).With(kit.InMemory(), kit.Listen("127.0.0.1:0"), kit.Env(kit.EnvProduction), kit.Logs(io.Discard))
		if err := app.Start(t.Context()); err != nil {
			t.Fatal(err)
		}
		defer app.Stop(context.Background())
		var found bool
		for _, d := range app.Graph().Diagnostics {
			found = found || (d.Severity == "warning" && d.Node == "members/mailer/mail" && strings.Contains(d.Message, "set KIT_SMTP_URL"))
		}
		if !found {
			t.Errorf("no warning: %+v", app.Graph().Diagnostics)
		}
	})
	t.Run("options", func(t *testing.T) {
		svc := kit.NewService("bad-mailers", "")
		svc.Mailer("zero", kit.MailAttempts(0))
		svc.Mailer("sender", kit.From("Nobody", "not an address"))
		err := kit.NewApp("x", svc).With(kit.InMemory(), kit.Listen("127.0.0.1:0"), kit.Logs(io.Discard)).Start(t.Context())
		for _, want := range []string{`mailer "zero" needs MailAttempts ≥ 1`, `mailer "sender": the sender "not an address" is refused`} {
			if err == nil || !strings.Contains(err.Error(), want) {
				t.Errorf("Start lacks %q: %v", want, err)
			}
		}
	})
}
