package mailkit

import (
	"context"

	"github.com/kitsunium/sdk/framework/internal/kit/plug"
	"github.com/kitsunium/sdk/framework/internal/kit/queuekit"
	"github.com/kitsunium/sdk/framework/internal/kit/storekit"
	mailspool "github.com/kitsunium/sdk/pkg/v1/app/mail/spool"
	"github.com/kitsunium/sdk/pkg/v1/app/resilience"
	"github.com/kitsunium/sdk/pkg/v1/data/sql"
	"github.com/kitsunium/sdk/pkg/v1/errs"
)

// A mailer's outbox as kit's core holds it (plug.MailSpool): the SDK's mail
// spool over the transport kit's core runs each attempt through, on the
// app's clock, in the queue the outbox lives in — on the database that keeps
// the mailer's service, the queue subsystem's sealed broker joined to the
// database's transaction manager (ADR 0178 C1, C7 item 7), else its own in
// a directory or in memory.

// spool is the SDK's mail spool as kit's core uses it.
type spool struct {
	s *mailspool.Spool
}

// open opens the SDK's spool over cfg. An outbox on a database takes the
// SDK's broker the queue subsystem opened there (queuekit.BrokerOf) and the
// database's transaction manager the store subsystem opened, a Deferrer
// that is an Aborter too (storekit.SQLOf); without one, the spool tells the
// mailbox a mail was queued when Send returns.
//
// IFACE-PLUGIN: kit reaches a mailer's outbox through plug.MailSpool; this
// is its implementation.
func open(cfg *plug.MailSpoolConfig) (plug.MailSpool, error) {
	sc := mailspool.Config{
		Transport:       attempts{run: cfg.Transport},
		Clock:           clockOf(cfg.Clock),
		Observe:         observeOf(cfg.Observe),
		Annotate:        cfg.Annotate,
		NewID:           cfg.NewID,
		From:            addressOf(cfg.From),
		Backoff:         backoffOf(cfg),
		MaxAttempts:     cfg.MaxAttempts,
		SendTimeout:     cfg.SendTimeout,
		PollInterval:    cfg.PollInterval,
		Dir:             cfg.Dir,
		MaxMessageBytes: cfg.MaxMessageBytes,
	}
	if cfg.Outbox != nil {
		b, ok := queuekit.BrokerOf(cfg.Outbox)
		if !ok {
			return nil, errs.Wrap(mailspool.Misconfigured, errs.WrapParams{},
				errs.String("problem", "the outbox's queue was not opened by the queue subsystem"))
		}
		sc.Broker = b
		sc.Deferrer = deferrerOf(cfg.Database)
	}
	s, err := mailspool.New(sc)
	if err != nil {
		return nil, err
	}
	return spool{s: s}, nil
}

// backoffOf is the SDK's exponential backoff at cfg's bounds: the wait
// after a failed attempt, from BackoffBase, doubling, up to BackoffMax.
func backoffOf(cfg *plug.MailSpoolConfig) resilience.Backoff {
	return resilience.Backoff{BaseDelay: cfg.BackoffBase, MaxDelay: cfg.BackoffMax}
}

// deferrerOf is the transaction manager of db, a database the store
// subsystem opened, as the spool defers on it: a Deferrer that is an Aborter
// too, as the SDK's is; nil otherwise.
func deferrerOf(db plug.Database) sql.Deferrer {
	if db == nil {
		return nil
	}
	tm, _, ok := storekit.SQLOf(db)
	if !ok {
		return nil
	}
	df, defers := tm.(sql.Deferrer)
	if _, aborts := tm.(sql.Aborter); !defers || !aborts {
		return nil
	}
	return df
}

// observeOf is fn as the spool tells it each event, nil for none.
func observeOf(fn func(e *plug.MailEventValue)) func(mailspool.Event) {
	if fn == nil {
		return nil
	}
	return func(e mailspool.Event) {
		fn(&plug.MailEventValue{
			Err: e.Err, Meta: e.Meta, At: e.At, QueuedAt: e.QueuedAt, Message: plug.MailValue{Value: e.Message},
			ID: e.ID, Attempt: e.Attempt, Kind: eventOf(e.Kind),
		})
	}
}

// events are the spool's events kit's core tells apart.
var events = map[mailspool.EventKind]plug.MailEvent{
	mailspool.EventQueued:       plug.MailEventQueued,
	mailspool.EventSent:         plug.MailEventSent,
	mailspool.EventRetrying:     plug.MailEventRetrying,
	mailspool.EventDeadLettered: plug.MailEventDeadLettered,
	mailspool.EventDuplicate:    plug.MailEventDuplicate,
}

// eventOf is the spool's event kind k as kit's core reads it.
func eventOf(k mailspool.EventKind) plug.MailEvent {
	if e, ok := events[k]; ok {
		return e
	}
	return plug.MailEventOther
}

// attemptFrom is the attempt the spool put in ctx.
func attemptFrom(ctx context.Context) (plug.MailAttemptValue, bool) {
	a, ok := mailspool.AttemptFrom(ctx)
	return plug.MailAttemptValue{QueuedAt: a.QueuedAt, Meta: a.Meta, ID: a.ID, Attempt: a.Attempt}, ok
}

// Send puts msg, a mail.Message, in the outbox.
func (s spool) Send(ctx context.Context, msg plug.MailValue) (string, error) {
	return s.s.Send(ctx, messageOf(msg))
}

// SendWithID puts msg, a mail.Message, in the outbox under id.
func (s spool) SendWithID(ctx context.Context, id string, msg plug.MailValue) error {
	return s.s.SendWithID(ctx, id, messageOf(msg))
}

// DeadLetters counts up to maxLetters of the outbox's dead letters.
func (s spool) DeadLetters(ctx context.Context, maxLetters int) (int, error) {
	dead, err := s.s.DeadLetters(ctx, maxLetters)
	return len(dead), err
}

// Run hands each mail to the transport until ctx ends.
func (s spool) Run(ctx context.Context) error { return s.s.Run(ctx) }

// Close releases the outbox.
func (s spool) Close() error { return s.s.Close() }
