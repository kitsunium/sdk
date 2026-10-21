package app

import (
	"context"
	"net/textproto"
	"runtime/debug"
	"time"

	"github.com/kitsunium/sdk/framework/internal/app/plug"
	"github.com/kitsunium/sdk/framework/model"
	"github.com/kitsunium/sdk/pkg/v1/app/mail"
	"github.com/kitsunium/sdk/pkg/v1/observe/logger"
	"github.com/kitsunium/sdk/pkg/v1/observe/trace"
	"github.com/kitsunium/sdk/pkg/v1/security/secret"
)

// A mail given up (issue #489). The spool dead-letters a mail after its last
// attempt, and — since the SDK's #486 — at once when the receiving server
// refuses it for good (an SMTP 5xx reply, RFC 5321 §4.2.1) and when an
// attempt fails once the mail is older than its mailer's max-age. The
// mailbox the Studio reads records why (model.MailSummary's GivenUp, and the
// reply's code, never its text), and the mailer's bounce hook hears of it:
// the framework's own logs it, a product's ([OnBounce]) replaces it.
//
// Each mailer declares two settings beside it, which config/<env>.* and
// <PRODUCT>_<NAME> override like any other: <mailer>-max-age (a duration,
// [MailMaxAge]'s, 5 days by default; 0 sets no age) and
// <mailer>-retry-permanent (false: a 5xx gives the mail up at once; true, for
// a relay known to answer 5xx for a condition that passes, retries it).

// The suffixes of a mailer's settings.
const (
	maxAgeSuffix         = "max-age"
	retryPermanentSuffix = "retry-permanent"
)

// defaultMailMaxAge is how long after it was queued a mail may still be
// retried, unless the mailer says otherwise: RFC 5321 §4.5.4.1 — "the
// give-up time generally needs to be at least 4-5 days" —, at its upper
// bound, so that a relay down over a long weekend loses no mail.
const defaultMailMaxAge time.Duration = 5 * 24 * time.Hour

// BounceEvent is a mail its mailer gave up on: the receiving server refused it for
// good, it outlived its mailer's max-age, or its last attempt failed —
// GivenUp says which. It stays in the outbox as a dead letter.
//
// Reply is the receiving server's own answer: the hook may read it, log it or
// act on it, but the framework never puts it in a message it shows — the
// Studio's mailbox keeps its code alone.
type BounceEvent struct {
	// Err is the last attempt's failure.
	Err error
	// Reply is the SMTP reply that failure carries, nil when the server
	// answered none — a relay that could not be reached, a timeout.
	Reply *textproto.Error
	// Message is the mail, as it was queued.
	Message mail.Message
	// QueuedAt is when it entered the outbox.
	QueuedAt time.Time
	// At is when it was given up, on the app's clock.
	At time.Time
	// Mailer is the mailer's node ID.
	Mailer string
	// ID is the mail's outbox ID.
	ID string
	// GivenUp is why: model.MailRejected, model.MailExpired or
	// model.MailExhausted.
	GivenUp string
	// Attempts is how many deliveries it got.
	Attempts int
}

// OnBounce replaces what the mailer does with a mail it gives up — by
// default, it logs it as an error naming why — with fn: mark an address
// undeliverable, tell an operator, send it another way. fn runs on a
// goroutine of its own once the mailbox is updated — the outbox goes on
// meanwhile, and fn may send mail —, with a context on the mailer's node that
// continues the trace of the Send; the mailer's stop waits for it. A panic in
// fn is logged and changes nothing else. A nil fn keeps the framework's own.
//
// IFACE-OPAQUE: the option is sealed — its method is unexported — so a caller
// only hands it to the declaration it configures.
func OnBounce(fn func(ctx context.Context, b *BounceEvent)) MailerConfigurer {
	return mailerOption(func(o *mailerOptions) { o.bounce = fn })
}

// MailMaxAge is how long after it was queued a mail may still be retried: an
// attempt that fails later gives it up, as expired, whatever attempts it had
// left. It is the default of the mailer's setting <mailer>-max-age, which
// config/<env>.* and <PRODUCT>_<MAILER>_MAX_AGE override; 0 sets no age. The
// default is 5 days (RFC 5321 §4.5.4.1).
//
// IFACE-OPAQUE: the option is sealed — its method is unexported — so a caller
// only hands it to the declaration it configures.
func MailMaxAge(d time.Duration) MailerConfigurer {
	return mailerOption(func(o *mailerOptions) { o.maxAge = d })
}

// declareSettings declares the mailer's settings in svc, the service that
// declares it: <mailer>-max-age, defaulting to [MailMaxAge]'s, never
// negative, and <mailer>-retry-permanent, false.
func (m *Mailer) declareSettings(svc *Service) {
	if m.opts.maxAge < 0 {
		svc.problem(m.decl, m.id, "mailer.max-age-negative", "name", m.name)
	}
	m.maxAgeFor = declareNodeSetting(svc, &m.nodeBase, maxAgeSuffix, max(m.opts.maxAge, 0), func(v any) phrase {
		if d, ok := v.(time.Duration); ok && d < 0 {
			return say("mailer.max-age-negative", "name", m.name)
		}
		return phrase{}
	})
	m.retryPermanentFor = declareNodeSetting(svc, &m.nodeBase, retryPermanentSuffix, false, nil)
}

// declareNodeSetting declares in svc the setting <node>-<suffix> the
// framework keeps for n, with def as its default and check judging its
// resolved value; nil, with a declaration error, when the node's name makes
// no setting's.
func declareNodeSetting[T SettingValue](svc *Service, n *nodeBase, suffix string, def T, check func(v any) phrase) *SettingService[T] {
	name := nodeSettingName(n.name, suffix)
	if secret.ValidateName(name) != nil {
		svc.problem(n.decl, n.id, "node.setting-name", "node", n.name, "setting", name, "max", secret.MaxNameLen)
		return nil
	}
	st := NewSettingService(def)
	st.svc, st.name, st.decl, st.check = svc, name, n.decl, check
	st.id = svc.name + "/setting/" + name
	svc.mu.Lock()
	svc.settings = append(svc.settings, st)
	svc.mu.Unlock()
	return st
}

// maxAge is the mailer's <mailer>-max-age in the app that runs it.
func (m *Mailer) maxAge() time.Duration {
	if m.maxAgeFor == nil {
		return max(m.opts.maxAge, 0)
	}
	return max(m.maxAgeFor.Get(), 0)
}

// retriesPermanent is the mailer's <mailer>-retry-permanent in the app that
// runs it.
func (m *Mailer) retriesPermanent() bool {
	return m.retryPermanentFor != nil && m.retryPermanentFor.Get()
}

// bounced hands the mail e reports given up to the mailer's bounce hook, in a
// context of the app's on the mailer's node — app.Log and every API that reads
// the running app find it there —, continuing the trace of the Send. The
// mailer's stop waits for the hook rather than cancel it, so that a mail it
// sends still reaches the outbox.
func (m *Mailer) bounced(a *App, e *plug.MailEventValue) {
	ctx := withNode(context.WithoutCancel(a.baseCtx), m.id, a)
	if sc, err := trace.ParseTraceParent(e.Meta[metaTrace]); err == nil {
		ctx = trace.ContextWithSpanContext(ctx, sc)
	}
	msg, _ := e.Message.Value.(mail.Message)
	b := &BounceEvent{
		Err: e.Err, Reply: e.Reply, Message: msg, QueuedAt: e.QueuedAt, At: e.At,
		Mailer: m.id, ID: e.ID, GivenUp: givenUpName(e.GivenUp), Attempts: e.Attempt,
	}
	hook := m.opts.bounce
	if hook == nil {
		m.logBounce(ctx, a, b)
		return
	}
	defer func() {
		if p := recover(); p != nil {
			logger.Error(ctx, a.log, "a mailer's bounce hook panicked", logger.String("node", m.id), logger.String("mail", b.ID),
				logger.Any("panic", p), logger.String("stack", string(debug.Stack())))
		}
	}()
	hook(ctx, b)
}

// logBounce is the framework's bounce hook: an error naming the mail, why it
// was given up, its attempts, the reply's code — never its text — and the
// failure as an SDK error renders it, its public half.
func (m *Mailer) logBounce(ctx context.Context, a *App, b *BounceEvent) {
	fields := []logger.Attr{
		logger.String("node", m.id), logger.String("mail", b.ID), logger.String("given_up", b.GivenUp),
		logger.Int("attempts", b.Attempts),
	}
	if code := replyCode(b.Reply); code != 0 {
		fields = append(fields, logger.Int("reply", code))
	}
	if b.Err != nil {
		fields = append(fields, logger.String("error", b.Err.Error()))
	}
	logger.Error(ctx, a.log, "a mail was given up: "+b.GivenUp, fields...)
}

// givenUps are the mailbox's words for why the spool gave a mail up.
var givenUps = map[plug.MailGiveUp]string{
	plug.MailGivenUpExhausted: model.MailExhausted,
	plug.MailGivenUpRejected:  model.MailRejected,
	plug.MailGivenUpExpired:   model.MailExpired,
}

// givenUpName is why the spool gave a mail up, in the mailbox's words;
// exhausted for a reason the framework does not know.
func givenUpName(g plug.MailGiveUp) string {
	if name, ok := givenUps[g]; ok {
		return name
	}
	return model.MailExhausted
}

// replyCode is reply's code, 0 for none.
func replyCode(reply *textproto.Error) int {
	if reply == nil {
		return 0
	}
	return reply.Code
}
