package plug

import (
	"context"
	"sync/atomic"
	"time"
)

// The mail subsystem's contract (ADR 0179 §3, step 6): what kit's core asks
// of the outbox its mailers keep and of the transports that empty it.
// framework/app/mail sets the hook and framework/internal/app/mailengine
// implements it over the SDK's mail spool and its SMTP and capture
// transports; the core reads it and imports neither the spool nor a
// transport. A mail crosses as a MailValue, the SDK's mail.Message
// held as any: the core names the SDK's mail types — Mailer.Send takes one —,
// plug names none. The values are here, the spool and the transport in
// mail_interface.go.

// The spool's refusals kit tells apart, which it words for the product.
const (
	// MailRefusalNone is no refusal kit words.
	MailRefusalNone MailRefusal = iota
	// MailRefusalClosed is a spool that is closed: its mailer is not
	// running.
	MailRefusalClosed
	// MailRefusalUnencodable is a mail the outbox cannot encode.
	MailRefusalUnencodable
	// MailRefusalInvalidURL is an SMTP URL that is not one of the form
	// smtp://user:password@host:port; its "problem" field says the clause.
	MailRefusalInvalidURL
)

// What a spool reports of a mail.
const (
	// MailEventOther is an event of the spool kit shows nothing of.
	MailEventOther MailEvent = iota
	// MailEventQueued is a mail put in the outbox.
	MailEventQueued
	// MailEventSent is a mail the transport accepted.
	MailEventSent
	// MailEventRetrying is a failed attempt that will be retried.
	MailEventRetrying
	// MailEventDeadLettered is a mail given up on after its last attempt.
	MailEventDeadLettered
	// MailEventDuplicate is a redelivery of a mail already sent, dropped.
	MailEventDuplicate
)

// Mail is the mail subsystem: the outbox kit's mailers keep and the
// transports that empty it; nil until framework/app/mail is imported. A
// product that declares a mailer without it is refused at the start (ADR
// 0179 §6).
var Mail atomic.Pointer[MailDriverConfig]

// MailDriverConfig is what framework/app/mail plugs into kit: how a
// mailer's outbox opens, the SDK's verdict on a mail, the transports a
// mailer hands its mail to, and how kit tells the spool's refusals apart.
type MailDriverConfig struct {
	// Open opens one mailer's outbox as cfg says, closed until it runs.
	Open func(cfg *MailSpoolConfig) (MailSpool, error)
	// Validate is the SDK's verdict on a mail: nil when the outbox takes it,
	// else what is wrong with it, in the SDK's public words.
	Validate func(msg MailValue) error
	// Capture is a transport that delivers nothing and keeps the last
	// message it composed (MailCaptureSource).
	Capture func() MailSink
	// ParseSMTP reads an SMTP URL — smtp://user:password@host:port?tls=
	// starttls|implicit|none, smtps:// for implicit TLS — as the SDK's
	// SMTP transport would take it.
	ParseSMTP func(url string) (MailRelayValue, error)
	// NewSMTP is the SDK's SMTP transport to relay, which dials nothing
	// until a mail is sent.
	NewSMTP func(relay *MailRelayValue) (MailSink, error)
	// Refused reports whether err, or an error it wraps, is the spool's or
	// the URL's refusal of kind: the SDK's sentinels stay in the subsystem,
	// and the error keeps the fields the SDK gave it.
	Refused func(err error, kind MailRefusal) bool
}

// MailRefusal is a refusal of the mail subsystem, as kit words it.
type MailRefusal uint8

// MailEvent is what a spool reports of a mail.
type MailEvent uint8

// MailSpoolConfig is one mailer's outbox: the transport each attempt runs,
// the app's clock, what kit hears of its mails, its retries, and where it
// keeps them — a queue on the database that keeps the mailer's service, a
// directory, or memory.
type MailSpoolConfig struct {
	// Transport makes one delivery attempt of msg; the spool retries a
	// failure, or dead-letters the mail after its last attempt.
	Transport func(ctx context.Context, msg MailValue, attempt MailAttemptValue) error
	// Clock is the app's clock, which the spool stamps and waits on.
	Clock Clock
	// Observe is told each event of each mail.
	Observe func(e *MailEventValue)
	// Annotate is what the spool keeps of a Send for its deliveries, read
	// from the Send's context.
	Annotate func(ctx context.Context) map[string]string
	// NewID mints a mail's outbox ID.
	NewID func() (string, error)
	// Outbox is the queue the spool keeps its mails in on a database —
	// the queue subsystem's, sealed —, nil for a directory or memory.
	Outbox Broker
	// Database is the database Outbox is on, whose transaction a mail sent
	// in one joins; nil for none.
	Database Database
	// From is the sender of every mail that names none: the SDK's
	// mail.Address, held as any.
	From MailValue
	// Dir is the directory the spool keeps its own queue in; with no Outbox
	// and no Dir, the outbox is in memory.
	Dir string
	// BackoffBase is the wait after a failed attempt, doubled after each,
	// up to BackoffMax.
	BackoffBase time.Duration
	// BackoffMax bounds the wait after a failed attempt.
	BackoffMax time.Duration
	// SendTimeout bounds one attempt.
	SendTimeout time.Duration
	// PollInterval bounds an idle wait.
	PollInterval time.Duration
	// MaxAttempts is how many attempts a mail gets before it is
	// dead-lettered.
	MaxAttempts int
	// MaxMessageBytes bounds one mail in the spool's own queue; zero is the
	// spool's own bound.
	MaxMessageBytes int
}

// MailValue is a value of the SDK's mail package as it crosses the
// contract: a mail.Message, or a sender's mail.Address, held as any — kit's
// core names the SDK's mail types, plug names none.
type MailValue struct {
	// Value is the SDK's mail value.
	Value any
}

// MailRelayValue is an SMTP relay as the SDK's transport takes it: its
// configuration, which holds the password and is handed to NewSMTP and never
// kept or printed, and what may be shown of it.
type MailRelayValue struct {
	// Config is the SDK's SMTP configuration, held as any.
	Config any
	// Server is where it delivers, host:port.
	Server string
	// TLS is its TLS mode as the URL spells it: starttls, implicit or none.
	TLS string
}

// MailAttemptValue is one delivery attempt of a mail: its outbox ID, its
// number, when the mail was queued, and what the spool kept of its Send.
type MailAttemptValue struct {
	// QueuedAt is when the mail was put in the outbox.
	QueuedAt time.Time
	// Meta is what Annotate kept of the Send.
	Meta map[string]string
	// ID is the mail's outbox ID.
	ID string
	// Attempt numbers the attempt, from 1.
	Attempt int
}

// MailEventValue is what a spool reports of a mail: its kind, the mail and
// its outbox ID, the attempt, when, and the failure of a failed attempt.
type MailEventValue struct {
	// Err is a failed attempt's failure.
	Err error
	// Meta is what Annotate kept of the Send.
	Meta map[string]string
	// At is when the event happened.
	At time.Time
	// QueuedAt is when the mail was put in the outbox.
	QueuedAt time.Time
	// Message is the mail.
	Message MailValue
	// ID is the mail's outbox ID.
	ID string
	// Attempt numbers the attempt the event is about.
	Attempt int
	// Kind is what happened.
	Kind MailEvent
}
