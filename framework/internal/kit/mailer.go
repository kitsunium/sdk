package kit

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"net"
	"path/filepath"
	"runtime/debug"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/kitsunium/sdk/framework/model"
	"github.com/kitsunium/sdk/pkg/v1/app/mail"
	mailspool "github.com/kitsunium/sdk/pkg/v1/app/mail/spool"
	"github.com/kitsunium/sdk/pkg/v1/app/resilience"
	"github.com/kitsunium/sdk/pkg/v1/errs"
	"github.com/kitsunium/sdk/pkg/v1/observe/logger"
	"github.com/kitsunium/sdk/pkg/v1/observe/trace"
	"github.com/kitsunium/sdk/pkg/v1/security/secret"
)

// Limits of a mailer.
const (
	// mailboxSize is how many mails a mailer remembers for the Studio.
	mailboxSize int = 200
	// maxMailBytes bounds one queued mail, attachments included, as the
	// spool writes it.
	maxMailBytes = 32 << 20
	// maxRawMail bounds the composed message kept for the Studio.
	maxRawMail = 256 << 10
	// mailSendTimeout bounds one hand-over to the transport; the spool's
	// lease is twice it, so a slow relay never causes a duplicate.
	mailSendTimeout time.Duration = time.Minute
	// A failed attempt waits mailBackoffFirst, doubling up to mailBackoffMax.
	mailBackoffFirst time.Duration = time.Second
	mailBackoffMax   time.Duration = 5 * time.Minute
	// maxDeadLetters is how many dead letters of earlier runs a mailer counts
	// at its start.
	maxDeadLetters int = 1000
)

// What the spool keeps of a Send beside the mail: the trace the delivery
// continues, and the node that sent it.
const (
	metaTrace = "trace"
	metaNode  = "node"
)

// smtpSecret is the mail connector's secret: the SMTP URL.
const smtpSecret = "smtp-url"

// defaultMailAttempts is how often the outbox tries a mail before it
// dead-letters it, unless the mailer says otherwise.
const defaultMailAttempts int = 6

// mailRetry is the SDK's exponential backoff at the mailer's bounds: the
// wait after a failed attempt, one second, doubling, up to five minutes.
var mailRetry = resilience.Backoff{BaseDelay: mailBackoffFirst, MaxDelay: mailBackoffMax}

// Outbound mail: Send validates and queues. The outbox is the SDK's mail
// spool — on disk when the app has a data directory — which keeps the message
// until it hands it to the transport, retries a failure with a backoff, drops
// a redelivery of a mail already sent, and dead-letters a message after its
// last attempt. kit wraps the transport: each attempt is a span continuing
// the trace of the Send, a run of the outbox's loop, and where the Studio's
// simulation fails it. The mailbox the Studio reads is kept from what the
// spool reports.
//
// Credentials never leave the mailer. The SMTP URL is the mail connector's
// secret, smtp-url: KIT_SMTP_URL, or the file KIT_SMTP_URL_FILE names, or
// kit-smtp-url in the environment's store (secretstore.go). It is parsed where
// the transport is built and nowhere else; the graph, the Studio and the logs
// see the relay's host and port, the TLS mode and the sender — never the user
// or the password, and never the URL.

// Mailer is outbound mail: a durable outbox and the transport that empties
// it. [Mailer.Send] only queues — it returns once the message is safely in
// the outbox — and the mailer's own loop hands each message to the transport,
// retrying a failure and dead-lettering a message after its last attempt.
//
// The transport is the SDK's mail package: SMTP when the mail connector's
// secret smtp-url is set — KIT_SMTP_URL, smtp://user:password@host:587?tls=
// starttls|implicit|none — and otherwise the capture transport, which keeps
// every message for the Studio's mailbox and for tests ([Mailer.Captured])
// and delivers nothing. Outside dev, the capture transport is reported as a
// warning. The URL is read again before every delivery: replacing it where
// it lives — a new version in the store, a new file behind
// KIT_SMTP_URL_FILE — takes effect at the next mail.
type Mailer struct {
	nodeBase
	opts mailerOptions

	// What the running app resolved, guarded by mu.
	mu        sync.Mutex
	transport mail.Transport
	capture   mail.FullTransport // the capture transport; nil with SMTP
	// relay is what the SMTP transport was built from; problem, the last
	// problem found with the URL since, logged once.
	relay   smtpRelay
	problem string
	spool   *mailspool.Spool
	cancel  context.CancelFunc
	done    chan struct{}
	loop    *loopState
	ring    mailRing
	// composed holds what the capture transport composed for a mail, from
	// the attempt that sent it to the spool's report that it was sent.
	composed map[string][]byte
}

// MailerConfigurer configures a mailer.
type MailerConfigurer interface {
	mailerConfigure(o *mailerOptions)
}

type mailerOptions struct {
	from        mail.Address
	maxAttempts int
}

type mailerOption func(o *mailerOptions)

// mailSender keys the node that sent a mail in the context handed to the
// spool.
type mailSender struct{}

// mailbox is what the Studio's mailbox reads from a mailer. The capture
// transport keeps whole messages; SMTP keeps summaries only.
type mailbox interface {
	mails(limit int) []model.MailSummary
	mail(id string) (model.MailMessage, bool)
}

// mailRing is what a mailer remembers of its mails, guarded by Mailer.mu.
type mailRing struct {
	// full is set when bodies are kept: the capture transport.
	full bool
	// order lists the remembered mails, oldest first; mails holds them.
	order []string
	mails map[string]*model.MailMessage
	// pending holds the mails queued or retrying, whether the ring still
	// remembers them or not.
	pending map[string]bool
	// sent counts the mails the transport accepted since the start; dead, the
	// dead letters: those of earlier runs, when deadKnown — the spool could
	// read them at the start —, then each mail given up on since.
	sent, dead int
	deadKnown  bool
}

// smtpRelay is what an SMTP transport was built from: the URL — a secret,
// compared, never shown — and the relay it names.
type smtpRelay struct {
	url         secret.Value
	server, tls string
}

// outboxTransport is the transport the spool hands each attempt to: kit's
// view of the attempt — a span continuing the trace of the Send, a run of the
// outbox's loop, the Studio's simulation — around the mailer's transport as
// the SMTP URL is now.
type outboxTransport struct {
	m *Mailer
	a *App
}

// mailerConfigure sets the option on what it configures.
func (f mailerOption) mailerConfigure(o *mailerOptions) { f(o) }

// From sets the sender of every message that names none.
//
// IFACE-OPAQUE: the option is sealed — its method is unexported — so a caller
// only hands it to the declaration it configures.
func From(name, addr string) MailerConfigurer {
	return mailerOption(func(o *mailerOptions) { o.from = mail.Address{Name: name, Addr: addr} })
}

// MailAttempts is how many deliveries a message gets before it is
// dead-lettered. The default is 6, backing off from one second.
//
// IFACE-OPAQUE: the option is sealed — its method is unexported — so a caller
// only hands it to the declaration it configures.
func MailAttempts(n int) MailerConfigurer {
	return mailerOption(func(o *mailerOptions) { o.maxAttempts = n })
}

// Mailer declares outbound mail.
//
//go:noinline
func (s *Service) Mailer(name string, opts ...MailerConfigurer) *Mailer {
	m := NewMailer()
	for _, o := range opts {
		o.mailerConfigure(&m.opts)
	}
	m.kind, m.name, m.decl = model.KindMailer, name, callerPos()
	s.add(m, true)
	if m.opts.maxAttempts < 1 {
		s.problem(m.decl, m.id, "mailer.attempts", "name", name)
	}
	if from := m.opts.from; !from.IsZero() || from.Name != "" {
		if err := mail.Validate(mail.Message{From: from, To: []mail.Address{{Addr: "probe@example.com"}}, Text: "probe"}); err != nil {
			s.problem(m.decl, m.id, "mailer.sender", "name", name, "sender", clip(from.Addr), "detail", errs.PublicOf(err))
		}
	}
	return m
}

// Send validates msg with the SDK's mail rules — a header carrying a line
// break, an unusable address, an empty body are refused here, not at
// delivery — and puts it in the outbox. It returns the message's outbox ID.
// Inside a transaction ([Transact], a command's) the mail waits for the
// commit — a rollback drops it —, and the ID returned at once is the one
// the mail keeps.
//
// A message without a sender gets the mailer's ([From]); without a date, the
// app's time; without a Message-ID, one made of its outbox ID, which every
// attempt keeps, so a receiver can tell a retried delivery from a new mail.
func (m *Mailer) Send(ctx context.Context, msg mail.Message) (string, error) {
	a := m.app()
	if a == nil {
		return "", notRunning(&m.nodeBase)
	}
	caller := currentNode(ctx)
	ctx, sp := a.begin(ctx, &spanStart{node: m.id, from: caller, edge: model.EdgeSends, op: model.OpSend, name: "Send"})
	id, err := m.enqueue(context.WithValue(ctx, mailSender{}, caller), msg)
	if id != "" {
		sp.attr("mail", id)
	}
	sp.end(err)
	return id, err
}

// enqueue hands msg to the spool, which stamps it and says it was queued
// (observe) before it returns. A mail the SDK refuses is refused here, by
// the same verdict the spool would give, so that a mistake of the caller is
// an [Invalid] error and nothing else is.
func (m *Mailer) enqueue(ctx context.Context, msg mail.Message) (string, error) {
	if msg.From.IsZero() {
		msg.From = m.opts.from
	}
	if err := mail.Validate(msg); err != nil {
		return "", Invalid(errs.PublicOf(err)).Wrap(err)
	}
	m.mu.Lock()
	spool := m.spool
	m.mu.Unlock()
	if spool == nil {
		return "", Unavailable(fmt.Sprintf("mailer %q is not running", m.name))
	}
	if unitOf(ctx) != nil {
		// Minted now, kept by the mail once the transaction commits.
		id := NewID("mail")
		release := func() error {
			m.mu.Lock()
			spool := m.spool
			m.mu.Unlock()
			if spool == nil {
				return Unavailable(fmt.Sprintf("mailer %q is not running", m.name))
			}
			return m.spooled(spool.SendWithID(withoutUnit(context.WithoutCancel(ctx)), id, msg))
		}
		if hold(ctx, heldEffect{node: m.id, release: release}) {
			markSpan(ctx, m.id, "held", "commit")
			return id, nil
		}
	}
	id, err := spool.Send(ctx, msg)
	if err != nil {
		return "", m.spooled(err)
	}
	return id, nil
}

// spooled is the spool's refusal of a mail in kit's words: a mail the
// outbox cannot hold is the caller's to change, the rest kit's failure.
func (m *Mailer) spooled(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, mailspool.Closed):
		return Unavailable(fmt.Sprintf("mailer %q is not running", m.name))
	case errs.HasReason(err, "MESSAGE_TOO_LARGE"):
		return Invalid(fmt.Sprintf("the mail is larger than the outbox accepts (%s)", humanBytes(maxMailBytes))).Wrap(err)
	case errors.Is(err, mailspool.MessageUnencodable):
		return failure(CodeMailEncode, "MAIL_ENCODE", "the mail cannot be encoded for the outbox", err, errs.String("mailer", m.id))
	}
	return failure(CodeMailQueue, "MAIL_QUEUE", "the mail could not be put in the outbox", err, errs.String("mailer", m.id))
}

// annotate is what the spool keeps of a Send for its deliveries: the trace
// to continue and the node that sent the mail.
func annotate(ctx context.Context) map[string]string {
	meta := map[string]string{}
	if header, _ := trace.FormatTraceParent(trace.SpanContextFromContext(ctx)); header != "" {
		meta[metaTrace] = header
	}
	if caller, _ := ctx.Value(mailSender{}).(string); caller != "" {
		meta[metaNode] = caller
	}
	return meta
}

// Captured returns the messages the capture transport kept, oldest first:
// what a test reads a verification link from. It is empty with SMTP. A mail
// appears once the outbox has delivered it — shortly after Send.
func (m *Mailer) Captured() []model.MailMessage {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.ring.full {
		return nil
	}
	out := make([]model.MailMessage, 0, len(m.ring.order))
	for _, id := range m.ring.order {
		if rec := m.ring.mails[id]; rec.Status == model.MailSent {
			out = append(out, cloneMail(rec))
		}
	}
	return out
}

// describe fills the graph node out with what the Mailer declares, and returns
// its edges.
func (m *Mailer) describe(a *App, out *model.Node) []model.Edge {
	info := &model.MailerInfo{Transport: model.TransportCapture, MaxAttempts: m.opts.maxAttempts, Outbox: "memory"}
	if !m.opts.from.IsZero() {
		info.From = renderAddress(m.opts.from)
	}
	if a != nil {
		if a.dataDir != "" {
			info.Outbox = "file"
		}
		m.describeTransport(a, info)
		if a.running() {
			m.describeCounts(info)
		}
	}
	out.Mailer = info
	return nil
}

// describeTransport says where the mailer's mail goes: the transport in use
// while it runs, else the one the SMTP URL as it is now would build.
func (m *Mailer) describeTransport(a *App, info *model.MailerInfo) {
	m.mu.Lock()
	running, capture, relay := m.transport != nil, m.capture, m.relay
	m.mu.Unlock()
	switch {
	case running && capture == nil:
		// The transport in use, not the URL as it is now.
		info.Transport, info.Server, info.TLS = model.TransportSMTP, relay.server, relay.tls
	case running:
	default:
		v, _, err := a.kitSecret(context.Background(), smtpSecret)
		if err != nil {
			return
		}
		info.Transport = model.TransportSMTP
		if cfg, problem := parseSMTPURL(v.Value.RevealString()); problem.empty() {
			info.Server, info.TLS = smtpServer(&cfg), tlsName(cfg.TLS)
		}
	}
}

// describeCounts says what the running mailer's outbox holds and sent.
func (m *Mailer) describeCounts(info *model.MailerInfo) {
	m.mu.Lock()
	queued, sent, dead, deadKnown := len(m.ring.pending), m.ring.sent, m.ring.dead, m.ring.deadKnown
	m.mu.Unlock()
	info.Queued, info.Sent = &queued, &sent
	if deadKnown {
		info.DeadLetters = &dead
	}
}

// mails returns up to limit summaries, newest first.
func (m *Mailer) mails(limit int) []model.MailSummary {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]model.MailSummary, 0, min(max(limit, 0), len(m.ring.order)))
	for i := len(m.ring.order) - 1; i >= 0 && len(out) < limit; i-- {
		out = append(out, *summaryOf(m.ring.mails[m.ring.order[i]]))
	}
	return out
}

// mail returns one mail: whole with the capture transport, its summary with
// SMTP.
func (m *Mailer) mail(id string) (model.MailMessage, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	rec, ok := m.ring.mails[id]
	if !ok {
		return model.MailMessage{}, false
	}
	return cloneMail(rec), true
}

// reset empties the mailbox; full says whether it keeps whole mails.
func (r *mailRing) reset(full bool) {
	*r = mailRing{full: full, mails: map[string]*model.MailMessage{}, pending: map[string]bool{}}
}

// add remembers a mail, forgetting the oldest beyond mailboxSize.
func (r *mailRing) add(rec model.MailMessage) *model.MailMessage {
	if cur, ok := r.mails[rec.ID]; ok {
		return cur
	}
	stored := rec
	r.mails[rec.ID] = &stored
	r.order = append(r.order, rec.ID)
	for len(r.order) > mailboxSize {
		delete(r.mails, r.order[0])
		r.order = r.order[1:]
	}
	return &stored
}

// start opens the outbox — the SDK's mail spool over the transport the
// environment asks for — and runs it. The spool delivers one mail at a time;
// a mail it already delivered is never sent again, and every attempt carries
// the same Message-ID, its ID at the sender's domain: a redelivery after a
// crash is the one duplicate left, and a receiver can recognise it.
//
// Goroutine lifecycle: one goroutine runs the outbox until stop cancels its
// context; it closes done, which stop waits for.
func (m *Mailer) start(ctx context.Context, a *App) error {
	transport, capture, relay, err := m.openTransport(ctx, a)
	if err != nil {
		return err
	}
	dir := ""
	if a.dataDir != "" {
		dir = filepath.Join(a.dataDir, m.svc.name, "outbox", m.name)
	}
	spool, err := mailspool.New(mailspool.Config{
		Transport:       outboxTransport{m: m, a: a},
		Clock:           a.clock,
		Observe:         func(e mailspool.Event) { m.observe(a, &e) },
		Annotate:        annotate,
		NewID:           func() (string, error) { return NewID("mail"), nil },
		Dir:             dir,
		From:            m.opts.from,
		Backoff:         mailRetry,
		MaxAttempts:     m.opts.maxAttempts,
		SendTimeout:     mailSendTimeout,
		MaxMessageBytes: maxMailBytes,
		PollInterval:    pollInterval,
	})
	if err != nil {
		return failure(CodeMailQueue, "OUTBOX_OPEN", "a mailer's outbox cannot be opened", err, errs.String("mailer", m.id))
	}
	dead, deadErr := spool.DeadLetters(ctx, maxDeadLetters)
	loop := a.kitLoop(m.id+" outbox", m.id, model.LoopConsumer, consumerHow, loopOrigin{model.ProvenanceLibrary, "sdk/v1/app/mail/spool"})
	ctx, cancel := context.WithCancel(context.WithoutCancel(a.baseCtx))
	done := make(chan struct{})
	m.mu.Lock()
	m.transport, m.capture, m.spool, m.cancel, m.done, m.loop = transport, capture, spool, cancel, done, loop
	m.relay, m.problem, m.composed = relay, "", map[string][]byte{}
	m.ring.reset(capture != nil)
	m.ring.dead, m.ring.deadKnown = len(dead), deadErr == nil
	m.mu.Unlock()
	go func() {
		defer close(done)
		if err := spool.Run(ctx); err != nil && ctx.Err() == nil {
			a.problem(m.id, say("mailer.outbox-stopped", "mailer", m.id, "detail", errs.PublicOf(err)))
			loop.died(a, err)
		}
	}()
	return nil
}

// stop ends the outbox's loop and closes its spool, waiting until ctx ends.
func (m *Mailer) stop(ctx context.Context, a *App) error {
	m.mu.Lock()
	cancel, done, spool, loop := m.cancel, m.done, m.spool, m.loop
	m.mu.Unlock()
	if cancel == nil {
		return nil
	}
	cancel()
	select {
	case <-done:
	case <-ctx.Done():
		return ctx.Err()
	}
	m.mu.Lock()
	m.transport, m.spool, m.cancel = nil, nil, nil
	m.mu.Unlock()
	err := spool.Close()
	loop.setState(a, model.LoopStopped)
	return err
}

// openTransport builds the transport the environment asks for: SMTP when
// the SMTP URL is set, the capture transport otherwise.
func (m *Mailer) openTransport(ctx context.Context, a *App) (transport mail.Transport, full mail.FullTransport, relay smtpRelay, err error) {
	v, _, err := a.kitSecret(ctx, smtpSecret)
	switch {
	case errors.Is(err, secret.NotFound):
		// One delivery kept: transmit reads it at once, and the mailbox
		// keeps what the Studio shows.
		c := mail.NewCapture(1)
		return c, c, smtpRelay{}, nil
	case errors.Is(err, secret.EnvRefused):
		return nil, nil, smtpRelay{}, explain(CodeMailConfig, "MAIL_CONFIG", "set KIT_SMTP_URL or KIT_SMTP_URL_FILE, not both", err, errs.String("mailer", m.id))
	case err != nil:
		return nil, nil, smtpRelay{}, explain(CodeMailConfig, "MAIL_CONFIG", "the SMTP URL cannot be read: KIT_SMTP_URL_FILE, or kit-smtp-url in the secret store", err, errs.String("mailer", m.id))
	}
	t, relay, problem, err := smtpTransport(v.Value)
	relay.url = v.Value
	if !problem.empty() {
		return nil, nil, smtpRelay{}, failure(CodeMailConfig, "MAIL_CONFIG", "KIT_SMTP_URL is not a usable SMTP URL", err,
			errs.String("mailer", m.id), errs.String("problem", problem.String()))
	}
	return t, nil, relay, nil
}

// smtpTransport builds the SMTP transport url names and the relay it
// reaches — whose url the caller keeps —, or says what is wrong with url.
func smtpTransport(url revealStringer) (transport mail.Transport, relay smtpRelay, problem phrase, err error) {
	cfg, problem := parseSMTPURL(url.RevealString())
	if !problem.empty() {
		return nil, smtpRelay{}, problem, nil
	}
	t, err := mail.NewSMTP(cfg)
	if err != nil {
		return nil, smtpRelay{}, say("smtp.transport", "detail", errs.PublicOf(err)), err
	}
	return t, smtpRelay{server: smtpServer(&cfg), tls: tlsName(cfg.TLS)}, phrase{}, nil
}

// smtpProblem is what is wrong with url as an SMTP URL: empty when it
// builds a transport.
func smtpProblem(url revealStringer) phrase {
	cfg, problem := parseSMTPURL(url.RevealString())
	if !problem.empty() {
		return problem
	}
	if _, err := mail.NewSMTP(cfg); err != nil {
		return say("smtp.transport", "detail", errs.PublicOf(err))
	}
	return phrase{}
}

// current is the SMTP transport for the URL as it is now: rebuilt when the
// URL changed since the transport was built. A URL that went missing or no
// longer builds a transport leaves the one in use, and is logged once.
func (m *Mailer) current(ctx context.Context, a *App, transport mail.Transport, relay smtpRelay) mail.Transport {
	v, _, err := a.kitSecret(ctx, smtpSecret)
	if err == nil && v.Value.Equal(relay.url) {
		return transport
	}
	var problem phrase
	var rebuilt mail.Transport
	var next smtpRelay
	switch {
	case errors.Is(err, secret.NotFound):
		problem = say("smtp.gone")
	case err != nil:
		problem = say("smtp.unreadable", "detail", errs.PublicOf(err))
	default:
		rebuilt, next, problem, err = smtpTransport(v.Value)
		next.url = v.Value
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if !problem.empty() {
		if problem.String() != m.problem {
			m.problem = problem.String()
			fields := []logger.Attr{logger.String("node", m.id)}
			if err != nil {
				fields = append(fields, logger.String("error", errs.PublicOf(err)))
			}
			logger.Warn(ctx, a.log, "the SMTP URL "+problem.String()+"; the mailer keeps the relay it had", fields...)
		}
		return transport
	}
	if m.transport == transport {
		m.transport, m.relay, m.problem = rebuilt, next, ""
		logger.Info(ctx, a.log, "the SMTP URL changed; the mailer uses it from this mail on", logger.String("node", m.id),
			logger.String("relay", next.server))
		return rebuilt
	}
	return m.transport
}

// Send makes one delivery attempt. A failure is logged here, inside the
// attempt's span; the spool retries it, or dead-letters it after the last.
func (t outboxTransport) Send(ctx context.Context, msg mail.Message) (err error) {
	m, a := t.m, t.a
	attempt, _ := mailspool.AttemptFrom(ctx)
	if header := attempt.Meta[metaTrace]; header != "" {
		if sc, perr := trace.ParseTraceParent(header); perr == nil {
			ctx = trace.ContextWithSpanContext(ctx, sc)
		}
	}
	m.mu.Lock()
	loop := m.loop
	m.mu.Unlock()
	started := a.clock.Now()
	loop.begin(a, "")
	ctx, sp := a.begin(ctx, &spanStart{node: m.id, op: model.OpDeliverMail, name: "Deliver"})
	sp.attr("attempt", strconv.Itoa(attempt.Attempt))
	sp.attr("mail", attempt.ID)
	defer func() {
		if p := recover(); p != nil {
			logger.Error(ctx, a.log, "a mail delivery panicked", logger.String("node", m.id), logger.Any("panic", p), logger.String("stack", string(debug.Stack())))
			err = failure(CodeMailPanic, "MAIL_PANICKED", "a mail delivery panicked", nil, errs.String("mailer", m.id))
		}
		if err != nil {
			m.failed(ctx, a, attempt, err)
		}
		sp.end(err)
		loop.idle(a, time.Time{})
		loop.ran(a, started, a.clock.Now(), err)
	}()
	raw, err := m.transmit(ctx, a, &msg)
	if err == nil && raw != nil {
		m.mu.Lock()
		m.composed[attempt.ID] = raw
		m.mu.Unlock()
	}
	return err
}

// failed logs a failed attempt: a warning while attempts remain, an error on
// the last one, after which the spool gives the mail up.
func (m *Mailer) failed(ctx context.Context, a *App, attempt mailspool.Attempt, err error) {
	fields := []logger.Attr{logger.String("node", m.id), logger.String("mail", attempt.ID), logger.Int("attempt", attempt.Attempt), logger.String("error", err.Error())}
	if attempt.Attempt >= m.opts.maxAttempts {
		logger.Error(ctx, a.log, "a mail was abandoned after its last attempt", fields...)
		return
	}
	logger.Warn(ctx, a.log, "a mail delivery failed; it will be retried", fields...)
}

// transmit hands one message to the transport. With the capture transport,
// it returns the composed message.
func (m *Mailer) transmit(ctx context.Context, a *App, msg *mail.Message) ([]byte, error) {
	m.mu.Lock()
	transport, capture, relay := m.transport, m.capture, m.relay
	m.mu.Unlock()
	if transport == nil {
		return nil, Unavailable(fmt.Sprintf("mailer %q is not running", m.name))
	}
	if capture == nil {
		transport = m.current(ctx, a, transport, relay)
	}
	if err := transport.Send(ctx, *msg); err != nil {
		return nil, err
	}
	if capture == nil {
		return nil, nil
	}
	sent := capture.Sent()
	if len(sent) == 0 {
		return nil, nil
	}
	return sent[len(sent)-1].Raw, nil
}

// observe keeps the mailbox in step with what the spool reports — each
// mail's status and the counts — and streams the mail's new status. The
// spool reports a mail an earlier run queued too: the mailbox learns it then.
func (m *Mailer) observe(a *App, e *mailspool.Event) {
	if e.Kind == mailspool.EventDuplicate {
		// A redelivery of a mail already sent, dropped: nothing changed.
		return
	}
	m.mu.Lock()
	rec, known := m.ring.mails[e.ID]
	if !known {
		// Queued now, by an earlier run of the process, or forgotten by the
		// ring.
		rec = m.ring.add(m.messageOf(e, m.ring.full))
	}
	switch e.Kind {
	case mailspool.EventQueued:
		m.ring.pending[e.ID] = true
	case mailspool.EventSent:
		rec.Attempts, rec.Status, rec.Error, rec.SentAt = e.Attempt, model.MailSent, "", new(e.At.UTC())
		if raw, ok := m.composed[e.ID]; ok {
			rec.Raw = rawText(raw)
			delete(m.composed, e.ID)
		}
		delete(m.ring.pending, e.ID)
		m.ring.sent++
	case mailspool.EventRetrying:
		rec.Attempts, rec.Status, rec.Error = e.Attempt, model.MailRetrying, publicText(e.Err)
		m.ring.pending[e.ID] = true
	case mailspool.EventDeadLettered:
		rec.Attempts, rec.Status, rec.Error = e.Attempt, model.MailDead, publicText(e.Err)
		delete(m.ring.pending, e.ID)
		m.ring.dead++
	default:
		// Every other event changes nothing the mailbox shows.
	}
	summary := summaryOf(rec)
	m.mu.Unlock()
	a.hub.publish(model.Event{Type: model.EventMail, Mail: summary})
}

// messageOf is the mailbox's record of a mail the spool reports; full keeps
// the bodies and headers.
func (m *Mailer) messageOf(e *mailspool.Event, full bool) model.MailMessage {
	msg := e.Message
	traceID, _ := traceIDOf(e.Meta[metaTrace])
	out := model.MailMessage{
		ID: e.ID, Mailer: m.id, From: renderAddress(msg.From), To: renderAddresses(msg.To),
		Subject: msg.Subject, Status: model.MailQueued, QueuedAt: e.QueuedAt, TraceID: traceID, Node: e.Meta[metaNode],
	}
	if full {
		out.Text, out.HTML, out.Headers = msg.Text, msg.HTML, headersOf(&msg)
	}
	return out
}

// publicText is what the mailbox says about a failed attempt: a kit error's
// message, or the SDK's Public text — wire-safe by the SDK's contract, and
// the only part of an SDK error kit shows — never err.Error().
func publicText(err error) string {
	if ke, ok := errors.AsType[*Error](err); ok {
		return ke.Message
	}
	if p := errs.PublicOf(err); p != "" {
		return p
	}
	_, body := describe(err)
	return body.Message
}

// summaryOf is a copy of rec's summary that shares nothing with it.
func summaryOf(rec *model.MailMessage) *model.MailSummary {
	s := rec.MailSummary
	s.To = slices.Clone(s.To)
	if s.SentAt != nil {
		at := *s.SentAt
		s.SentAt = &at
	}
	return &s
}

// cloneMail is a copy of rec that shares nothing with it.
func cloneMail(rec *model.MailMessage) model.MailMessage {
	c := *rec
	c.MailSummary = *summaryOf(rec)
	if rec.Headers != nil {
		c.Headers = maps.Clone(rec.Headers)
	}
	return c
}

// renderAddress spells a as a mail header does: "Name <addr>".
func renderAddress(a mail.Address) string {
	if a.Name == "" {
		return a.Addr
	}
	return a.Name + " <" + a.Addr + ">"
}

// renderAddresses spells every address of list.
func renderAddresses(list []mail.Address) []string {
	out := make([]string, len(list))
	for i, a := range list {
		out[i] = renderAddress(a)
	}
	return out
}

// headersOf renders what a mail says besides its sender, recipients, subject
// and bodies. Bcc is not among them: it reaches the envelope and no header.
func headersOf(msg *mail.Message) map[string]string {
	h := map[string]string{}
	if !msg.Date.IsZero() {
		h["Date"] = msg.Date.Format(time.RFC1123Z)
	}
	if msg.MessageID != "" {
		h["Message-ID"] = "<" + strings.Trim(msg.MessageID, "<>") + ">"
	}
	setAddresses(h, "Cc", msg.Cc)
	setAddresses(h, "Reply-To", msg.ReplyTo)
	for _, f := range msg.Headers {
		if prev, ok := h[f.Name]; ok {
			h[f.Name] = prev + ", " + f.Value
		} else {
			h[f.Name] = f.Value
		}
	}
	if len(msg.Attachments) > 0 {
		h["X-Kit-Attachments"] = attachmentsText(msg.Attachments)
	}
	return h
}

// setAddresses sets the header name to the addresses of list, when there are
// any.
func setAddresses(h map[string]string, name string, list []mail.Address) {
	if len(list) > 0 {
		h[name] = strings.Join(renderAddresses(list), ", ")
	}
}

// attachmentsText names every attachment with its size.
func attachmentsText(atts []mail.Attachment) string {
	names := make([]string, len(atts))
	for i, att := range atts {
		names[i] = fmt.Sprintf("%s (%s)", att.Filename, humanBytes(int64(len(att.Content))))
	}
	return strings.Join(names, ", ")
}

// rawText is the composed message as the Studio shows it, cut at maxRawMail.
func rawText(raw []byte) string {
	if len(raw) <= maxRawMail {
		return string(raw)
	}
	return string(raw[:maxRawMail]) + "\r\n[… cut by kit at " + humanBytes(maxRawMail) + "]"
}

// traceIDOf is the trace ID of a traceparent header, "" when it has none.
func traceIDOf(header string) (string, bool) {
	if header == "" {
		return "", false
	}
	sc, err := trace.ParseTraceParent(header)
	if err != nil {
		return "", false
	}
	return sc.TraceID.String(), true
}

// smtpServer is where cfg delivers, host:port.
func smtpServer(cfg *mail.SMTPConfig) string {
	return net.JoinHostPort(cfg.Host, strconv.Itoa(cfg.Port))
}

// tlsName is the TLS mode as the URL spells it: starttls, implicit or none.
func tlsName(mode mail.TLSMode) string {
	switch mode {
	case mail.TLSStartTLS:
		return "starttls"
	case mail.TLSImplicit:
		return "implicit"
	case mail.TLSDisabled:
		return "none"
	default:
		// A mode a parsed URL never holds: parseSMTPURL refuses the unset one.
		return "unset"
	}
}

// parseSMTPURL reads smtp://user:password@host:port?tls=starttls|implicit|none
// (smtps:// for implicit TLS) with the SDK's mail.ParseURL, which refuses
// what its SMTP transport would. The configuration holds the password: it is
// built where the transport is, handed to mail.NewSMTP, and never kept or
// printed. The problem is the clause the SDK named, which never quotes the
// URL.
func parseSMTPURL(raw string) (mail.SMTPConfig, phrase) {
	cfg, err := mail.ParseURL(raw)
	if err == nil {
		return cfg, phrase{}
	}
	clause := ""
	for _, f := range errs.FieldsOf(err) {
		if f.Key() == "problem" {
			clause = f.StringValue()
		}
	}
	switch {
	case errors.Is(err, mail.InvalidURL) && clause != "":
		return mail.SMTPConfig{}, say("smtp.unusable", "clause", clause)
	case clause != "":
		return mail.SMTPConfig{}, say("smtp.refused-because", "detail", errs.PublicOf(err), "clause", clause)
	}
	return mail.SMTPConfig{}, say("smtp.refused", "detail", errs.PublicOf(err))
}

// mailerProblems judges the mailers against the environment: capture outside
// dev delivers nothing — a warning; an SMTP URL that cannot be used refuses
// to start. Building the transport dials nothing.
func (a *App) mailerProblems() []model.Diagnostic {
	if !a.hasMailer() {
		return nil
	}
	set, problem := a.smtpState()
	var out []model.Diagnostic
	for _, m := range a.mailers() {
		switch {
		case !set && a.cfg.env != EnvDev:
			out = append(out, diagnosticOf("warning", m.id, a.source(&m.decl), say("mailer.capture", "mailer", m.id)))
		case !problem.empty():
			out = append(out, diagnosticOf("error", m.id, a.source(&m.decl), say("mailer.smtp", "mailer", m.id, "problem", problem)))
		}
	}
	return out
}

// smtpState says whether the SMTP URL is set, and what is wrong with it.
func (a *App) smtpState() (set bool, problem phrase) {
	v, _, err := a.kitSecret(context.Background(), smtpSecret)
	switch {
	case err == nil:
		return true, smtpProblem(v.Value)
	case !errors.Is(err, secret.NotFound):
		return true, say("smtp.unreadable", "detail", errs.PublicOf(err))
	default:
		return false, phrase{}
	}
}

// mailers are the app's mailers, in its services' order.
func (a *App) mailers() []*Mailer {
	var out []*Mailer
	for _, svc := range a.services {
		if svc == nil {
			continue
		}
		nodes, _ := svc.snapshot()
		for _, n := range nodes {
			if m, ok := n.(*Mailer); ok {
				out = append(out, m)
			}
		}
	}
	return out
}

// hasMailer reports whether the app declares a mailer.
func (a *App) hasMailer() bool {
	for _, svc := range a.services {
		if svc == nil {
			continue
		}
		nodes, _ := svc.snapshot()
		for _, n := range nodes {
			if n.base().kind == model.KindMailer {
				return true
			}
		}
	}
	return false
}

// NewMailer is a mailer no service declares yet, with the default attempts:
// [Service.Mailer] makes one and declares it, which is how a product gets
// one.
func NewMailer() *Mailer { return &Mailer{opts: mailerOptions{maxAttempts: defaultMailAttempts}} }
